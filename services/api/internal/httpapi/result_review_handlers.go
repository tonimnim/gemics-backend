package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const resultReviewCursorKind = "result-review-queue"

func resultReviewDecisionScope(actorID, reviewID string) string {
	return "result-review-decision:" + actorID + ":" + reviewID
}

type resultReviewMatchView struct {
	ID              string `json:"id"`
	CompetitionID   string `json:"competitionId"`
	CompetitionName string `json:"competitionName"`
	GameID          string `json:"gameId"`
	StageFormat     string `json:"stageFormat"`
	Bracket         string `json:"bracket"`
	RoundNumber     int    `json:"roundNumber"`
	MatchNumber     int    `json:"matchNumber"`
	BestOf          int    `json:"bestOf"`
}

type resultReviewSummary struct {
	ID        string                `json:"id"`
	Status    string                `json:"status"`
	Reason    string                `json:"reason"`
	Version   int                   `json:"version"`
	QueuedAt  time.Time             `json:"queuedAt"`
	Decision  *string               `json:"decision"`
	DecidedAt *time.Time            `json:"decidedAt"`
	DecidedBy *string               `json:"decidedBy"`
	Match     resultReviewMatchView `json:"match"`
}

type resultReviewParticipant struct {
	EntryID       string  `json:"entryId"`
	EntryStatus   string  `json:"entryStatus"`
	CaptainUserID string  `json:"captainUserId"`
	DisplayName   string  `json:"displayName"`
	Handle        *string `json:"handle"`
}

type resultReviewParticipants struct {
	Home resultReviewParticipant `json:"home"`
	Away resultReviewParticipant `json:"away"`
}

type staffReportEvidenceView struct {
	ID        string `json:"id"`
	MediaType string `json:"mediaType"`
	ByteSize  int64  `json:"byteSize"`
	Ready     bool   `json:"ready"`
}

type staffReporterView struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
}

// staffReportView is one blind claim as Gamics staff see it. Screenshots are
// fetched through GET /v1/evidence/{id}, which re-checks the reviewer.
type staffReportView struct {
	ID         string                    `json:"id"`
	ReportedBy staffReporterView         `json:"reportedBy"`
	HomeScore  int                       `json:"homeScore"`
	AwayScore  int                       `json:"awayScore"`
	Tiebreak   *tiebreakScoreInput       `json:"tiebreak"`
	Games      []gameScoreInput          `json:"games"`
	ReportedAt time.Time                 `json:"reportedAt"`
	Evidence   []staffReportEvidenceView `json:"evidence"`
}

type resultReviewEntryReports struct {
	Initial *staffReportView `json:"initial"`
	Final   *staffReportView `json:"final"`
}

type resultReviewReports struct {
	Home resultReviewEntryReports `json:"home"`
	Away resultReviewEntryReports `json:"away"`
}

type resultReviewVerificationView struct {
	FirstReportEntryID string     `json:"firstReportEntryId"`
	FirstReportedAt    time.Time  `json:"firstReportedAt"`
	ReportDeadlineAt   time.Time  `json:"reportDeadlineAt"`
	MismatchAt         *time.Time `json:"mismatchAt"`
	ResponseDeadlineAt *time.Time `json:"responseDeadlineAt"`
}

type resultReviewWindowUploadView struct {
	EntryID             string    `json:"entryId"`
	EvidenceID          string    `json:"evidenceId"`
	UploadedBy          string    `json:"uploadedBy"`
	Status              string    `json:"status"`
	ProcessingErrorCode *string   `json:"processingErrorCode"`
	CreatedAt           time.Time `json:"createdAt"`
}

type resultReviewDecisionView struct {
	Decision       string            `json:"decision"`
	CorrectedScore *reviewScoreInput `json:"correctedScore"`
	Note           string            `json:"note"`
	DeciderKind    string            `json:"deciderKind"`
	DecidedBy      *string           `json:"decidedBy"`
	DeciderRef     *string           `json:"deciderRef"`
	DecidedAt      time.Time         `json:"decidedAt"`
}

