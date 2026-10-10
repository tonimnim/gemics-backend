package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	errResultReviewNotFound = errors.New("result review not found")
	// errResultReviewConflict means the staff member plays in, captains in or
	// organizes the match under review (D27).
	errResultReviewConflict = errors.New("result reviewer has a conflict of interest")
)

const (
	resultReviewNoteMinRunes        = 10
	resultReviewNoteMaxRunes        = 2000
	resultReviewMaxStrikes          = 2
	resultReviewDeciderRefMaxRunes  = 128
	resultReviewStrikeReason        = "false_result_report"
	resultReviewSystemRequestPrefix = "match-review-system:"
)

// reviewActor decides a Gamics result review. Staff decide through the admin
// API with their request's id. A future automated "system" decider calls
// decideResultReviewInTx with its own DeciderRef, from which its request id is
// derived; it can neither correct a score nor record a strike, so an
// unreviewed automated decision never leads to a ban (D13).
type reviewActor struct {
	Kind       string // staff | system
	UserID     *string
	RequestID  string
	DeciderRef string
}

func (actor reviewActor) validate() error {
	switch {
	case actor.Kind == "staff" && actor.UserID != nil && *actor.UserID != "" && actor.DeciderRef == "":
		return nil
	case actor.Kind == "system" && actor.UserID == nil && actor.DeciderRef != "" &&
		utf8.RuneCountInString(actor.DeciderRef) <= resultReviewDeciderRefMaxRunes:
		return nil
	default:
		return fmt.Errorf("%w: invalid %q review actor", errResultVerificationInvariant, actor.Kind)
	}
}

// resolutionActor is who the finalizer, removals and audit rows name. A system
// decider is recorded with no user and a request id derived from its reference.
func (actor reviewActor) resolutionActor() resolutionActor {
	requestID := actor.RequestID
	if actor.Kind == "system" {
		requestID = resultReviewSystemRequestPrefix + actor.DeciderRef
	}
	return resolutionActor{Kind: actor.Kind, UserID: cloneOptionalString(actor.UserID), RequestID: requestID}
}

// reviewScoreInput is the score Gamics decides when neither claim is right.
type reviewScoreInput struct {
	HomeScore int                 `json:"homeScore"`
	AwayScore int                 `json:"awayScore"`
	Games     []gameScoreInput    `json:"games,omitempty"`
	Tiebreak  *tiebreakScoreInput `json:"tiebreak"`
}

func (input reviewScoreInput) claimInput() scoreReportInput {
	return scoreReportInput{HomeScore: input.HomeScore, AwayScore: input.AwayScore, Games: input.Games,
		Tiebreak: input.Tiebreak}
}

type reviewDecisionInput struct {
	ExpectedVersion int               `json:"expectedVersion"`
	Decision        string            `json:"decision"`
	CorrectedScore  *reviewScoreInput `json:"correctedScore"`
	StrikeUserIDs   []string          `json:"strikeUserIds"`
	Note            string            `json:"note"`
}

// normalize canonicalizes the decision and rejects what no review could
// accept. It runs before any lock, and again inside decideResultReviewInTx so
// a future system caller gets the same checks.
func (input *reviewDecisionInput) normalize() *planRejection {
	input.Decision = strings.ToLower(strings.TrimSpace(input.Decision))
	input.Note = strings.TrimSpace(input.Note)
	for index, userID := range input.StrikeUserIDs {
		input.StrikeUserIDs[index] = strings.ToLower(strings.TrimSpace(userID))
	}
	switch input.Decision {
	case "accept_home", "accept_away", "corrected_score", "remove_both":
	default:
		return invalidReviewDecision("Choose accept_home, accept_away, corrected_score or remove_both.")
	}
	if (input.Decision == "corrected_score") != (input.CorrectedScore != nil) {
		return invalidReviewDecision("A corrected score is required for corrected_score and allowed for no other decision.")
	}
	if runes := utf8.RuneCountInString(input.Note); runes < resultReviewNoteMinRunes || runes > resultReviewNoteMaxRunes {
		return invalidReviewDecision("Explain the decision in 10 to 2000 characters.")
	}
	if input.ExpectedVersion < 1 {
		return invalidReviewDecision("expectedVersion must be the version of the review you opened.")
	}
	if len(input.StrikeUserIDs) > resultReviewMaxStrikes || !validUniqueUUIDList(input.StrikeUserIDs) {
		return invalidStrikeUser()
	}
	return nil
}

