package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// The screenshot reader is an internal service (docs/screenshot-reader.md)
// that reads the eFootball Full Time screen from a player's screenshot. It only
// reads. Go never trusts a single screenshot: it re-checks the numbers, maps
// the team names to the two players, looks for screenshots or stats reused
// from another match, and lets the reader decide a disputed result only when
// both players' own screenshots agree with each other and with exactly one
// claim.

// screenshotStatKeys are the rows of the Full Time stats table, in screen
// order, under the reader's fixed keys.
var screenshotStatKeys = []string{"possession", "shots", "shotsOnTarget", "fouls", "offsides", "cornerKicks",
	"freeKicks", "passes", "successfulPasses", "crosses", "interceptions", "tackles", "saves"}

var screenshotImageHashPattern = regexp.MustCompile(`^[0-9a-f]{16}$`)

// screenshotSide is one team as the reader saw it.
type screenshotSide struct {
	Team       string   `json:"team"`
	Score      int      `json:"score"`
	Confidence *float64 `json:"confidence"`
}

// visionReadResponse is the reader's answer to POST /v1/read.
type visionReadResponse struct {
	Screen    string           `json:"screen"`
	FullTime  *bool            `json:"fullTime"`
	Left      *screenshotSide  `json:"left"`
	Right     *screenshotSide  `json:"right"`
	Penalties *json.RawMessage `json:"penalties"`
	Stats     map[string][]int `json:"stats"`
	// Confidence is the reader's belief that both scores and both team names
	// are right.
	Confidence float64 `json:"confidence"`
	ImageHash  string  `json:"imageHash"`
	Model      struct {
		Engine     string `json:"engine"`
		Version    string `json:"version"`
		Recognizer string `json:"recognizer"`
	} `json:"model"`
}

type screenshotPenalties struct {
	Left  int `json:"left"`
	Right int `json:"right"`
}

// screenshotReading is a reader result after Go's own validation.
type screenshotReading struct {
	Screen         string
	FullTime       *bool
	Confidence     float64
	LeftTeam       string
	LeftScore      int
	RightTeam      string
	RightScore     int
	LeftPenalties  *int
	RightPenalties *int
	Stats          map[string][2]int
	ImageHash      *string
	Engine         string
	ModelVersion   string
}

// visionError is a failed read. Retryable means the reader was unavailable
// (down, overloaded, misconfigured token): the screenshot is fine and is
// retried for as long as the outage lasts. Otherwise this image can never be
// read and staff decide without a reading.
type visionError struct {
	Retryable bool
	// Busy means the reader shed load (503 busy, 429): retried within
	// seconds, without counting towards the outage limit.
	Busy    bool
	Message string
}

func (e *visionError) Error() string { return e.Message }

// visionClient calls the reader over the internal network.
type visionClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// read sends one image and validates the answer.
func (c visionClient) read(ctx context.Context, image []byte, mediaType, evidenceID, requestID string) (
	screenshotReading, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/read", bytes.NewReader(image))
	if err != nil {
		return screenshotReading{}, &visionError{Message: "reader URL is invalid"}
	}
	request.Header.Set("Content-Type", mediaType)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("X-Evidence-ID", evidenceID)
	if requestID != "" {
		request.Header.Set("X-Request-ID", requestID)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return screenshotReading{}, &visionError{Retryable: true, Message: "reader unreachable: " + classifyNetworkError(err)}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return screenshotReading{}, &visionError{Retryable: true, Message: "reader response unreadable"}
	}
	if response.StatusCode != http.StatusOK {
		// The reader refuses an image it can never read (4xx) and fails for
		// reasons that pass (5xx). A rejected token or rate limit is the
		// reader being unavailable, not the image's fault.
		code := readerErrorCode(body)
		retryable := response.StatusCode >= 500 || response.StatusCode == http.StatusUnauthorized ||
			response.StatusCode == http.StatusTooManyRequests
		busy := response.StatusCode == http.StatusTooManyRequests ||
			(response.StatusCode == http.StatusServiceUnavailable && code == "busy")
		return screenshotReading{}, &visionError{Retryable: retryable, Busy: busy,
			Message: fmt.Sprintf("reader returned %d: %s", response.StatusCode, code)}
	}
	var decoded visionReadResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return screenshotReading{}, &visionError{Message: "reader answered with invalid JSON"}
	}
	reading, err := validateVisionResponse(decoded)
	if err != nil {
		return screenshotReading{}, &visionError{Message: "reader answer rejected: " + err.Error()}
	}
	return reading, nil
}