// resultReviewDetail extends the summary. Its decision object shadows the
// summary's decision string in JSON, as the contract describes.
type resultReviewDetail struct {
	resultReviewSummary
	Participants          resultReviewParticipants       `json:"participants"`
	Reports               resultReviewReports            `json:"reports"`
	Verification          resultReviewVerificationView   `json:"verification"`
	ResponseWindowUploads []resultReviewWindowUploadView `json:"responseWindowUploads"`
	ActiveStrikeCounts    map[string]int                 `json:"activeStrikeCounts"`
	Strikes               []playerStrikeView             `json:"strikes"`
	Decision              *resultReviewDecisionView      `json:"decision"`
}

// resultReviewSummaryColumns reads a review joined to its match as "m", which
// resultReviewConflictClause needs.
const resultReviewSummaryColumns = `review.id::text,review.status,review.reason,review.version,review.queued_at,
	review.decision,review.decided_at,review.decided_by::text,m.id::text,m.competition_id::text,competition.name,
	competition.game_id,stage.format,m.bracket,m.round_number,m.match_number,stage.best_of`

const resultReviewFrom = ` FROM match_result_reviews review
	JOIN matches m ON m.id=review.match_id
	JOIN competitions competition ON competition.id=m.competition_id
	JOIN competition_stages stage ON stage.id=m.stage_id `

func resultReviewSummaryTargets(summary *resultReviewSummary) []any {
	return []any{&summary.ID, &summary.Status, &summary.Reason, &summary.Version, &summary.QueuedAt,
		&summary.Decision, &summary.DecidedAt, &summary.DecidedBy, &summary.Match.ID, &summary.Match.CompetitionID,
		&summary.Match.CompetitionName, &summary.Match.GameID, &summary.Match.StageFormat, &summary.Match.Bracket,
		&summary.Match.RoundNumber, &summary.Match.MatchNumber, &summary.Match.BestOf}
}

type resultReviewFilters struct {
	Status        string
	CompetitionID string
}

// scope binds a cursor to every filter, so a cursor from one view of the queue
// can never page through another.
func (filters resultReviewFilters) scope() string {
	return filters.Status + "|" + filters.CompetitionID
}

// listResultReviews serves the Gamics review queue. Queued reviews are served
// oldest first; decided and closed ones newest first. Reviews of matches the
// operator plays in, captains in or organizes are silently omitted (D27).
func (s *Server) listResultReviews(w http.ResponseWriter, r *http.Request) {
	filters, limit, cursor, ok := s.resultReviewPageInput(w, r)
	if !ok {
		return
	}
	actorID := identityFromContext(r.Context()).UserID
	items, err := queryResultReviewPage(r.Context(), s.db.Writer, filters, actorID, cursor, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the result review queue.")
		return
	}
	page := staffQueuePage{}
	if len(items) > limit {
		items = items[:limit]
		page.HasMore = true
		last := items[len(items)-1]
		var next string
		var encodeErr error
		if filters.Status == "all" {
			next, encodeErr = s.encodeStaffQueueKeyCursor(resultReviewCursorKind, filters.scope(), actorID,
				resultReviewOrderKey(last), last.ID)
		} else {
			sortTime := last.QueuedAt
			if filters.Status != "queued" && last.DecidedAt != nil {
				sortTime = *last.DecidedAt
			}
			next, encodeErr = s.encodeStaffQueueCursor(resultReviewCursorKind, filters.scope(), actorID, sortTime, last.ID)
		}
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate the result review queue.")
			return
		}
		page.NextCursor = &next
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": page})
}

