package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// screenshotSample builds a reader answer like the real samples.
func screenshotSample(leftTeam string, leftScore int, rightTeam string, rightScore int,
	stats map[string][]int) visionReadResponse {
	full := true
	response := visionReadResponse{Screen: "match_result", FullTime: &full, Confidence: 0.97,
		Left: &screenshotSide{Team: leftTeam, Score: leftScore}, Right: &screenshotSide{Team: rightTeam, Score: rightScore},
		Stats: stats, ImageHash: "c3d1f0e0b8a4c2e1"}
	response.Model.Engine, response.Model.Version = "easyocr", "1.7.2+test"
	return response
}

// The three samples the team shared, as the reader would return them.
var (
	sampleArgentina = map[string][]int{"possession": {57, 43}, "shots": {5, 7}, "shotsOnTarget": {5, 6},
		"fouls": {1, 1}, "offsides": {0, 1}, "cornerKicks": {0, 0}, "freeKicks": {1, 1}, "passes": {96, 68},
		"successfulPasses": {83, 50}, "crosses": {0, 0}, "interceptions": {16, 12}, "tackles": {3, 11}, "saves": {3, 2}}
	sampleMugz = map[string][]int{"possession": {64, 36}, "shots": {16, 1}, "shotsOnTarget": {12, 1},
		"fouls": {0, 0}, "offsides": {0, 0}, "cornerKicks": {3, 0}, "freeKicks": {0, 0}, "passes": {156, 96},
		"successfulPasses": {122, 66}, "crosses": {4, 0}, "interceptions": {28, 24}, "tackles": {5, 5}, "saves": {0, 7}}
	sampleMzee = map[string][]int{"possession": {50, 50}, "shots": {4, 9}, "shotsOnTarget": {3, 6},
		"fouls": {1, 3}, "offsides": {1, 3}, "cornerKicks": {0, 3}, "freeKicks": {3, 1}, "passes": {90, 83},
		"successfulPasses": {60, 57}, "crosses": {1, 0}, "interceptions": {16, 26}, "tackles": {4, 4}, "saves": {3, 0}}
)

func mustValidate(t *testing.T, response visionReadResponse) screenshotReading {
	t.Helper()
	reading, err := validateVisionResponse(response)
	if err != nil {
		t.Fatal(err)
	}
	return reading
}

func TestScreenshotPlausibilityAcceptsRealScreensAndCatchesEditedScores(t *testing.T) {
	for name, sample := range map[string]visionReadResponse{
		"argentina": screenshotSample("Argentina", 3, "shinegum", 3, sampleArgentina),
		"mugz":      screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz),
		"mzee":      screenshotSample("mzee mzima", 3, "CR Flamengo", 0, sampleMzee),
	} {
		if failed := screenshotPlausibility(mustValidate(t, sample)); len(failed) != 0 {
			t.Errorf("%s: a real screen failed %v", name, failed)
		}
	}
	// Flipping Mugz FC 2-1 to 1-3: SQUAD 0 had one shot on target.
	edited := mustValidate(t, screenshotSample("Mugz FC", 1, "SQUAD 0", 3, sampleMugz))
	if failed := screenshotPlausibility(edited); !slices.Contains(failed, "goals_exceed_shots_on_target") {
		t.Fatalf("edited score passed: %v", failed)
	}
	// A genuine console screen (FC Barcelona 4-1, the stadium scoreboard behind
	// agrees) has 4 goals + 7 saves against 10 on target, and 1 + 3 against 3:
	// eFootball doesn't count saves that way, so the rule is only a hint.
	barcelona := mustValidate(t, screenshotSample("FC Barcelona", 4, "Emirhann", 1, map[string][]int{
		"shotsOnTarget": {10, 3}, "saves": {3, 7}}))
	failed := screenshotPlausibility(barcelona)
	if !slices.Equal(failed, []string{"goals_and_saves_exceed_shots_on_target"}) || hasBlockingFlag(failed) {
		t.Fatalf("a genuine screen was blocked: %v", failed)
	}
}

func TestScreenshotGeometryRefusesShapesNoScreenCanHave(t *testing.T) {
	encode := func(width, height int) []byte {
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, image.NewGray(image.Rect(0, 0, width, height))); err != nil {
			t.Fatal(err)
		}
		return buffer.Bytes()
	}
	for _, size := range [][2]int{{1600, 720}, {2400, 1080}, {720, 1600}, {1024, 768}, {960, 240}} {
		if problem := screenshotGeometryProblem(encode(size[0], size[1])); problem != "" {
			t.Errorf("%v refused: %s", size, problem)
		}
	}
	// A thin strip would be scaled into a frame hundreds of thousands of
	// pixels wide; a tiny image or a long banner crop isn't the screen either.
	for _, size := range [][2]int{{8192, 10}, {10, 8192}, {200, 120}, {2000, 400}} {
		if problem := screenshotGeometryProblem(encode(size[0], size[1])); problem == "" {
			t.Errorf("%v accepted", size)
		}
	}
	if problem := screenshotGeometryProblem([]byte("not an image")); problem != "" {
		t.Errorf("undecodable bytes refused here instead of by the reader: %s", problem)
	}
}