// healthy reports whether the reader answers GET /healthz with 200, so the
// worker never claims screenshots while it is down.
func (c visionClient) healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return false
	}
	response, err := c.http.Do(request)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	return response.StatusCode == http.StatusOK
}

func classifyNetworkError(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "timed out"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "connection failed"
	}
}

// readerErrorCode keeps only the reader's error code, never free text.
func readerErrorCode(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" && len(payload.Error) <= 64 {
		return payload.Error
	}
	return "no error code"
}

// validateVisionResponse keeps only well-formed fields. A reading that is not
// the Full Time screen keeps nothing but its screen and model.
func validateVisionResponse(response visionReadResponse) (screenshotReading, error) {
	version := response.Model.Version
	if recognizer := strings.TrimSpace(response.Model.Recognizer); recognizer != "" {
		version += " (" + recognizer + ")"
	}
	reading := screenshotReading{
		Screen: response.Screen, Engine: truncateRunes(response.Model.Engine, 64),
		ModelVersion: truncateRunes(version, 128), Stats: map[string][2]int{},
	}
	if reading.Engine == "" {
		reading.Engine = "unknown"
	}
	if math.IsNaN(response.Confidence) || response.Confidence < 0 || response.Confidence > 1 {
		return screenshotReading{}, errors.New("confidence outside 0..1")
	}
	reading.Confidence = math.Round(response.Confidence*1000) / 1000
	if hash := strings.ToLower(strings.TrimSpace(response.ImageHash)); screenshotImageHashPattern.MatchString(hash) {
		reading.ImageHash = &hash
	}
	switch response.Screen {
	case "unknown":
		return reading, nil
	case "match_result":
	default:
		return screenshotReading{}, fmt.Errorf("screen %q", response.Screen)
	}
	if response.Left == nil || response.Right == nil {
		return screenshotReading{}, errors.New("a match result needs both sides")
	}
	for _, side := range []*screenshotSide{response.Left, response.Right} {
		if side.Score < 0 || side.Score > 99 {
			return screenshotReading{}, errors.New("score outside 0..99")
		}
	}
	reading.FullTime = response.FullTime
	reading.LeftTeam, reading.RightTeam = cleanTeamName(response.Left.Team), cleanTeamName(response.Right.Team)
	reading.LeftScore, reading.RightScore = response.Left.Score, response.Right.Score
	if response.Penalties != nil && string(*response.Penalties) != "null" {
		var penalties screenshotPenalties
		if err := json.Unmarshal(*response.Penalties, &penalties); err != nil ||
			penalties.Left < 0 || penalties.Left > 99 || penalties.Right < 0 || penalties.Right > 99 {
			return screenshotReading{}, errors.New("penalties malformed")
		}
		reading.LeftPenalties, reading.RightPenalties = &penalties.Left, &penalties.Right
	}
	for _, key := range screenshotStatKeys {
		values, ok := response.Stats[key]
		if !ok {
			continue
		}
		limit := 999
		if key == "possession" {
			limit = 100
		}
		// A malformed row is dropped rather than trusted.
		if len(values) != 2 || values[0] < 0 || values[1] < 0 || values[0] > limit || values[1] > limit {
			continue
		}
		reading.Stats[key] = [2]int{values[0], values[1]}
	}
	return reading, nil
}

func cleanTeamName(name string) string {
	printable := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, name)
	return truncateRunes(strings.Join(strings.Fields(printable), " "), 80)
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return value
}

