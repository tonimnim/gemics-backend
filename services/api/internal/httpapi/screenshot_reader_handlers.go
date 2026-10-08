package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const visionTrainingCursorKind = "vision_training"

func (s *Server) registerScreenshotReaderRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/admin/screenshot-readings/{evidenceId}/retry",
		s.platformRoute(platformResultReviewManage, s.retryScreenshotReading))
	mux.HandleFunc("GET /v1/internal/vision/training-examples", s.listVisionTrainingExamples)
}

// staffScreenshotReadingView is what the reader made of one screenshot, as a
// reviewer sees it beside the image.
type staffScreenshotReadingView struct {
	Status        string               `json:"status"`
	Screen        *string              `json:"screen"`
	Confidence    *float64             `json:"confidence"`
	Left          *screenshotSideView  `json:"left"`
	Right         *screenshotSideView  `json:"right"`
	Penalties     *screenshotPenalties `json:"penalties"`
	Stats         map[string][2]int    `json:"stats"`
	Orientation   string               `json:"orientation"`
	Score         *screenshotScore     `json:"score"`
	Flags         []string             `json:"flags"`
	ReusedMatchID *string              `json:"reusedMatchId"`
	Error         *string              `json:"error"`
	Model         *string              `json:"model"`
}

type screenshotSideView struct {
	Team  string `json:"team"`
	Score int    `json:"score"`
}

// attachScreenshotReadings adds each screenshot's reading and the overall
// screenshot verdict to a review detail. It changes nothing when no
// screenshot was queued for reading. With reading turned off, screenshots
// still waiting are left out rather than shown as forever "reading".
func (s *Server) attachScreenshotReadings(ctx context.Context, queryer rowsQueryer, detail *resultReviewDetail) error {
	screenshots, err := loadScreenshotContext(ctx, queryer, detail.Match.ID)
	if err != nil {
		return err
	}
	if _, _, enabled := s.screenshotReaderDeps(); !enabled {
		kept := screenshots.Readings[:0]
		for _, item := range screenshots.Readings {
			if item.Status != "queued" {
				kept = append(kept, item)
			}
		}
		screenshots.Readings = kept
	}
	if len(screenshots.Readings) == 0 {
		return nil
	}
	evaluation := evaluateScreenshots(screenshots.evaluationInput(s.config.VisionAutoMinConfidence))
	evaluation.AutoDecide = s.config.VisionAutoDecide && evaluation.Decision != "" &&
		detail.Status == "queued" && detail.Reason == "reports_differ"
	views := map[string]*staffScreenshotReadingView{}
	for _, item := range screenshots.Readings {
		view := &staffScreenshotReadingView{Status: item.Status, Orientation: item.Orientation, Score: item.Score,
			Flags: item.Flags, ReusedMatchID: item.ReusedMatchID, Error: item.LastError, Stats: map[string][2]int{}}
		if reading := item.Reading; reading != nil {
			view.Screen, view.Confidence = &reading.Screen, &reading.Confidence
			model := strings.TrimSpace(reading.Engine + " " + reading.ModelVersion)
			view.Model = &model
			if reading.Screen == "match_result" {
				view.Left = &screenshotSideView{Team: reading.LeftTeam, Score: reading.LeftScore}
				view.Right = &screenshotSideView{Team: reading.RightTeam, Score: reading.RightScore}
				view.Stats = reading.Stats
				if reading.LeftPenalties != nil && reading.RightPenalties != nil {
					view.Penalties = &screenshotPenalties{Left: *reading.LeftPenalties, Right: *reading.RightPenalties}
				}
			}
		}
		views[item.EvidenceID] = view
	}
	for _, report := range []*staffReportView{detail.Reports.Home.Initial, detail.Reports.Home.Final,
		detail.Reports.Away.Initial, detail.Reports.Away.Final} {
		if report == nil {
			continue
		}
		for index := range report.Evidence {
			report.Evidence[index].Reading = views[report.Evidence[index].ID]
		}
	}
	detail.ScreenshotCheck = &evaluation
	return nil
}