func TestValidateVisionResponseKeepsOnlyWellFormedFields(t *testing.T) {
	sample := screenshotSample("  Mugz   FC ", 2, "SQUAD 0", 1, map[string][]int{
		"possession": {64, 36}, "shots": {16}, "passes": {156, -1}, "goals": {2, 1}, "saves": {0, 7}})
	sample.ImageHash = "C3D1F0E0B8A4C2E1"
	reading := mustValidate(t, sample)
	if reading.LeftTeam != "Mugz FC" || reading.ImageHash == nil || *reading.ImageHash != "c3d1f0e0b8a4c2e1" {
		t.Fatalf("reading = %+v", reading)
	}
	if len(reading.Stats) != 2 || reading.Stats["saves"] != [2]int{0, 7} {
		t.Fatalf("malformed and unknown rows were kept: %v", reading.Stats)
	}
	penalties := json.RawMessage(`{"left":4,"right":3}`)
	sample.Penalties = &penalties
	if reading := mustValidate(t, sample); reading.LeftPenalties == nil || *reading.RightPenalties != 3 {
		t.Fatalf("penalties = %v %v", reading.LeftPenalties, reading.RightPenalties)
	}
	unknown := visionReadResponse{Screen: "unknown", Confidence: 0.4}
	if reading := mustValidate(t, unknown); reading.Screen != "unknown" || reading.Engine != "unknown" {
		t.Fatalf("unknown screen = %+v", reading)
	}
	for name, mutate := range map[string]func(*visionReadResponse){
		"confidence above 1": func(r *visionReadResponse) { r.Confidence = 1.5 },
		"score above 99":     func(r *visionReadResponse) { r.Left.Score = 100 },
		"missing side":       func(r *visionReadResponse) { r.Right = nil },
		"strange screen":     func(r *visionReadResponse) { r.Screen = "lobby" },
	} {
		broken := screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz)
		mutate(&broken)
		if _, err := validateVisionResponse(broken); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTeamNamesMatchToleratesOneOCRSlipOnly(t *testing.T) {
	for _, pair := range [][2]string{
		{"mzee mzima", "mzeemzima"}, {"Mugz FC", "MUGZ FC"}, {"shinegum", "shlnegum"},
		{"東京ユナイテッド", "東京ユナイテッド"}, {"SQUAD 0", "squad0"}, {"東京", "東京"},
	} {
		if !teamNamesMatch(pair[0], pair[1]) {
			t.Errorf("%q and %q should match", pair[0], pair[1])
		}
	}
	// No substrings, extra words or short-name slips: a player could pick a
	// team name that passes for someone else's.
	for _, pair := range [][2]string{
		{"Argentina", "shinegum"}, {"Mugz FC", "SQUAD 0"}, {"FC", "FC"}, {"Barca", "Bayer"}, {"", "x"},
		{"CR Flamengo", "Flamengo"}, {"Mugz FC Real Madrid", "Mugz FC"}, {"Chelsea", "SQUAD 0 Barcelona Chelsea"},
		{"shinegum", "shlnegam"},
	} {
		if teamNamesMatch(pair[0], pair[1]) {
			t.Errorf("%q and %q should not match", pair[0], pair[1])
		}
	}
}

func TestScreenshotOrientationNeedsBothLearnedNames(t *testing.T) {
	reading := mustValidate(t, screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz))
	if got := screenshotOrientation(reading, []string{"Mugz FC"}, []string{"SQUAD 0"}); got != orientationHomeLeft {
		t.Errorf("home on the left = %s", got)
	}
	if got := screenshotOrientation(reading, []string{"SQUAD 0"}, []string{"Mugz FC"}); got != orientationHomeRight {
		t.Errorf("home on the right = %s", got)
	}
	// One known name is never enough: the other player controls theirs.
	for name, names := range map[string][2][]string{
		"only home known":  {{"Mugz FC"}, nil},
		"only away known":  {nil, {"SQUAD 0"}},
		"both on one side": {{"Mugz FC", "SQUAD 0"}, {"kevo"}},
		"shared name":      {{"Mugz FC"}, {"Mugz FC", "SQUAD 0"}},
		"nothing known":    {nil, nil},
	} {
		if got := screenshotOrientation(reading, names[0], names[1]); got != orientationUnknown {
			t.Errorf("%s = %s", name, got)
		}
	}
	// Names one OCR slip apart match both players, so neither side is known.
	simba := mustValidate(t, screenshotSample("Simba SC", 2, "Simba SD", 1, sampleMugz))
	if got := screenshotOrientation(simba, []string{"Simba SC"}, []string{"Simba SD"}); got != orientationUnknown {
		t.Errorf("near-identical names = %s", got)
	}
	draw := mustValidate(t, screenshotSample("Argentina", 3, "shinegum", 3, sampleArgentina))
	if got := screenshotOrientation(draw, nil, nil); got != orientationEither {
		t.Errorf("draw = %s", got)
	}
}