func invalidReviewDecision(message string) *planRejection {
	return &planRejection{Status: http.StatusBadRequest, Code: "invalid_review_decision", Message: message}
}

func invalidStrikeUser() *planRejection {
	return &planRejection{Status: http.StatusBadRequest, Code: "invalid_strike_user",
		Message: "Strikes can only be recorded against a player this decision proves wrong."}
}

// lockedReview is the match_result_reviews row taken FOR UPDATE.
type lockedReview struct {
	ID, MatchID, Status, Reason string
	Version                     int
}

// reviewDecisionOutcome is what a decision changed, for the caller's response
// and post-commit cache invalidation.
type reviewDecisionOutcome struct {
	ReviewID, MatchID, CompetitionID string
	ReviewVersion                    int
	StrikeIDs                        []string
	Result                           finalizedResult
}

// reviewDecisionRejection carries a client-facing refusal out of
// decideResultReviewInTx. Like planRejection, it never depends on anything the
// decider may not see.
type reviewDecisionRejection struct {
	planRejection
}

func (rejection *reviewDecisionRejection) Error() string {
	return "result review decision rejected: " + rejection.Code
}

// planReviewDecision turns a Gamics decision into the match's terminal
// outcome and the strikes to record. Only the side the decision proves wrong
// can be struck: the rejecter when the submitted result stands, the submitter
// when the score is corrected away from it, and either for remove_both.
func planReviewDecision(s verificationState, review lockedReview, actor reviewActor,
	input reviewDecisionInput) (matchResolution, []string, *planRejection) {
	invariant := func(cause error) *planRejection {
		return &planRejection{Status: http.StatusInternalServerError, Code: "internal_error",
			Message: "Unable to decide the review.", Cause: cause}
	}
	if err := actor.validate(); err != nil {
		return matchResolution{}, nil, invariant(err)
	}
	switch {
	case review.Status != "queued":
		return matchResolution{}, nil, &planRejection{Status: http.StatusConflict, Code: "review_already_decided",
			Message: "This review has already been decided or closed."}
	case review.Version != input.ExpectedVersion:
		return matchResolution{}, nil, &planRejection{Status: http.StatusConflict, Code: "review_version_conflict",
			Message: "This review changed since you opened it. Refresh it and decide again."}
	case actor.Kind == "system" && input.Decision == "corrected_score":
		return matchResolution{}, nil, invalidReviewDecision("An automated decision cannot correct the score.")
	case actor.Kind == "system" && len(input.StrikeUserIDs) > 0:
		return matchResolution{}, nil, &planRejection{Status: http.StatusBadRequest, Code: "invalid_strike_user",
			Message: "An automated decision cannot record strikes."}
	}
	submitted := s.submittedReport()
	if err := reviewStateError(s, review, submitted); err != nil {
		return matchResolution{}, nil, invariant(err)
	}
	submitter, rejecter := submitted.ReportedBy, *s.Verification.RejectedBy
	submittedSide := "away"
	if sameOptionalString(&submitted.EntryID, s.Match.HomeEntryID) {
		submittedSide = "home"
	}
	var resolution matchResolution
	var wrong []string
	switch input.Decision {
	case "accept_home", "accept_away":
		if input.Decision != "accept_"+submittedSide {
			return matchResolution{}, nil, invalidReviewDecision(
				"Only the submitted result can be accepted. Enter a corrected score instead.")
		}
		resolution = s.reviewedResult(submitted.Claim, submitter, actor)
		wrong = []string{rejecter}
	case "corrected_score":
		if input.CorrectedScore == nil {
			return matchResolution{}, nil, invalidReviewDecision("A corrected score is required for corrected_score.")
		}
		claim, message := normalizeScoreClaim(input.CorrectedScore.claimInput(), s.Match.BestOf, s.Match.StageFormat)
		if message != "" {
			return matchResolution{}, nil, &planRejection{Status: http.StatusBadRequest, Code: "invalid_score", Message: message}
		}
		resolution = s.reviewedResult(claim, *actor.UserID, actor)
		wrong = []string{submitter}
		if claim.equal(submitted.Claim) {
			wrong = []string{rejecter}
		}
	case "remove_both":
		resolution = s.reviewRemoval()
		wrong = []string{submitter, rejecter}
	default:
		return matchResolution{}, nil, invalidReviewDecision("Choose accept_home, accept_away, corrected_score or remove_both.")
	}
	strikes := slices.Clone(input.StrikeUserIDs)
	for _, userID := range strikes {
		if !slices.Contains(wrong, userID) {
			return matchResolution{}, nil, invalidStrikeUser()
		}
	}
	slices.Sort(strikes)
	return resolution, strikes, nil
}