// retryScreenshotReading reads a screenshot again, for example after the
// reader's model improved. It is refused once the review is decided, so a
// decision never loses the readings it was made from, and to a reviewer who
// plays in or captains the match (D27). Every retry is audited.
func (s *Server) retryScreenshotReading(w http.ResponseWriter, r *http.Request) {
	evidenceID := strings.ToLower(strings.TrimSpace(r.PathValue("evidenceId")))
	if !uuidPattern.MatchString(evidenceID) {
		writeError(w, http.StatusNotFound, "screenshot_reading_not_found", "No reading for that screenshot.")
		return
	}
	ctx := r.Context()
	actorID := identityFromContext(ctx).UserID
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the screenshot.")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var conflicted, decided bool
	var previousModel *string
	err = tx.QueryRow(ctx, `SELECT `+resultReviewConflictClause("m", "$2::uuid")+`,
		EXISTS (SELECT 1 FROM match_result_reviews review WHERE review.match_id=m.id AND review.status<>'queued')
		  AND NOT EXISTS (SELECT 1 FROM match_result_reviews review WHERE review.match_id=m.id AND review.status='queued'),
		reading.model_version
		FROM screenshot_readings reading JOIN matches m ON m.id=reading.match_id
		WHERE reading.evidence_id=$1 FOR UPDATE OF reading`, evidenceID, actorID).
		Scan(&conflicted, &decided, &previousModel)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && conflicted) {
		writeError(w, http.StatusNotFound, "screenshot_reading_not_found", "No reading for that screenshot.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the screenshot.")
		return
	}
	if decided {
		writeError(w, http.StatusConflict, "review_decided", "This dispute is decided; its readings are kept as they were.")
		return
	}
	// A new claim token makes any read already in flight discard its result.
	if _, err = tx.Exec(ctx, `UPDATE screenshot_readings SET status='queued',attempts=0,outage_retries=0,
		claim_token=NULL,available_at=now(),last_error=NULL,screen=NULL,read_at=NULL,evaluated_at=NULL,updated_at=now()
		WHERE evidence_id=$1`, evidenceID); err == nil {
		err = appendPlatformAuditContext(ctx, tx, r.Header.Get("X-Request-ID"), &actorID, "screenshot_reading.retried",
			"screenshot_reading", evidenceID, map[string]any{"modelVersion": previousModel}, map[string]any{"status": "queued"})
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to queue the screenshot.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"data": map[string]string{"evidenceId": evidenceID,
		"status": "queued"}})
}

// visionTrainingExample is a real screenshot labelled in screen terms: what
// is printed on the left and the right, from a result staff confirmed.
type visionTrainingExample struct {
	EvidenceID        string               `json:"evidenceId"`
	MatchID           string               `json:"matchId"`
	DownloadURL       string               `json:"downloadUrl"`
	DownloadExpiresAt time.Time            `json:"downloadExpiresAt"`
	ContentType       string               `json:"contentType"`
	Label             visionTrainingLabel  `json:"label"`
	Source            string               `json:"source"`
	DecidedAt         time.Time            `json:"decidedAt"`
	Reader            visionTrainingReader `json:"reader"`
	ReaderCorrect     bool                 `json:"readerCorrect"`
}

type visionTrainingLabel struct {
	Screen    string               `json:"screen"`
	Left      screenshotSideView   `json:"left"`
	Right     screenshotSideView   `json:"right"`
	Penalties *screenshotPenalties `json:"penalties"`
}

type visionTrainingReader struct {
	Left       screenshotSideView   `json:"left"`
	Right      screenshotSideView   `json:"right"`
	Penalties  *screenshotPenalties `json:"penalties"`
	Stats      map[string][2]int    `json:"stats"`
	Confidence float64              `json:"confidence"`
	Model      string               `json:"model"`
}