func evaluated(side, evidenceID string, response visionReadResponse) *evaluatedReading {
	reading, err := validateVisionResponse(response)
	if err != nil {
		panic(err)
	}
	return &evaluatedReading{EvidenceID: evidenceID, UploadedBySide: side, Status: "read", Reading: &reading}
}

func claim(home, away int) *claimScore {
	return &claimScore{Score: screenshotScore{HomeScore: home, AwayScore: away}}
}

func TestEvaluateScreenshotsDecidesOnlyWhenBothPlayersAgree(t *testing.T) {
	mugz := screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz)
	evaluate := func(readings []*evaluatedReading, home, away *claimScore) screenshotEvaluation {
		return evaluateScreenshots(screenshotEvaluationInput{Readings: readings, HomeNames: []string{"Mugz FC"},
			AwayNames: []string{"SQUAD 0"}, HomeClaim: home, AwayClaim: away, BestOf: 1, MinConfidence: 0.9})
	}
	both := func(home, away visionReadResponse) []*evaluatedReading {
		return []*evaluatedReading{evaluated("home", "e1", home), evaluated("away", "e2", away)}
	}

	agree := evaluate(both(mugz, mugz), claim(2, 1), claim(1, 2))
	if agree.Verdict != verdictSupports || agree.Decision != "accept_home" || agree.SupportedSide != "home" ||
		agree.Score.HomeScore != 2 {
		t.Fatalf("agreeing screenshots = %+v", agree)
	}
	if result := evaluate(both(mugz, mugz), claim(3, 0), claim(2, 1)); result.Decision != "accept_away" {
		t.Fatalf("away's claim matches = %+v", result)
	}
	// Only one player's screenshot: supported, never decided automatically.
	one := evaluate([]*evaluatedReading{evaluated("home", "e1", mugz)}, claim(2, 1), claim(1, 2))
	if one.Verdict != verdictSupports || one.SupportedSide != "home" || one.Decision != "" {
		t.Fatalf("one-sided evidence = %+v", one)
	}
	// The forger edits the score, one stat, or adds words to the team names.
	forgedScore := screenshotSample("Mugz FC", 1, "SQUAD 0", 2, sampleMugz)
	forgedStat := screenshotSample("Mugz FC", 2, "SQUAD 0", 1, map[string][]int{"passes": {150, 96}})
	forgedNames := screenshotSample("Mugz FC Real Madrid", 2, "SQUAD 0 Barcelona Chelsea", 1, sampleMugz)
	for name, testCase := range map[string]struct {
		forged visionReadResponse
		field  string
	}{"score": {forgedScore, "score"}, "stat": {forgedStat, "passes"}, "names": {forgedNames, "teams"}} {
		result := evaluate(both(mugz, testCase.forged), claim(2, 1), claim(1, 2))
		if result.Verdict != verdictConflict || result.Decision != "" || !slices.Contains(result.Differences, testCase.field) {
			t.Errorf("forged %s = %+v", name, result)
		}
	}
	// Swapping only the names (not the numbers) lands on a different
	// home/away score, so it never decides.
	mirrored := screenshotSample("SQUAD 0", 2, "Mugz FC", 1, sampleMugz)
	if result := evaluate(both(mugz, mirrored), claim(2, 1), claim(1, 2)); result.Decision != "" ||
		result.Verdict != verdictConflict {
		t.Fatalf("mirrored names = %+v", result)
	}
	// Low confidence, reuse, an unproven Full Time and a series each block it.
	low := mugz
	low.Confidence = 0.6
	unproven := mugz
	unproven.FullTime = nil
	reused := evaluated("away", "e2", mugz)
	statsKind := "stats"
	reused.ReuseKind = &statsKind
	incomplete := screenshotSample("Mugz FC", 2, "SQUAD 0", 1, map[string][]int{"possession": {64, 36}, "saves": {0, 7}})
	for name, readings := range map[string][]*evaluatedReading{
		"low confidence":    both(mugz, low),
		"full time unknown": both(mugz, unproven),
		"reused stats":      {evaluated("home", "e1", mugz), reused},
		"stats incomplete":  both(incomplete, incomplete),
	} {
		if result := evaluate(readings, claim(2, 1), claim(1, 2)); result.Decision != "" {
			t.Errorf("%s still decided: %+v", name, result)
		}
	}
	// Saves only feed a hint, so an unread saves row doesn't block here (the
	// reader may still lower its confidence for the missing row).
	noSaves := screenshotSample("Mugz FC", 2, "SQUAD 0", 1, map[string][]int{"possession": {64, 36}, "shotsOnTarget": {12, 1}})
	if result := evaluate(both(noSaves, noSaves), claim(2, 1), claim(1, 2)); result.Decision != "accept_home" {
		t.Fatalf("unread saves blocked = %+v", result)
	}
	// A similar image is a hint for staff, never a block on its own.
	similar := evaluated("away", "e2", mugz)
	imageKind := "image"
	similar.ReuseKind = &imageKind
	if result := evaluate([]*evaluatedReading{evaluated("home", "e1", mugz), similar}, claim(2, 1), claim(1, 2)); result.Decision != "accept_home" ||
		!slices.Contains(similar.Flags, "similar_image") {
		t.Fatalf("similar image blocked the decision: %+v %v", result, similar.Flags)
	}
	// A pair that only lines up mirrored still agrees.
	mirrorPair := screenshotSample("SQUAD 0", 1, "Mugz FC", 2, map[string][]int{})
	mirrorPair.Stats = map[string][]int{}
	for key, value := range sampleMugz {
		mirrorPair.Stats[key] = []int{value[1], value[0]}
	}
	if result := evaluate(both(mugz, mirrorPair), claim(2, 1), claim(1, 2)); result.Decision != "accept_home" ||
		len(result.Differences) != 0 {
		t.Fatalf("mirrored genuine pair = %+v", result)
	}
	// A shoot-out is always checked by staff for now.
	shootout := screenshotSample("Mugz FC", 1, "SQUAD 0", 1, sampleMugz)
	penalties := json.RawMessage(`{"left":4,"right":3}`)
	shootout.Penalties = &penalties
	pensClaim := &claimScore{Score: screenshotScore{HomeScore: 1, AwayScore: 1, HomePenalties: intPointer(4), AwayPenalties: intPointer(3)}}
	if result := evaluate(both(shootout, shootout), pensClaim, claim(1, 2)); result.SupportedSide != "home" || result.Decision != "" {
		t.Fatalf("shoot-out = %+v", result)
	}
	bestOfThree := evaluateScreenshots(screenshotEvaluationInput{Readings: both(mugz, mugz), HomeNames: []string{"Mugz FC"},
		AwayNames: []string{"SQUAD 0"}, HomeClaim: claim(2, 1), AwayClaim: claim(1, 2), BestOf: 3, MinConfidence: 0.9})
	if bestOfThree.Decision != "" || bestOfThree.SupportedSide != "home" {
		t.Fatalf("best of three = %+v", bestOfThree)
	}
	series := claim(2, 1)
	series.Series = true
	if result := evaluate(both(mugz, mugz), series, claim(1, 2)); result.Decision != "" {
		t.Fatalf("series claim decided: %+v", result)
	}
	// Neither claim, unknown teams, pending, unreadable.
	if result := evaluate(both(mugz, mugz), claim(3, 1), claim(1, 3)); result.Verdict != verdictNeither {
		t.Fatalf("neither = %+v", result)
	}
	strangers := screenshotSample("Lions", 2, "Tigers", 1, sampleMugz)
	if result := evaluate(both(strangers, strangers), claim(2, 1), claim(1, 2)); result.Verdict != verdictUnclear ||
		result.Decision != "" {
		t.Fatalf("unknown teams = %+v", result)
	}
	pending := &evaluatedReading{EvidenceID: "e3", UploadedBySide: "away", Status: "queued"}
	if result := evaluate([]*evaluatedReading{evaluated("home", "e1", mugz), pending}, claim(2, 1), claim(1, 2)); result.Verdict != verdictPending {
		t.Fatalf("pending = %+v", result)
	}
	failed := &evaluatedReading{EvidenceID: "e4", UploadedBySide: "away", Status: "failed"}
	if result := evaluate([]*evaluatedReading{failed}, claim(2, 1), claim(1, 2)); result.Verdict != verdictUnreadable {
		t.Fatalf("unreadable = %+v", result)
	}
	if result := evaluate(append(both(mugz, mugz), failed), claim(2, 1), claim(1, 2)); result.Verdict != verdictSupports ||
		result.Decision != "" {
		t.Fatalf("partly unreadable = %+v", result)
	}
}