// reviewStateError is the review planner's invariant: the locked state is
// consistent and is the review's disputed match, in review with a submitted
// and rejected result.
func reviewStateError(s verificationState, review lockedReview, submitted *verificationReport) error {
	if err := s.consistencyError(); err != nil {
		return err
	}
	if review.MatchID != s.Match.ID || s.Match.State != "disputed" || s.Verification == nil ||
		s.Verification.Phase != "in_review" || submitted == nil || s.Verification.RejectedBy == nil {
		return fmt.Errorf("%w: the review's match is not disputed and in review with a rejected result",
			errResultVerificationInvariant)
	}
	return nil
}

// reviewedResult confirms the decided score (T13). The accepted claim's
// reporter, or the staff member who corrected it, authors the canonical row;
// the decider confirms it, and a system decider leaves decided_by empty.
func (s verificationState) reviewedResult(decided scoreClaim, authorID string, actor reviewActor) matchResolution {
	claim := decided
	claim.Games = slices.Clone(decided.Games)
	var winner *string
	switch homeWon, awayWon := resultWinner(claim.HomeScore, claim.AwayScore, claim.Tiebreak); {
	case homeWon:
		winner = cloneOptionalString(s.Match.HomeEntryID)
	case awayWon:
		winner = cloneOptionalString(s.Match.AwayEntryID)
	}
	return matchResolution{
		FinalState: "completed", WinnerEntryID: winner, CompletionReason: "platform_review",
		Cause: progressionCausePlatformReview, Claim: &claim, ClaimAuthorID: &authorID,
		ConfirmerID: cloneOptionalString(actor.UserID), Origin: "platform_review", RemoveEntryIDs: []string{},
		Resolution: "platform_review", ApplyRatings: true,
	}
}

// reviewRemoval removes both entries from the tournament (T14). Like every
// removal, it never touches ratings.
func (s verificationState) reviewRemoval() matchResolution {
	removed := s.Match.entryIDs()
	slices.Sort(removed)
	return matchResolution{
		FinalState: "cancelled", CompletionReason: "platform_review", Cause: progressionCausePlatformReview,
		RemoveEntryIDs: removed, RemovalReason: "platform_review", Resolution: "platform_review",
	}
}