// screenshotPlausibility recomputes the stats checks in Go. A goal is a shot
// on target and a save stops one, so goals plus the other side's saves never
// exceed a side's shots on target. An own goal is the rare honest exception,
// which is why a failed check is a flag for staff, never a rejection.
func screenshotPlausibility(reading screenshotReading) []string {
	failed := []string{}
	stat := func(key string) ([2]int, bool) {
		value, ok := reading.Stats[key]
		return value, ok
	}
	if possession, ok := stat("possession"); ok && math.Abs(float64(possession[0]+possession[1]-100)) > 1 {
		failed = append(failed, "possession_not_100")
	}
	onTarget, hasOnTarget := stat("shotsOnTarget")
	if shots, ok := stat("shots"); ok && hasOnTarget && (onTarget[0] > shots[0] || onTarget[1] > shots[1]) {
		failed = append(failed, "on_target_exceeds_shots")
	}
	if hasOnTarget && (reading.LeftScore > onTarget[0] || reading.RightScore > onTarget[1]) {
		failed = append(failed, "goals_exceed_shots_on_target")
	} else if saves, ok := stat("saves"); ok && hasOnTarget &&
		(reading.LeftScore+saves[1] > onTarget[0] || reading.RightScore+saves[0] > onTarget[1]) {
		failed = append(failed, "goals_and_saves_exceed_shots_on_target")
	}
	if passes, ok := stat("passes"); ok {
		if successful, ok := stat("successfulPasses"); ok && (successful[0] > passes[0] || successful[1] > passes[1]) {
			failed = append(failed, "successful_exceeds_passes")
		}
	}
	return failed
}

// screenshotStatsFingerprint identifies a screen by its numbers. Two matches
// never produce the same full table, so a repeat means a reused screenshot.
// It needs most rows read to be meaningful.
func screenshotStatsFingerprint(reading screenshotReading) *string {
	// A short or abandoned match can have near-empty stats that two unrelated
	// matches share, so only a table with real play identifies a screen.
	passes, ok := reading.Stats["passes"]
	if reading.Screen != "match_result" || len(reading.Stats) < 8 || !ok || passes[0] < 20 || passes[1] < 20 {
		return nil
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "%d-%d", reading.LeftScore, reading.RightScore)
	for _, key := range screenshotStatKeys {
		if value, ok := reading.Stats[key]; ok {
			fmt.Fprintf(&builder, "|%s:%d,%d", key, value[0], value[1])
		}
	}
	sum := sha256.Sum256([]byte(builder.String()))
	fingerprint := hex.EncodeToString(sum[:])
	return &fingerprint
}

// teamNameKey normalises a team name for comparison: letters and digits in
// any script, lower case, without spaces or punctuation.
func teamNameKey(name string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
		}
	}
	return truncateRunes(builder.String(), 80)
}