func TestVisionClientSendsTheImageAndClassifiesFailures(t *testing.T) {
	var gotAuth, gotType, gotEvidence string
	var gotBody []byte
	status, body := http.StatusOK, ""
	reader := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(status)
			return
		}
		gotAuth, gotType, gotEvidence = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("X-Evidence-ID")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer reader.Close()
	client := visionClient{baseURL: reader.URL, token: screenshotTestToken, http: reader.Client()}
	if !client.healthy(t.Context()) {
		t.Fatal("a healthy reader reported unhealthy")
	}
	answer, _ := json.Marshal(screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz))
	body = string(answer)
	reading, err := client.read(t.Context(), []byte("png"), "image/png", "evidence-1", "request-1")
	if err != nil || reading.LeftScore != 2 || reading.RightTeam != "SQUAD 0" {
		t.Fatalf("read = %+v, %v", reading, err)
	}
	if gotAuth != "Bearer "+screenshotTestToken || gotType != "image/png" || gotEvidence != "evidence-1" ||
		string(gotBody) != "png" {
		t.Fatalf("request = %q %q %q %q", gotAuth, gotType, gotEvidence, gotBody)
	}
	for _, testCase := range []struct {
		status    int
		body      string
		retryable bool
	}{
		{http.StatusUnprocessableEntity, `{"error":"undecodable_image"}`, false},
		{http.StatusUnsupportedMediaType, `{"error":"unsupported_media_type"}`, false},
		{http.StatusServiceUnavailable, `{"error":"model_not_loaded"}`, true},
		{http.StatusUnauthorized, `{"error":"invalid_token"}`, true},
		{http.StatusTooManyRequests, `{"error":"busy"}`, true},
		{http.StatusOK, `not json`, false},
		{http.StatusOK, `{"screen":"match_result","confidence":0.9}`, false},
	} {
		status, body = testCase.status, testCase.body
		_, err := client.read(t.Context(), []byte("png"), "image/png", "evidence-1", "")
		readerErr, ok := err.(*visionError)
		if !ok || readerErr.Retryable != testCase.retryable {
			t.Errorf("%d %s: err = %v", testCase.status, testCase.body, err)
		}
	}
	status = http.StatusServiceUnavailable
	if client.healthy(t.Context()) {
		t.Fatal("an unavailable reader reported healthy")
	}
}