// decideResultReviewInTx is the only path that decides a Gamics review, and it
// owns the mandatory lock order: review lookup, competition gate, match,
// verification, review, reports. A future system decider calls exactly this
// function inside its own transaction. Any error returns before the caller
// commits, so the deferred rollback discards every write.
func decideResultReviewInTx(ctx context.Context, tx pgx.Tx, reviewID string, actor reviewActor,
	input reviewDecisionInput) (reviewDecisionOutcome, error) {
	if err := actor.validate(); err != nil {
		return reviewDecisionOutcome{}, err
	}
	input.StrikeUserIDs = slices.Clone(input.StrikeUserIDs)
	if rejection := input.normalize(); rejection != nil {
		return reviewDecisionOutcome{}, &reviewDecisionRejection{*rejection}
	}
	state, review, err := lockResultReviewState(ctx, tx, reviewID)
	if err != nil {
		return reviewDecisionOutcome{}, err
	}
	if actor.Kind == "staff" {
		conflicted, conflictErr := resultReviewConflicted(ctx, tx, state.Match.ID, *actor.UserID)
		if conflictErr != nil {
			return reviewDecisionOutcome{}, conflictErr
		}
		if conflicted {
			return reviewDecisionOutcome{}, errResultReviewConflict
		}
	}
	resolution, strikeUserIDs, rejection := planReviewDecision(state, review, actor, input)
	if rejection != nil {
		return reviewDecisionOutcome{}, &reviewDecisionRejection{*rejection}
	}
	decider := actor.resolutionActor()
	outcome := reviewDecisionOutcome{ReviewID: review.ID, MatchID: state.Match.ID, CompetitionID: state.Match.CompetitionID}
	if outcome.Result, err = finalizeMatchResolution(ctx, tx, state.Match, state.Verification, resolution, decider, false); err != nil {
		return reviewDecisionOutcome{}, err
	}
	if outcome.ReviewVersion, err = markResultReviewDecided(ctx, tx, state.Match, review, actor, input, resolution); err != nil {
		return reviewDecisionOutcome{}, err
	}
	if outcome.StrikeIDs, err = recordPlayerStrikes(ctx, tx, state.Match, review.ID, decider, strikeUserIDs, input.Note); err != nil {
		return reviewDecisionOutcome{}, err
	}
	if err = appendPlatformAuditContext(ctx, tx, decider.RequestID, decider.UserID, "result.review_decided",
		"match_result_review", review.ID, map[string]any{"status": review.Status, "version": review.Version},
		map[string]any{
			"status": "decided", "version": outcome.ReviewVersion, "decision": input.Decision, "reason": review.Reason,
			"deciderKind": actor.Kind, "deciderRef": nullableString(actor.DeciderRef), "note": input.Note,
			"matchId": state.Match.ID, "matchVersion": outcome.Result.MatchVersion,
			"removedEntryIds": outcome.Result.RemovedEntryIDs, "strikeIds": outcome.StrikeIDs,
		}); err != nil {
		return reviewDecisionOutcome{}, err
	}
	// Identical for both entries and free of the reason and the score; removed
	// entries hear from competition.entry_removed instead.
	if err = insertProgressionOutbox(ctx, tx, "match", state.Match.ID, "result.review_decided", map[string]any{
		"matchId": state.Match.ID, "competitionId": state.Match.CompetitionID,
		"matchVersion": outcome.Result.MatchVersion, "reviewId": review.ID,
	}); err != nil {
		return reviewDecisionOutcome{}, err
	}
	return outcome, nil
}

// lockResultReviewState takes the decision's locks in the mandatory order. The
// review is looked up without a lock first, only to name the gate.
func lockResultReviewState(ctx context.Context, tx pgx.Tx, reviewID string) (verificationState, lockedReview, error) {
	var matchID, competitionID string
	err := tx.QueryRow(ctx, `SELECT match_id::text,competition_id::text FROM match_result_reviews WHERE id=$1`,
		reviewID).Scan(&matchID, &competitionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return verificationState{}, lockedReview{}, errResultReviewNotFound
	}
	if err != nil {
		return verificationState{}, lockedReview{}, err
	}
	if err = lockCompetitionProgressionGate(ctx, tx, competitionID); err != nil {
		return verificationState{}, lockedReview{}, err
	}
	var state verificationState
	state.Match, err = lockResultMatch(ctx, tx, matchID, competitionID, "")
	if errors.Is(err, pgx.ErrNoRows) {
		return verificationState{}, lockedReview{}, errResultReviewNotFound
	}
	if err != nil {
		return verificationState{}, lockedReview{}, err
	}
	if state.Match.competitionClosed() {
		return verificationState{}, lockedReview{}, errCompetitionClosed
	}
	if state.Verification, err = lockResultVerification(ctx, tx, matchID); err != nil {
		return verificationState{}, lockedReview{}, err
	}
	var review lockedReview
	err = tx.QueryRow(ctx, `SELECT id::text,match_id::text,status,reason,version
		FROM match_result_reviews WHERE id=$1 FOR UPDATE`, reviewID).Scan(
		&review.ID, &review.MatchID, &review.Status, &review.Reason, &review.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return verificationState{}, lockedReview{}, errResultReviewNotFound
	}
	if err != nil {
		return verificationState{}, lockedReview{}, err
	}
	if state.Reports, err = lockResultReports(ctx, tx, matchID); err != nil {
		return verificationState{}, lockedReview{}, err
	}
	return state, review, nil
}