// listVisionTrainingExamples gives the reader's team real labelled
// screenshots. A screenshot is exported only when its label can be trusted:
// the match was best of one and has a confirmed result, both players'
// screenshots agree with each other, and none of them carries a flag. A forged
// screenshot therefore never becomes a training label. Authenticated with
// VISION_TRAINING_TOKEN (never sent to the reader), optionally limited to
// VISION_TRAINING_CIDRS; it is not part of the public API.
func (s *Server) listVisionTrainingExamples(w http.ResponseWriter, r *http.Request) {
	if s.config.VisionTrainingToken == "" || s.db == nil || s.evidenceStore == nil {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
		return
	}
	if len(s.config.VisionTrainingCIDRs) > 0 {
		address, ok := parseRequestIP(s.clientIP(r))
		allowed := false
		for _, cidr := range s.config.VisionTrainingCIDRs {
			if prefix, err := netip.ParsePrefix(cidr); err == nil && ok && prefix.Contains(address) {
				allowed = true
			}
		}
		if !allowed {
			writeError(w, http.StatusNotFound, "not_found", "Not found.")
			return
		}
	}
	token, hasBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !hasBearer || subtle.ConstantTimeCompare([]byte(token), []byte(s.config.VisionTrainingToken)) != 1 {
		writeError(w, http.StatusUnauthorized, "invalid_token", "A valid training token is required.")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 100.")
			return
		}
		limit = parsed
	}
	var afterTime *time.Time
	var afterID *string
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor, err := decodePublicCursor(raw, visionTrainingCursorKind, s.config.AccessTokenSecret, time.Now())
		if err != nil || !uuidPattern.MatchString(cursor.ID) {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The cursor is invalid or expired.")
			return
		}
		value := cursorTime(cursor.SortTime)
		afterTime, afterID = &value, &cursor.ID
	}
	ctx := r.Context()
	// Staff decisions only: an automatic decision was made from the reader's
	// own output, so training on it would teach the reader its own mistakes.
	rows, err := s.db.Writer.Query(ctx, `SELECT reading.evidence_id::text,reading.match_id::text,evidence.object_key,
		evidence.media_type,reading.read_at,result.home_score,result.away_score,result.home_tiebreak_score,
		result.away_tiebreak_score,review.decided_at
		FROM screenshot_readings reading
		JOIN evidence_uploads evidence ON evidence.id=reading.evidence_id
		JOIN match_result_reviews review ON review.match_id=reading.match_id AND review.status='decided'
		  AND review.decider_kind='staff'
		JOIN LATERAL (SELECT home_score,away_score,home_tiebreak_score,away_tiebreak_score
			FROM result_submissions WHERE match_id=reading.match_id AND status='confirmed'
			ORDER BY submitted_at DESC,id DESC LIMIT 1) result ON true
		WHERE reading.status='read' AND reading.screen='match_result'
		  AND ($1::timestamptz IS NULL OR (reading.read_at,reading.evidence_id)>($1,$2::uuid))
		ORDER BY reading.read_at,reading.evidence_id LIMIT $3`, afterTime, afterID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load training examples.")
		return
	}
	type candidate struct {
		EvidenceID, MatchID, ObjectKey, MediaType string
		ReadAt                                    time.Time
		Result                                    screenshotScore
		DecidedAt                                 *time.Time
	}
	candidates, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (candidate, error) {
		var item candidate
		err := row.Scan(&item.EvidenceID, &item.MatchID, &item.ObjectKey, &item.MediaType, &item.ReadAt,
			&item.Result.HomeScore, &item.Result.AwayScore, &item.Result.HomePenalties, &item.Result.AwayPenalties,
			&item.DecidedAt)
		return item, err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load training examples.")
		return
	}
	var nextCursor *string
	if len(candidates) > limit {
		candidates = candidates[:limit]
		last := candidates[len(candidates)-1]
		next, encodeErr := encodePublicCursor(publicCursor{Kind: visionTrainingCursorKind,
			ExpiresAt: time.Now().Add(24 * time.Hour).Unix(), SortTime: last.ReadAt.UnixNano(), ID: last.EvidenceID},
			s.config.AccessTokenSecret)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate training examples.")
			return
		}
		nextCursor = &next
	}
	contexts := map[string]screenshotContext{}
	trusted := map[string]bool{}
	examples := make([]visionTrainingExample, 0, len(candidates))
	for _, item := range candidates {
		screenshots, ok := contexts[item.MatchID]
		if !ok {
			if screenshots, err = loadScreenshotContext(ctx, s.db.Writer, item.MatchID); err != nil {
				writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load training examples.")
				return
			}
			contexts[item.MatchID] = screenshots
			trusted[item.MatchID] = trustedForTraining(screenshots)
		}
		if !trusted[item.MatchID] {
			continue
		}
		example, ok := visionTrainingExampleFor(screenshots, item.EvidenceID, item.Result)
		if !ok {
			continue
		}
		intent, presignErr := s.evidenceStore.PresignGet(ctx, item.ObjectKey, 15*time.Minute)
		if presignErr != nil {
			writeError(w, http.StatusServiceUnavailable, "storage_unavailable", "Unable to sign screenshot downloads.")
			return
		}
		example.MatchID, example.ContentType, example.Source = item.MatchID, item.MediaType, "staff_decision"
		example.DownloadURL, example.DownloadExpiresAt = intent.URL, intent.ExpiresAt.UTC()
		example.DecidedAt = valueOr(item.DecidedAt, item.ReadAt).UTC()
		examples = append(examples, example)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": examples, "nextCursor": nextCursor})
}