// teamNamesMatch tolerates one OCR slip in names of six or more characters
// and nothing else: no substrings, no extra words. Looser matching lets a
// player choose a team name that passes for someone else's.
func teamNamesMatch(read, known string) bool {
	a, b := []rune(teamNameKey(read)), []rune(teamNameKey(known))
	shorter := min(len(a), len(b))
	// Two Latin letters ("FC") say nothing about a team; two characters of a
	// Japanese or Thai name can.
	minimum := 3
	if !isASCII(a) || !isASCII(b) {
		minimum = 2
	}
	if shorter < minimum {
		return false
	}
	if string(a) == string(b) {
		return true
	}
	return shorter >= 6 && abs(len(a)-len(b)) <= 1 && levenshtein(a, b) <= 1
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func isASCII(runes []rune) bool {
	for _, r := range runes {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func levenshtein(a, b []rune) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min(previous[j]+1, current[j-1]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

// Orientation says how a screenshot's left and right map to the match.
const (
	orientationHomeLeft  = "home_left"
	orientationHomeRight = "home_right"
	orientationEither    = "either" // a draw without penalties reads the same both ways
	orientationUnknown   = "unknown"
)

// screenshotOrientation maps a screenshot's left and right to the match from
// the players' learned team names (see player_team_names). Both banner names
// must map, one to each player, and neither to both; one matching name is
// never enough, because a player controls their own team name.
func screenshotOrientation(reading screenshotReading, homeNames, awayNames []string) string {
	matches := func(name string, known []string) bool {
		return slices.ContainsFunc(known, func(candidate string) bool { return teamNamesMatch(name, candidate) })
	}
	leftHome, leftAway := matches(reading.LeftTeam, homeNames), matches(reading.LeftTeam, awayNames)
	rightHome, rightAway := matches(reading.RightTeam, homeNames), matches(reading.RightTeam, awayNames)
	switch {
	case leftHome && !leftAway && rightAway && !rightHome:
		return orientationHomeLeft
	case leftAway && !leftHome && rightHome && !rightAway:
		return orientationHomeRight
	case reading.LeftScore == reading.RightScore && reading.LeftPenalties == nil:
		return orientationEither
	default:
		return orientationUnknown
	}
}

// screenshotScore is a result in match orientation.
type screenshotScore struct {
	HomeScore     int  `json:"homeScore"`
	AwayScore     int  `json:"awayScore"`
	HomePenalties *int `json:"homePenalties"`
	AwayPenalties *int `json:"awayPenalties"`
}

func (reading screenshotReading) inMatchOrientation(orientation string) (screenshotScore, bool) {
	switch orientation {
	case orientationHomeLeft, orientationEither:
		return screenshotScore{HomeScore: reading.LeftScore, AwayScore: reading.RightScore,
			HomePenalties: reading.LeftPenalties, AwayPenalties: reading.RightPenalties}, true
	case orientationHomeRight:
		return screenshotScore{HomeScore: reading.RightScore, AwayScore: reading.LeftScore,
			HomePenalties: reading.RightPenalties, AwayPenalties: reading.LeftPenalties}, true
	}
	return screenshotScore{}, false
}

func (score screenshotScore) equals(other screenshotScore) bool {
	return score.HomeScore == other.HomeScore && score.AwayScore == other.AwayScore &&
		equalOptionalInt(score.HomePenalties, other.HomePenalties) &&
		equalOptionalInt(score.AwayPenalties, other.AwayPenalties)
}

func equalOptionalInt(left, right *int) bool {
	return (left == nil) == (right == nil) && (left == nil || *left == *right)
}

// claimScore is a blind claim as the evaluation compares it.
type claimScore struct {
	Score screenshotScore
	// Series claims (best of three and up) report game wins, which no single
	// screenshot shows.
	Series bool
}

// evaluatedReading is one stored reading as staff and the auto-decider see it.
type evaluatedReading struct {
	EvidenceID     string
	UploadedBySide string // home | away
	Status         string
	Reading        *screenshotReading
	ReusedMatchID  *string
	ReuseKind      *string
	LastError      *string
	Orientation    string
	Score          *screenshotScore
	Flags          []string
}

// screenshotEvaluation is the reader's verdict on a review.
type screenshotEvaluation struct {
	Verdict     string           `json:"verdict"`
	Score       *screenshotScore `json:"score"`
	Differences []string         `json:"differences"`
	Reasons     []string         `json:"reasons"`
	// SupportedSide is home or away when the screenshots match exactly one
	// claim, whether or not the reader may settle it.
	SupportedSide string `json:"supportedSide,omitempty"`
	// Decision is accept_home or accept_away when the reader may decide.
	Decision string `json:"decision,omitempty"`
	// AutoDecide says the reader will settle this review itself: automatic
	// decisions are on, the review is open, and Decision is set.
	AutoDecide bool `json:"autoDecide"`
}

// screenshotHints are flags shown to staff that never block a decision.
var screenshotHints = []string{"similar_image"}

func hasBlockingFlag(flags []string) bool {
	return slices.ContainsFunc(flags, func(flag string) bool { return !slices.Contains(screenshotHints, flag) })
}

const (
	verdictPending    = "pending"     // readings still queued
	verdictSupports   = "supports"    // screenshots agree and match exactly one claim
	verdictNeither    = "neither"     // screenshots agree but match no claim
	verdictConflict   = "conflicting" // screenshots disagree with each other
	verdictUnreadable = "unreadable"  // nothing usable was read
	verdictUnclear    = "inconclusive"
)

// screenshotEvaluationInput is everything evaluateScreenshots weighs.
type screenshotEvaluationInput struct {
	Readings []*evaluatedReading
	// HomeNames and AwayNames are the players' learned team names: the only
	// names that decide which side of a screenshot is whose.
	HomeNames, AwayNames []string
	HomeClaim, AwayClaim *claimScore
	// BestOf is the stage's best-of; a series total is never on one screen.
	BestOf        int
	MinConfidence float64
}

// evaluateScreenshots decides what the readings of one review say. It is pure
// so the staff view and the auto-decider can never disagree.
func evaluateScreenshots(in screenshotEvaluationInput) screenshotEvaluation {
	evaluation := screenshotEvaluation{Differences: []string{}, Reasons: []string{}}
	reason := func(text string) { evaluation.Reasons = append(evaluation.Reasons, text) }
	usable := map[string][]*evaluatedReading{}
	var all []*evaluatedReading
	pending, blocked := false, false
	for _, item := range in.Readings {
		item.Flags = []string{}
		item.Orientation = orientationUnknown
		item.Score = nil
		switch {
		case item.Status == "queued":
			pending = true
			continue
		case item.Status != "read" || item.Reading == nil:
			item.Flags = append(item.Flags, "not_read")
			blocked = true
			continue
		case item.Reading.Screen != "match_result":
			item.Flags = append(item.Flags, "not_result_screen")
			blocked = true
			continue
		}
		reading := *item.Reading
		item.Flags = append(item.Flags, screenshotPlausibility(reading)...)
		if reading.Confidence < in.MinConfidence {
			item.Flags = append(item.Flags, "low_confidence")
		}
		switch {
		case item.ReuseKind != nil && *item.ReuseKind == "stats":
			item.Flags = append(item.Flags, "reused_stats")
		case item.ReuseKind != nil:
			// A perceptual hash summarises the fixed layout more than the
			// digits, so a matching image is a hint for staff, not proof.
			item.Flags = append(item.Flags, "similar_image")
		}
		if _, ok := reading.Stats["shotsOnTarget"]; !ok {
			item.Flags = append(item.Flags, "stats_incomplete")
		} else if _, ok := reading.Stats["saves"]; !ok {
			item.Flags = append(item.Flags, "stats_incomplete")
		}
		switch {
		case reading.FullTime == nil:
			item.Flags = append(item.Flags, "full_time_unknown")
		case !*reading.FullTime:
			item.Flags = append(item.Flags, "not_full_time")
		}
		item.Orientation = screenshotOrientation(reading, in.HomeNames, in.AwayNames)
		if score, ok := reading.inMatchOrientation(item.Orientation); ok {
			item.Score = &score
		} else {
			item.Flags = append(item.Flags, "teams_unknown")
		}
		all = append(all, item)
		if !hasBlockingFlag(item.Flags) {
			usable[item.UploadedBySide] = append(usable[item.UploadedBySide], item)
		} else {
			blocked = true
		}
	}
	// Every pair of read result screens must agree on everything read.
	for index := 1; index < len(all); index++ {
		for _, difference := range screenshotDifferences(*all[0], *all[index]) {
			if !slices.Contains(evaluation.Differences, difference) {
				evaluation.Differences = append(evaluation.Differences, difference)
			}
		}
	}
	// And they must say the same thing about the match: one home/away score.
	var agreed *screenshotScore
	for _, item := range all {
		if item.Score == nil {
			continue
		}
		if agreed == nil {
			agreed = item.Score
		} else if !agreed.equals(*item.Score) && !slices.Contains(evaluation.Differences, "orientation") {
			evaluation.Differences = append(evaluation.Differences, "orientation")
		}
	}
	sort.Strings(evaluation.Differences)
	switch {
	case pending:
		evaluation.Verdict = verdictPending
		return evaluation
	case len(all) == 0:
		evaluation.Verdict = verdictUnreadable
		return evaluation
	case len(evaluation.Differences) > 0:
		evaluation.Verdict = verdictConflict
		reason("The screenshots disagree.")
		return evaluation
	case agreed == nil:
		evaluation.Verdict = verdictUnclear
		reason("The players' team names aren't known yet, so the screenshot's sides can't be matched to them.")
		return evaluation
	}
	evaluation.Score = agreed
	homeMatches := in.HomeClaim != nil && in.HomeClaim.Score.equals(*agreed)
	awayMatches := in.AwayClaim != nil && in.AwayClaim.Score.equals(*agreed)
	switch {
	case !homeMatches && !awayMatches:
		evaluation.Verdict = verdictNeither
		return evaluation
	case homeMatches && awayMatches:
		evaluation.Verdict = verdictUnclear
		return evaluation
	}
	evaluation.Verdict = verdictSupports
	evaluation.SupportedSide = "away"
	if homeMatches {
		evaluation.SupportedSide = "home"
	}
	// The reader may only decide when nothing is in doubt and each player's
	// own screenshot says the same.
	series := in.BestOf > 1 || (in.HomeClaim != nil && in.HomeClaim.Series) || (in.AwayClaim != nil && in.AwayClaim.Series)
	switch {
	case series:
		reason("A series result isn't shown on one screenshot.")
	case agreed.HomePenalties != nil:
		// The shoot-out display isn't confirmed on real screenshots yet.
		reason("Penalty shoot-outs are always checked by staff.")
	case blocked:
		reason("A screenshot was unreadable, flagged or below the confidence threshold.")
	case len(usable["home"]) == 0 || len(usable["away"]) == 0:
		reason("Each player must provide a readable screenshot.")
	default:
		evaluation.Decision = "accept_" + evaluation.SupportedSide
	}
	return evaluation
}

// screenshotDifferences names every field two readings of the same match
// disagree on. It compares them as read and mirrored, and keeps the closer
// fit: if the game ever shows each player their own team on the left, two
// genuine screenshots are mirror images of each other. Whichever way they
// line up, both must then map to the same home/away score (see
// evaluateScreenshots), so mirroring never hides a disagreement.
func screenshotDifferences(first, second evaluatedReading) []string {
	asRead := screenshotFieldDifferences(*first.Reading, *second.Reading)
	if len(asRead) == 0 {
		return asRead
	}
	if mirrored := screenshotFieldDifferences(*first.Reading, mirrorReading(*second.Reading)); len(mirrored) < len(asRead) {
		return mirrored
	}
	return asRead
}

func mirrorReading(reading screenshotReading) screenshotReading {
	mirrored := reading
	mirrored.LeftTeam, mirrored.RightTeam = reading.RightTeam, reading.LeftTeam
	mirrored.LeftScore, mirrored.RightScore = reading.RightScore, reading.LeftScore
	mirrored.LeftPenalties, mirrored.RightPenalties = reading.RightPenalties, reading.LeftPenalties
	mirrored.Stats = make(map[string][2]int, len(reading.Stats))
	for key, value := range reading.Stats {
		mirrored.Stats[key] = [2]int{value[1], value[0]}
	}
	return mirrored
}

func screenshotFieldDifferences(a, b screenshotReading) []string {
	differences := []string{}
	sameTeam := func(left, right string) bool {
		return teamNameKey(left) == teamNameKey(right) || teamNamesMatch(left, right)
	}
	if !sameTeam(a.LeftTeam, b.LeftTeam) || !sameTeam(a.RightTeam, b.RightTeam) {
		differences = append(differences, "teams")
	}
	if a.LeftScore != b.LeftScore || a.RightScore != b.RightScore {
		differences = append(differences, "score")
	}
	if !equalOptionalInt(a.LeftPenalties, b.LeftPenalties) || !equalOptionalInt(a.RightPenalties, b.RightPenalties) {
		differences = append(differences, "penalties")
	}
	for _, key := range screenshotStatKeys {
		left, okLeft := a.Stats[key]
		right, okRight := b.Stats[key]
		if okLeft && okRight && left != right {
			differences = append(differences, key)
		}
	}
	return differences
}

// formatScreenshotScore renders a score for notes, e.g. "3-1" or "1-1 (4-3 pens)".
func formatScreenshotScore(score screenshotScore) string {
	text := strconv.Itoa(score.HomeScore) + "-" + strconv.Itoa(score.AwayScore)
	if score.HomePenalties != nil && score.AwayPenalties != nil {
		text += fmt.Sprintf(" (%d-%d pens)", *score.HomePenalties, *score.AwayPenalties)
	}
	return text
}