// resultReviewPageInput validates the queue filters and a cursor bound to the
// operator and to every filter.
func (s *Server) resultReviewPageInput(w http.ResponseWriter, r *http.Request) (resultReviewFilters, int, *publicCursor, bool) {
	filters := resultReviewFilters{
		Status:        strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status"))),
		CompetitionID: strings.ToLower(strings.TrimSpace(r.URL.Query().Get("competitionId"))),
	}
	if filters.Status == "" {
		filters.Status = "queued"
	}
	if filters.Status != "queued" && filters.Status != "decided" && filters.Status != "closed" && filters.Status != "all" {
		writeError(w, http.StatusBadRequest, "invalid_status", "Choose queued, decided, closed or all.")
		return resultReviewFilters{}, 0, nil, false
	}
	if filters.CompetitionID != "" && !uuidPattern.MatchString(filters.CompetitionID) {
		writeError(w, http.StatusBadRequest, "invalid_competition", "Choose a valid competition.")
		return resultReviewFilters{}, 0, nil, false
	}
	limit, cursor, ok := s.staffQueuePageInput(w, r, resultReviewCursorKind, filters.scope())
	if !ok {
		return resultReviewFilters{}, 0, nil, false
	}
	return filters, limit, cursor, true
}

// queryResultReviewPage reads up to limit reviews of one keyset page. Queued
// reviews use the queue index; decided reviews use the decided index.
func queryResultReviewPage(ctx context.Context, queryer rowsQueryer, filters resultReviewFilters, actorID string,
	cursor *publicCursor, limit int) ([]resultReviewSummary, error) {
	var competitionID any
	if filters.CompetitionID != "" {
		competitionID = filters.CompetitionID
	}
	if filters.Status == "all" {
		return queryAllResultReviews(ctx, queryer, competitionID, actorID, cursor, limit)
	}
	var afterTime *time.Time
	var afterID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		afterTime, afterID = &value, &cursor.ID
	}
	order := ` AND ($4::timestamptz IS NULL OR (review.queued_at,review.id)>($4,$5::uuid))
		ORDER BY review.queued_at,review.id LIMIT $6`
	if filters.Status != "queued" {
		order = ` AND ($4::timestamptz IS NULL OR (review.decided_at,review.id)<($4,$5::uuid))
		ORDER BY review.decided_at DESC,review.id DESC LIMIT $6`
	}
	result, err := queryer.Query(ctx, `SELECT `+resultReviewSummaryColumns+resultReviewFrom+`
		WHERE review.status=$1 AND ($2::uuid IS NULL OR review.competition_id=$2::uuid)
		AND NOT `+resultReviewConflictClause("m", "$3::uuid")+order,
		filters.Status, competitionID, actorID, afterTime, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	items := make([]resultReviewSummary, 0, limit)
	for result.Next() {
		var item resultReviewSummary
		if err = result.Scan(resultReviewSummaryTargets(&item)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, result.Err()
}

// resultReviewOrderSQL orders every review: queued ones first, oldest first,
// then decided and closed ones, newest first.
var resultReviewOrderSQL = staffQueueOrderKey("review.status='queued'", "review.queued_at",
	"COALESCE(review.decided_at,review.queued_at)")

func resultReviewOrderKey(item resultReviewSummary) int64 {
	return staffQueueKey(item.Status == "queued", item.QueuedAt, timeOr(item.DecidedAt, item.QueuedAt))
}

// queryAllResultReviews reads one keyset page of reviews in every status.
func queryAllResultReviews(ctx context.Context, queryer rowsQueryer, competitionID any, actorID string,
	cursor *publicCursor, limit int) ([]resultReviewSummary, error) {
	var afterKey *int64
	var afterID *string
	if cursor != nil {
		afterKey, afterID = &cursor.SortTime, &cursor.ID
	}
	result, err := queryer.Query(ctx, `SELECT `+resultReviewSummaryColumns+resultReviewFrom+`
		WHERE ($1::uuid IS NULL OR review.competition_id=$1::uuid)
		AND NOT `+resultReviewConflictClause("m", "$2::uuid")+`
		AND ($3::bigint IS NULL OR (`+resultReviewOrderSQL+`,review.id)>($3,$4::uuid))
		ORDER BY `+resultReviewOrderSQL+`,review.id LIMIT $5`,
		competitionID, actorID, afterKey, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	items := make([]resultReviewSummary, 0, limit)
	for result.Next() {
		var item resultReviewSummary
		if err = result.Scan(resultReviewSummaryTargets(&item)...); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, result.Err()
}

// getResultReview serves one review with both sides' claims. A conflicted
// operator is refused rather than shown a match they are part of (D27).
func (s *Server) getResultReview(w http.ResponseWriter, r *http.Request) {
	reviewID := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !uuidPattern.MatchString(reviewID) {
		writeError(w, http.StatusNotFound, "result_review_not_found", "Result review not found.")
		return
	}
	detail, err := loadResultReviewDetail(r.Context(), s.db.Writer, reviewID, identityFromContext(r.Context()).UserID)
	if err != nil {
		s.writeResultReviewFailure(w, reviewID, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
}

// decideResultReview records a staff decision (T13, T14) through the only
// decision path, decideResultReviewInTx, which takes every lock itself.
func (s *Server) decideResultReview(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	reviewID := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !uuidPattern.MatchString(reviewID) {
		writeError(w, http.StatusNotFound, "result_review_not_found", "Result review not found.")
		return
	}
	var input reviewDecisionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if rejection := input.normalize(); rejection != nil {
		writeError(w, rejection.Status, rejection.Code, rejection.Message)
		return
	}
	requestHash, err := hashRequest(input)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to decide the review.")
		return
	}
	ctx := r.Context()
	actorID := identityFromContext(ctx).UserID
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide the review.")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	scope := resultReviewDecisionScope(actorID, reviewID)
	replay, err := beginIdempotentRequest(ctx, tx, scope, idempotencyKey, requestHash)
	if errors.Is(err, errIdempotencyConflict) {
		writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another review decision.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to decide the review.")
		return
	}
	if replay != nil {
		w.Header().Set("Idempotency-Replayed", "true")
		writeResultRawJSON(w, replay.Status, replay.Body)
		return
	}
	outcome, err := decideResultReviewInTx(ctx, tx, reviewID,
		reviewActor{Kind: "staff", UserID: &actorID, RequestID: r.Header.Get("X-Request-ID")}, input)
	if err != nil {
		s.writeResultReviewFailure(w, reviewID, err)
		return
	}
	detail, err := loadResultReviewDetail(ctx, tx, reviewID, actorID)
	if err != nil {
		s.writeResultReviewFailure(w, reviewID, err)
		return
	}
	body, err := json.Marshal(map[string]any{"data": detail})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to decide the review.")
		return
	}
	if err = finishIdempotentRequest(ctx, tx, scope, idempotencyKey, http.StatusOK, body); err != nil || tx.Commit(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the review decision.")
		return
	}
	s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), outcome.CompetitionID)
	writeResultRawJSON(w, http.StatusOK, body)
}

// writeResultReviewFailure maps a failed read or decision. Invariants are bugs
// and are logged by class only: raw database errors can carry row data.
func (s *Server) writeResultReviewFailure(w http.ResponseWriter, reviewID string, err error) {
	var rejection *reviewDecisionRejection
	switch {
	case errors.As(err, &rejection):
		if rejection.Status >= http.StatusInternalServerError {
			s.logger.Error("result review state invariant", "review_id", reviewID, "cause", rejection.Cause)
		}
		writeError(w, rejection.Status, rejection.Code, rejection.Message)
	case errors.Is(err, errResultReviewNotFound):
		writeError(w, http.StatusNotFound, "result_review_not_found", "Result review not found.")
	case errors.Is(err, errResultReviewConflict):
		writeError(w, http.StatusForbidden, "result_review_conflict", "You can't review a match you play in or organize.")
	case errors.Is(err, errCompetitionClosed):
		writeError(w, http.StatusConflict, "competition_closed", "This competition is closed, so its results can no longer change.")
	case errors.Is(err, errMatchResolutionChanged), errors.Is(err, errMatchProgressionConflict):
		writeError(w, http.StatusConflict, "match_changed", "The match changed while the review was being decided. Refresh it and try again.")
	default:
		class := resultVerificationErrorClass(err)
		if class == "database" {
			s.logger.Warn("result review", "review_id", reviewID, "class", class)
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load or decide the review.")
			return
		}
		s.logger.Error("result review", "review_id", reviewID, "class", class)
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to load or decide the review.")
	}
}

// loadResultReviewDetail reads one review for a staff viewer. A conflicted
// viewer gets errResultReviewConflict before any claim is read.
func loadResultReviewDetail(ctx context.Context, queryer rowsQueryer, reviewID, viewerID string) (resultReviewDetail, error) {
	detail, conflicted, err := queryResultReviewHead(ctx, queryer, reviewID, viewerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return resultReviewDetail{}, errResultReviewNotFound
	}
	if err != nil {
		return resultReviewDetail{}, err
	}
	if conflicted {
		return resultReviewDetail{}, errResultReviewConflict
	}
	reports, err := queryResultReviewReports(ctx, queryer, detail.Match.ID)
	if err != nil {
		return resultReviewDetail{}, err
	}
	for index := range reports {
		detail.placeReport(reports[index])
	}
	if detail.ActiveStrikeCounts, err = queryReporterActiveStrikes(ctx, queryer, detail.Match.ID); err != nil {
		return resultReviewDetail{}, err
	}
	if detail.Strikes, err = queryPlayerStrikes(ctx, queryer, playerStrikeSelect+`WHERE strike.review_id=$1
		ORDER BY strike.created_at,strike.id`, reviewID); err != nil {
		return resultReviewDetail{}, err
	}
	detail.ResponseWindowUploads = []resultReviewWindowUploadView{}
	if detail.Reason == "evidence_unavailable" {
		if detail.ResponseWindowUploads, err = queryResultReviewWindowUploads(ctx, queryer, detail, reports); err != nil {
			return resultReviewDetail{}, err
		}
	}
	return detail, nil
}

// queryResultReviewHead reads the review, its match, both entries, the
// verification windows and the stored decision in one statement, together
// with whether the viewer is conflicted.
func queryResultReviewHead(ctx context.Context, queryer rowsQueryer, reviewID, viewerID string) (
	resultReviewDetail, bool, error) {
	var detail resultReviewDetail
	summary := &detail.resultReviewSummary
	var decisionNote string
	var deciderKind, deciderRef, tiebreakType *string
	var correctedHome, correctedAway, homeTiebreak, awayTiebreak *int
	var correctedGames []byte
	var conflicted bool
	targets := append(resultReviewSummaryTargets(summary),
		&decisionNote, &deciderKind, &deciderRef, &correctedHome, &correctedAway, &tiebreakType, &homeTiebreak,
		&awayTiebreak, &correctedGames,
		&detail.Participants.Home.EntryID, &detail.Participants.Home.EntryStatus, &detail.Participants.Home.CaptainUserID,
		&detail.Participants.Home.DisplayName, &detail.Participants.Home.Handle,
		&detail.Participants.Away.EntryID, &detail.Participants.Away.EntryStatus, &detail.Participants.Away.CaptainUserID,
		&detail.Participants.Away.DisplayName, &detail.Participants.Away.Handle,
		&detail.Verification.FirstReportEntryID, &detail.Verification.FirstReportedAt,
		&detail.Verification.ReportDeadlineAt, &detail.Verification.MismatchAt, &detail.Verification.ResponseDeadlineAt,
		&conflicted)
	err := queryer.QueryRow(ctx, `SELECT `+resultReviewSummaryColumns+`,
		review.note,review.decider_kind,review.decider_ref,review.corrected_home_score,review.corrected_away_score,
		review.corrected_tiebreak_type,review.corrected_home_tiebreak_score,review.corrected_away_tiebreak_score,
		review.corrected_game_results,
		home.id::text,home.status,home.captain_user_id::text,home.display_name,home_profile.handle,
		away.id::text,away.status,away.captain_user_id::text,away.display_name,away_profile.handle,
		verification.first_report_entry_id::text,verification.first_reported_at,verification.report_deadline_at,
		verification.mismatch_at,verification.response_deadline_at,
		`+resultReviewConflictClause("m", "$2::uuid")+resultReviewFrom+`
		JOIN match_result_verifications verification ON verification.match_id=review.match_id
		JOIN competition_entries home ON home.id=m.home_entry_id
		JOIN competition_entries away ON away.id=m.away_entry_id
		LEFT JOIN player_profiles home_profile ON home_profile.user_id=home.captain_user_id
		LEFT JOIN player_profiles away_profile ON away_profile.user_id=away.captain_user_id
		WHERE review.id=$1`, reviewID, viewerID).Scan(targets...)
	if err != nil {
		return resultReviewDetail{}, false, err
	}
	summary.QueuedAt, summary.DecidedAt = summary.QueuedAt.UTC(), utcTime(summary.DecidedAt)
	if summary.Status == "decided" && summary.Decision != nil && deciderKind != nil && summary.DecidedAt != nil {
		decision := &resultReviewDecisionView{Decision: *summary.Decision, Note: decisionNote, DeciderKind: *deciderKind,
			DecidedBy: summary.DecidedBy, DeciderRef: deciderRef, DecidedAt: *summary.DecidedAt}
		if correctedHome != nil && correctedAway != nil {
			decision.CorrectedScore = &reviewScoreInput{HomeScore: *correctedHome, AwayScore: *correctedAway}
			if tiebreakType != nil && homeTiebreak != nil && awayTiebreak != nil {
				decision.CorrectedScore.Tiebreak = &tiebreakScoreInput{Type: *tiebreakType, HomeScore: *homeTiebreak,
					AwayScore: *awayTiebreak}
			}
			if err = decodeStoredJSON(correctedGames, &decision.CorrectedScore.Games, "corrected games"); err != nil {
				return resultReviewDetail{}, false, err
			}
		}
		detail.Decision = decision
	}
	detail.Verification.FirstReportedAt = detail.Verification.FirstReportedAt.UTC()
	detail.Verification.ReportDeadlineAt = detail.Verification.ReportDeadlineAt.UTC()
	detail.Verification.MismatchAt = utcTime(detail.Verification.MismatchAt)
	detail.Verification.ResponseDeadlineAt = utcTime(detail.Verification.ResponseDeadlineAt)
	return detail, conflicted, nil
}

type resultReviewReport struct {
	EntryID, Kind string
	View          staffReportView
}

func queryResultReviewReports(ctx context.Context, queryer rowsQueryer, matchID string) ([]resultReviewReport, error) {
	rows, err := queryer.Query(ctx, `SELECT report.id::text,report.entry_id::text,report.kind,report.reported_by::text,
		reporter.display_name,report.home_score,report.away_score,report.tiebreak_type,report.home_tiebreak_score,
		report.away_tiebreak_score,report.game_results,report.reported_at,
		COALESCE((SELECT json_agg(json_build_object('id',evidence.id::text,'mediaType',evidence.media_type,
			'byteSize',evidence.byte_size,'ready',evidence.status='completed') ORDER BY link.position)
			FROM match_result_report_evidence link JOIN evidence_uploads evidence ON evidence.id=link.evidence_id
			WHERE link.report_id=report.id),'[]'::json)
		FROM match_result_reports report JOIN users reporter ON reporter.id=report.reported_by
		WHERE report.match_id=$1 ORDER BY report.entry_id,report.kind`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	reports := make([]resultReviewReport, 0, 4)
	for rows.Next() {
		var report resultReviewReport
		var tiebreakType *string
		var homeTiebreak, awayTiebreak *int
		var games, evidence []byte
		view := &report.View
		if err = rows.Scan(&view.ID, &report.EntryID, &report.Kind, &view.ReportedBy.UserID, &view.ReportedBy.DisplayName,
			&view.HomeScore, &view.AwayScore, &tiebreakType, &homeTiebreak, &awayTiebreak, &games, &view.ReportedAt,
			&evidence); err != nil {
			return nil, err
		}
		if tiebreakType != nil && homeTiebreak != nil && awayTiebreak != nil {
			view.Tiebreak = &tiebreakScoreInput{Type: *tiebreakType, HomeScore: *homeTiebreak, AwayScore: *awayTiebreak}
		}
		if err = decodeStoredJSON(games, &view.Games, "report games"); err != nil {
			return nil, err
		}
		if err = decodeStoredJSON(evidence, &view.Evidence, "report evidence"); err != nil {
			return nil, err
		}
		view.ReportedAt = view.ReportedAt.UTC()
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

// placeReport files a claim under its entry's side and kind.
func (detail *resultReviewDetail) placeReport(report resultReviewReport) {
	side := &detail.Reports.Home
	if report.EntryID == detail.Participants.Away.EntryID {
		side = &detail.Reports.Away
	} else if report.EntryID != detail.Participants.Home.EntryID {
		return
	}
	view := report.View
	if report.Kind == "final" {
		side.Final = &view
		return
	}
	side.Initial = &view
}

// queryReporterActiveStrikes counts the active strikes of everyone who
// reported on the match, including reporters with none.
func queryReporterActiveStrikes(ctx context.Context, queryer rowsQueryer, matchID string) (map[string]int, error) {
	rows, err := queryer.Query(ctx, `SELECT reporter.user_id::text,count(strike.id)::integer
		FROM (SELECT DISTINCT reported_by AS user_id FROM match_result_reports WHERE match_id=$1) reporter
		LEFT JOIN player_strikes strike ON strike.user_id=reporter.user_id AND strike.revoked_at IS NULL
		GROUP BY reporter.user_id ORDER BY reporter.user_id`, matchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var userID string
		var count int
		if err = rows.Scan(&userID, &count); err != nil {
			return nil, err
		}
		counts[userID] = count
	}
	return counts, rows.Err()
}

// queryResultReviewWindowUploads lists every upload a non-responder made during
// the response window of an evidence_unavailable review (T16), with its
// current status. Filtering by status here would hide exactly the screenshot
// that caused the review once processing catches up, so none is applied; the
// reviewer opens completed ones through GET /v1/evidence/{id}.
func queryResultReviewWindowUploads(ctx context.Context, queryer rowsQueryer, detail resultReviewDetail,
	reports []resultReviewReport) ([]resultReviewWindowUploadView, error) {
	m := lockedResultMatch{ID: detail.Match.ID, CompetitionID: detail.Match.CompetitionID,
		HomeEntryID: &detail.Participants.Home.EntryID, AwayEntryID: &detail.Participants.Away.EntryID}
	v := &lockedVerification{MismatchAt: detail.Verification.MismatchAt, ResponseDeadlineAt: detail.Verification.ResponseDeadlineAt}
	claims := make([]verificationReport, 0, len(reports))
	for _, report := range reports {
		claims = append(claims, verificationReport{ID: report.View.ID, EntryID: report.EntryID, Kind: report.Kind,
			ReportedBy: report.View.ReportedBy.UserID})
	}
	uploads, err := loadResponseWindowUploads(ctx, queryer, m, v, claims, false)
	if err != nil {
		return nil, err
	}
	views := make([]resultReviewWindowUploadView, 0, len(uploads))
	for _, item := range uploads {
		views = append(views, resultReviewWindowUploadView{EntryID: item.EntryID, EvidenceID: item.EvidenceID,
			UploadedBy: item.UploadedBy, Status: item.Status, ProcessingErrorCode: item.ProcessingErrorCode,
			CreatedAt: item.CreatedAt})
	}
	return views, nil
}