// resultReviewConflicted re-checks, under the locks, that the staff member
// neither plays in, captains in nor organizes the match (D27).
func resultReviewConflicted(ctx context.Context, tx pgx.Tx, matchID, userID string) (bool, error) {
	var conflicted bool
	err := tx.QueryRow(ctx, `SELECT `+resultReviewConflictClause("m", "$2")+` FROM matches m WHERE m.id=$1`,
		matchID, userID).Scan(&conflicted)
	return conflicted, err
}

// markResultReviewDecided records the decision on the locked review. It must
// affect exactly the queued version the plan was made from.
func markResultReviewDecided(ctx context.Context, tx pgx.Tx, m lockedResultMatch, review lockedReview, actor reviewActor,
	input reviewDecisionInput, resolution matchResolution) (int, error) {
	var homeScore, awayScore, homeTiebreak, awayTiebreak *int
	var tiebreakType *string
	var games any
	if input.Decision == "corrected_score" {
		raw, err := json.Marshal(resolution.Claim.Games)
		if err != nil {
			return 0, err
		}
		homeScore, awayScore, games = &resolution.Claim.HomeScore, &resolution.Claim.AwayScore, json.RawMessage(raw)
		tiebreakType, homeTiebreak, awayTiebreak = resolution.Claim.tiebreakColumns()
	}
	return updateResultVersion(ctx, tx, `UPDATE match_result_reviews SET status='decided',decision=$3,
		corrected_home_score=$4,corrected_away_score=$5,corrected_tiebreak_type=$6,corrected_home_tiebreak_score=$7,
		corrected_away_tiebreak_score=$8,corrected_game_results=$9,note=$10,decider_kind=$11,decided_by=$12,
		decider_ref=$13,decided_at=$14,version=version+1,updated_at=now()
		WHERE id=$1 AND status='queued' AND version=$2 RETURNING version`,
		review.ID, review.Version, input.Decision, homeScore, awayScore, tiebreakType, homeTiebreak, awayTiebreak,
		games, input.Note, actor.Kind, actor.UserID, nullableString(actor.DeciderRef), m.DatabaseNow)
}

// recordPlayerStrikes records a conduct strike against each liar the staff
// decider named, after progression and ratings. Each strike is audited and
// pushed to the player; the push carries no match detail beyond identifiers.
func recordPlayerStrikes(ctx context.Context, tx pgx.Tx, m lockedResultMatch, reviewID string, decider resolutionActor,
	userIDs []string, note string) ([]string, error) {
	strikeIDs := make([]string, 0, len(userIDs))
	for _, userID := range userIDs {
		var strikeID string
		err := tx.QueryRow(ctx, `INSERT INTO player_strikes
			(user_id,match_id,review_id,reason_code,note,created_by_kind,created_by,created_at)
			VALUES ($1,$2,$3,$4,$5,'staff',$6,$7) ON CONFLICT (review_id,user_id) DO NOTHING RETURNING id::text`,
			userID, m.ID, reviewID, resultReviewStrikeReason, note, decider.UserID, m.DatabaseNow).Scan(&strikeID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err = appendPlatformAuditContext(ctx, tx, decider.RequestID, decider.UserID, "player.strike_recorded",
			"player_strike", strikeID, nil, map[string]any{
				"userId": userID, "matchId": m.ID, "reviewId": reviewID, "reasonCode": resultReviewStrikeReason,
			}); err != nil {
			return nil, err
		}
		if err = insertProgressionOutbox(ctx, tx, "user", userID, "player.strike_recorded", map[string]any{
			"strikeId": strikeID, "userId": userID, "matchId": m.ID, "reviewId": reviewID,
		}); err != nil {
			return nil, err
		}
		strikeIDs = append(strikeIDs, strikeID)
	}
	return strikeIDs, nil
}