// trustedForTraining says a match's screenshots can carry the decided result
// as their label: best of one, at least one result screen from each player,
// all agreeing on every field, and none flagged (team names may be unknown;
// the score's fit to the result orients them).
func trustedForTraining(screenshots screenshotContext) bool {
	if screenshots.BestOf > 1 {
		return false
	}
	evaluation := evaluateScreenshots(screenshots.evaluationInput(screenshotLearnMinConfidence))
	if evaluation.Verdict == verdictPending || len(evaluation.Differences) > 0 {
		return false
	}
	sides := map[string]bool{}
	for _, item := range screenshots.Readings {
		for _, flag := range item.Flags {
			if flag != "teams_unknown" && !slices.Contains(screenshotHints, flag) {
				return false
			}
		}
		if item.Reading != nil && item.Reading.Screen == "match_result" {
			sides[item.UploadedBySide] = true
		}
	}
	return sides["home"] && sides["away"]
}

// visionTrainingExampleFor labels one reading with the decided result,
// oriented by the team names or, failing that, by the one way round the
// reader's score fits the result.
func visionTrainingExampleFor(screenshots screenshotContext, evidenceID string, result screenshotScore) (
	visionTrainingExample, bool) {
	for _, item := range screenshots.Readings {
		if item.EvidenceID != evidenceID || item.Reading == nil {
			continue
		}
		reading := *item.Reading
		orientation := screenshotOrientation(reading, screenshots.HomeNames, screenshots.AwayNames)
		if orientation == orientationUnknown || orientation == orientationEither {
			homeLeft, _ := reading.inMatchOrientation(orientationHomeLeft)
			homeRight, _ := reading.inMatchOrientation(orientationHomeRight)
			switch {
			case homeLeft.equals(result) && !homeRight.equals(result):
				orientation = orientationHomeLeft
			case homeRight.equals(result) && !homeLeft.equals(result):
				orientation = orientationHomeRight
			case result.HomeScore == result.AwayScore && result.HomePenalties == nil:
				orientation = orientationHomeLeft // a draw labels the same either way
			default:
				return visionTrainingExample{}, false
			}
		}
		// Team names come from the screen: the two players' screenshots agreed
		// on them (trustedForTraining), so they are what is printed there.
		leftScore, rightScore := result.HomeScore, result.AwayScore
		leftPens, rightPens := result.HomePenalties, result.AwayPenalties
		if orientation == orientationHomeRight {
			leftScore, rightScore, leftPens, rightPens = result.AwayScore, result.HomeScore, result.AwayPenalties,
				result.HomePenalties
		}
		label := visionTrainingLabel{Screen: "match_result",
			Left:  screenshotSideView{Team: reading.LeftTeam, Score: leftScore},
			Right: screenshotSideView{Team: reading.RightTeam, Score: rightScore}}
		if leftPens != nil && rightPens != nil {
			label.Penalties = &screenshotPenalties{Left: *leftPens, Right: *rightPens}
		}
		reader := visionTrainingReader{Left: screenshotSideView{Team: reading.LeftTeam, Score: reading.LeftScore},
			Right: screenshotSideView{Team: reading.RightTeam, Score: reading.RightScore}, Stats: reading.Stats,
			Confidence: reading.Confidence, Model: strings.TrimSpace(reading.Engine + " " + reading.ModelVersion)}
		if reading.LeftPenalties != nil && reading.RightPenalties != nil {
			reader.Penalties = &screenshotPenalties{Left: *reading.LeftPenalties, Right: *reading.RightPenalties}
		}
		correct := reading.LeftScore == leftScore && reading.RightScore == rightScore &&
			equalOptionalInt(reading.LeftPenalties, leftPens) && equalOptionalInt(reading.RightPenalties, rightPens)
		return visionTrainingExample{EvidenceID: evidenceID, Label: label, Reader: reader, ReaderCorrect: correct}, true
	}
	return visionTrainingExample{}, false
}