const (
	screenshotTestToken         = "reader-token-0123456789-0123456789"
	screenshotTestTrainingToken = "training-token-0123456789-0123456789"
)

// screenshotTestStore serves every object's bytes as its key.
type screenshotTestStore struct{ resultReportsEvidenceStore }

func (screenshotTestStore) Read(_ context.Context, objectKey string, _ int64) ([]byte, error) {
	return []byte(objectKey), nil
}

// screenshotFakeReader answers per evidence id and counts its calls.
type screenshotFakeReader struct {
	mu        sync.Mutex
	answers   map[string]visionReadResponse
	unhealthy bool
	status    int
	calls     int
}

func (f *screenshotFakeReader) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/healthz" {
		if f.unhealthy {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return
	}
	f.calls++
	if r.Header.Get("Authorization") != "Bearer "+screenshotTestToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	answer, ok := f.answers[r.Header.Get("X-Evidence-ID")]
	if !ok {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"error":"undecodable_image"}`)
		return
	}
	_ = json.NewEncoder(w).Encode(answer)
}

func (f *screenshotFakeReader) set(evidenceID string, answer visionReadResponse) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[evidenceID] = answer
}

// screenshotDispute starts a match whose home player submits a result that
// the away player rejects; each then sends one screenshot.
func screenshotDispute(t *testing.T, h resultFlowHarness, matchID string, submitted [2]int) (resultReportsSides, string, string) {
	t.Helper()
	sides := resultReportsStart(t, h.Pool, matchID)
	resultFlowReject(t, h, sides, "home", submitted[0], submitted[1])
	homeEvidence := resultFlowRespond(t, h, sides.MatchID, sides.HomeUser)
	awayEvidence := resultFlowRespond(t, h, sides.MatchID, sides.AwayUser)
	return sides, homeEvidence, awayEvidence
}

func screenshotReaderHarness(t *testing.T) (resultFlowHarness, *screenshotFakeReader, func(), func()) {
	t.Helper()
	h := resultFlowSetup(t)
	fake := &screenshotFakeReader{answers: map[string]visionReadResponse{}}
	reader := httptest.NewServer(http.HandlerFunc(fake.handler))
	h.Server.evidenceStore = screenshotTestStore{}
	h.Server.config.VisionURL, h.Server.config.VisionToken = reader.URL, screenshotTestToken
	h.Server.config.VisionTrainingToken = screenshotTestTrainingToken
	h.Server.config.VisionTimeout, h.Server.config.VisionConcurrency = 5*time.Second, 2
	h.Server.config.VisionAutoDecide, h.Server.config.VisionAutoMinConfidence = true, 0.9
	client, store, ok := h.Server.screenshotReaderDeps()
	if !ok {
		t.Fatal("reader not configured")
	}
	var healthy atomic.Bool
	healthy.Store(true)
	tick := func() { h.Server.screenshotReaderTick(t.Context(), client, store, &healthy) }
	return h, fake, tick, reader.Close
}

func screenshotReviewState(t *testing.T, h resultFlowHarness, reviewID string) (string, string, string) {
	t.Helper()
	var status, deciderKind, decision string
	if err := h.Pool.QueryRow(t.Context(), `SELECT status,COALESCE(decider_kind,''),COALESCE(decision,'')
		FROM match_result_reviews WHERE id=$1`, reviewID).Scan(&status, &deciderKind, &decision); err != nil {
		t.Fatal(err)
	}
	return status, deciderKind, decision
}

// TestIntegrationScreenshotReaderLearnsFromStaffAndDecidesOnlyWhenSafe runs
// the reader over real disputes. With no learned team names it only informs
// staff; a staff decision teaches both names; with learned names, agreeing
// screenshots are settled automatically, and a renamed opponent changes
// nothing; a forged screenshot stays with staff and is never exported for
// training; a later copy of an image is flagged as reused.
func TestIntegrationScreenshotReaderLearnsFromStaffAndDecidesOnlyWhenSafe(t *testing.T) {
	h, fake, tick, closeReader := screenshotReaderHarness(t)
	defer closeReader()
	seeded := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	semifinals := readyIntegrationMatches(t, h.Pool, seeded.ID)

	// Semifinal one: nobody's team names are known, so it waits for staff.
	one, oneHome, oneAway := screenshotDispute(t, h, semifinals[0], [2]int{2, 1})
	mugz := screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz)
	fake.set(oneHome, mugz)
	fake.set(oneAway, mugz)
	oneReview := resultFlowReviewID(t, h.Pool, one.MatchID)
	tick()
	detail := resultFlowGetReview(t, h, h.StaffID, oneReview)
	if detail.Status != "queued" || detail.ScreenshotCheck == nil || detail.ScreenshotCheck.Verdict != verdictUnclear ||
		detail.ScreenshotCheck.AutoDecide {
		t.Fatalf("unknown teams = %s %+v", detail.Status, detail.ScreenshotCheck)
	}
	h.expectStatus(t, resultFlowDecide(t, h, h.StaffID, oneReview, "screenshot-staff-1", map[string]any{
		"expectedVersion": detail.Version, "decision": "accept_home", "note": "Both screenshots show Mugz FC 2-1.",
	}), http.StatusOK)
	learned := resultFlowStrings(t, h.Pool, `SELECT user_id::text||':'||name FROM player_team_names ORDER BY 1`)
	want := []string{one.HomeUser + ":Mugz FC", one.AwayUser + ":SQUAD 0"}
	slices.Sort(want)
	if !slices.Equal(learned, want) {
		t.Fatalf("learned team names = %v, want %v", learned, want)
	}

	// Semifinal two: both players' names were learned earlier. The losing
	// away player renames themselves after the winner's team; it changes
	// nothing, because only learned names identify a side.
	two := resultReportsStart(t, h.Pool, semifinals[1])
	resultFlowExec(t, h.Pool, `INSERT INTO player_team_names(user_id,name_key,name) VALUES ($1,'lionsfc','Lions FC'),
		($2,'tigerssc','Tigers SC')`, two.HomeUser, two.AwayUser)
	resultFlowExec(t, h.Pool, `UPDATE users SET display_name='Lions FC' WHERE id=$1`, two.AwayUser)
	resultFlowExec(t, h.Pool, `UPDATE game_accounts SET in_game_name='Lions FC' WHERE user_id=$1`, two.AwayUser)
	resultFlowReject(t, h, two, "home", 3, 0)
	twoHome := resultFlowRespond(t, h, two.MatchID, two.HomeUser)
	twoAway := resultFlowRespond(t, h, two.MatchID, two.AwayUser)
	lions := screenshotSample("Lions FC", 3, "Tigers SC", 0, sampleMzee)
	lions.ImageHash = "1111222233334444"
	fake.set(twoHome, lions)
	fake.set(twoAway, lions)
	twoReview := resultFlowReviewID(t, h.Pool, two.MatchID)
	tick()
	if status, kind, decision := screenshotReviewState(t, h, twoReview); status != "decided" || kind != "system" ||
		decision != "accept_home" {
		t.Fatalf("semifinal two = %s %s %s", status, kind, decision)
	}
	var confirmations int
	if err := h.Pool.QueryRow(t.Context(), `SELECT sum(confirmations) FROM player_team_names WHERE user_id=ANY($1::uuid[])`,
		[]string{two.HomeUser, two.AwayUser}).Scan(&confirmations); err != nil || confirmations != 2 {
		t.Fatalf("an automatic decision taught names: %d %v", confirmations, err)
	}

	// The final: the away player rejects home's 2-1 and forges a 1-2.
	final := resultReportsStart(t, h.Pool, readyIntegrationMatches(t, h.Pool, seeded.ID)[0])
	resultFlowReject(t, h, final, "home", 2, 1)
	finalHome := resultFlowRespond(t, h, final.MatchID, final.HomeUser)
	finalAway := resultFlowRespond(t, h, final.MatchID, final.AwayUser)
	names := map[string]string{one.HomeUser: "Mugz FC", two.HomeUser: "Lions FC"}
	honest := screenshotSample(names[final.HomeUser], 2, names[final.AwayUser], 1,
		map[string][]int{"possession": {55, 45}, "shotsOnTarget": {6, 3}, "saves": {2, 4}})
	honest.ImageHash = "5555666677778888"
	forged := honest
	forged.Left = &screenshotSide{Team: names[final.HomeUser], Score: 1}
	forged.Right = &screenshotSide{Team: names[final.AwayUser], Score: 2}
	forged.ImageHash = "9999aaaabbbbcccc"
	fake.set(finalHome, honest)
	fake.set(finalAway, forged)
	finalReview := resultFlowReviewID(t, h.Pool, final.MatchID)
	tick()
	detail = resultFlowGetReview(t, h, h.StaffID, finalReview)
	if detail.Status != "queued" || detail.ScreenshotCheck.Verdict != verdictConflict ||
		!slices.Contains(detail.ScreenshotCheck.Differences, "score") {
		t.Fatalf("forged final = %s %+v", detail.Status, detail.ScreenshotCheck)
	}

	// A later copy of semifinal one's image is the reuse, never the original.
	reusedCopy := mugz
	fake.set(finalHome, reusedCopy)
	resultFlowExec(t, h.Pool, `UPDATE screenshot_readings SET status='queued',screen=NULL,read_at=NULL,claim_token=NULL,
		available_at=now() WHERE evidence_id=$1`, finalHome)
	tick()
	var reuseKind, originalReuse *string
	if err := h.Pool.QueryRow(t.Context(), `SELECT (SELECT reuse_kind FROM screenshot_readings WHERE evidence_id=$1),
		(SELECT reuse_kind FROM screenshot_readings WHERE evidence_id=$2)`, finalHome, oneHome).
		Scan(&reuseKind, &originalReuse); err != nil || reuseKind == nil || *reuseKind != "stats" || originalReuse != nil {
		t.Fatalf("reuse = copy %v original %v, %v", reuseKind, originalReuse, err)
	}

	// Staff decide the final; its disagreeing screenshots still teach nothing
	// and never become training labels.
	h.expectStatus(t, resultFlowDecide(t, h, h.StaffID, finalReview, "screenshot-staff-2", map[string]any{
		"expectedVersion": detail.Version, "decision": "accept_home", "note": "The away screenshot was edited.",
	}), http.StatusOK)
	// Only staff-decided matches whose screenshots agree become examples:
	// semifinal one. Semifinal two was decided by the reader itself, and the
	// final's screenshots disagree.
	feed := screenshotTrainingFeed(t, h, screenshotTestTrainingToken, http.StatusOK)
	if len(feed) != 2 {
		t.Fatalf("training feed has %d examples, want semifinal one's 2", len(feed))
	}
	for _, example := range feed {
		if example.MatchID != one.MatchID || !example.ReaderCorrect || example.Source != "staff_decision" ||
			example.Label.Left.Team != "Mugz FC" || example.Label.Left.Score != 2 || example.Label.Right.Score != 1 ||
			example.DownloadURL == "" || example.ContentType == "" {
			t.Fatalf("training example = %+v", example)
		}
	}
	screenshotTrainingFeed(t, h, screenshotTestToken, http.StatusUnauthorized)

	// A decided review keeps its readings.
	request := httptest.NewRequest(http.MethodPost, "/v1/admin/screenshot-readings/"+oneHome+"/retry", nil)
	request.SetPathValue("evidenceId", oneHome)
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: h.StaffID}))
	recorder := httptest.NewRecorder()
	h.Server.retryScreenshotReading(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("retry on a decided review = %d %s", recorder.Code, recorder.Body)
	}
}

func screenshotTrainingFeed(t *testing.T, h resultFlowHarness, token string, status int) []visionTrainingExample {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/internal/vision/training-examples?limit=50", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	h.Server.listVisionTrainingExamples(recorder, request)
	if recorder.Code != status {
		t.Fatalf("training feed = %d %s", recorder.Code, recorder.Body)
	}
	var body struct {
		Items      []visionTrainingExample `json:"items"`
		NextCursor *string                 `json:"nextCursor"`
	}
	if status == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return body.Items
}

func (h resultFlowHarness) expectStatus(t *testing.T, recorder *httptest.ResponseRecorder, status int) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("response = %d %s, want %d", recorder.Code, recorder.Body, status)
	}
}

// TestIntegrationScreenshotReaderSurvivesOutagesAndStaleClaims checks that a
// reader outage never fails a screenshot, that a late result from an expired
// claim is dropped, that a refused image fails at once, and that with reading
// off a waiting screenshot is not shown as forever reading.
func TestIntegrationScreenshotReaderSurvivesOutagesAndStaleClaims(t *testing.T) {
	h, fake, tick, closeReader := screenshotReaderHarness(t)
	defer closeReader()
	seeded := seedIntegrationCompetition(t, h.Pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	sides, homeEvidence, awayEvidence := screenshotDispute(t, h, readyIntegrationMatches(t, h.Pool, seeded.ID)[0], [2]int{2, 1})
	readingState := func(evidenceID string) (string, int, int) {
		t.Helper()
		var status string
		var attempts, outages int
		if err := h.Pool.QueryRow(t.Context(), `SELECT status,attempts,outage_retries FROM screenshot_readings
			WHERE evidence_id=$1`, evidenceID).Scan(&status, &attempts, &outages); err != nil {
			t.Fatal(err)
		}
		return status, attempts, outages
	}

	// While /healthz fails nothing is claimed.
	fake.mu.Lock()
	fake.unhealthy = true
	fake.mu.Unlock()
	tick()
	if status, attempts, _ := readingState(homeEvidence); status != "queued" || attempts != 0 {
		t.Fatalf("claimed while the reader was down: %s %d", status, attempts)
	}
	// A reader that answers 503 delays the screenshot without failing it, and
	// the tick stops there instead of walking the rest of the backlog past
	// the health check: with one slot, only one of the two is claimed.
	fake.mu.Lock()
	fake.unhealthy, fake.status = false, http.StatusServiceUnavailable
	fake.mu.Unlock()
	h.Server.config.VisionConcurrency = 1
	tick()
	h.Server.config.VisionConcurrency = 2
	homeStatus, homeAttempts, homeOutages := readingState(homeEvidence)
	awayStatus, awayAttempts, awayOutages := readingState(awayEvidence)
	if homeStatus != "queued" || awayStatus != "queued" || homeAttempts+awayAttempts != 1 ||
		homeOutages+awayOutages != 1 {
		t.Fatalf("outage = %s/%s, attempts %d+%d, outages %d+%d (one claim, then stop)",
			homeStatus, awayStatus, homeAttempts, awayAttempts, homeOutages, awayOutages)
	}

	// A result from an expired claim is dropped.
	stored, err := h.Server.storeScreenshotReading(t.Context(), screenshotJob{EvidenceID: homeEvidence,
		MatchID: sides.MatchID, Token: "00000000-0000-4000-8000-000000000000"},
		mustValidate(t, screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz)))
	if err != nil || stored {
		t.Fatalf("stale claim stored = %v %v", stored, err)
	}

	// A refused image fails at once.
	fake.mu.Lock()
	fake.status = 0
	fake.mu.Unlock()
	fake.set(homeEvidence, screenshotSample("Mugz FC", 2, "SQUAD 0", 1, sampleMugz))
	resultFlowExec(t, h.Pool, `UPDATE screenshot_readings SET available_at=now() WHERE match_id=$1`, sides.MatchID)
	tick()
	if status, _, _ := readingState(awayEvidence); status != "failed" {
		t.Fatalf("refused image = %s", status)
	}
	if status, _, _ := readingState(homeEvidence); status != "read" {
		t.Fatalf("readable image = %s", status)
	}

	// With reading off, a screenshot still waiting is left out of the view.
	resultFlowExec(t, h.Pool, `UPDATE screenshot_readings SET status='queued',screen=NULL,read_at=NULL
		WHERE evidence_id=$1`, homeEvidence)
	h.Server.config.VisionURL = ""
	detail := resultFlowGetReview(t, h, h.StaffID, resultFlowReviewID(t, h.Pool, sides.MatchID))
	if detail.Reports.Home.Final.Evidence[0].Reading != nil || detail.ScreenshotCheck == nil ||
		detail.ScreenshotCheck.Verdict != verdictUnreadable {
		t.Fatalf("reader off = %+v %+v", detail.Reports.Home.Final.Evidence[0].Reading, detail.ScreenshotCheck)
	}
}
