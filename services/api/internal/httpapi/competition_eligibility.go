package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	eligibilityStatusEligible   = "eligible"
	eligibilityStatusIneligible = "ineligible"
	eligibilityStatusRegistered = "registered"
)

var eligibilityCountryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

// eligibilityQueryer is intentionally satisfied by both pgx.Tx and pgxpool.Pool.
// Registration and payment initiation can therefore run the exact same policy
// decision inside their authoritative write transaction.
type eligibilityQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type competitionEligibilityRules struct {
	MinimumAge                 *int     `json:"minimumAge"`
	AllowedCountries           []string `json:"allowedCountries"`
	MinimumRating              *int     `json:"minimumRating"`
	MaximumRank                *int     `json:"maximumRank"`
	RankingScope               string   `json:"rankingScope,omitempty"`
	RequireVerifiedGameAccount bool     `json:"requireVerifiedGameAccount"`
}

type competitionRulesEnvelope struct {
	Eligibility *competitionEligibilityRules `json:"eligibility"`
}

type eligibilityIssue struct {
	Code     string `json:"code"`
	Category string `json:"category"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type competitionEligibilityResult struct {
	CompetitionID  string                      `json:"competitionId"`
	GameID         string                      `json:"gameId"`
	Status         string                      `json:"status"`
	Eligible       bool                        `json:"eligible"`
	CanRegisterNow bool                        `json:"canRegisterNow"`
	RequiredAction string                      `json:"requiredAction"`
	EntryFeeMinor  int64                       `json:"entryFeeMinor"`
	Currency       string                      `json:"currency"`
	Requirements   competitionEligibilityRules `json:"requirements"`
	Issues         []eligibilityIssue          `json:"issues"`
	// PaymentID is the player's latest payment for the competition, so a
	// client that lost the STK response can resume polling it.
	PaymentID   *string   `json:"paymentId"`
	EvaluatedAt time.Time `json:"evaluatedAt"`
}

type competitionEligibilityFacts struct {
	CompetitionID        string
	GameID               string
	Status               string
	MaxEntries           int
	EntryFeeMinor        int64
	Currency             string
	RegistrationOpensAt  time.Time
	RegistrationClosesAt time.Time
	StartsAt             time.Time
	Rules                []byte
	CountryCode          string
	BirthDate            *time.Time
	ProfileComplete      bool
	Rating               *int
	GlobalRank           *int
	CountryRank          *int
	GameAccountID        *string
	GameAccountGameID    *string
	GameAccountStatus    *string
	EntryID              *string
	EntryStatus          *string
	PaymentID            *string
	PaymentStatus        *string
	OccupiedEntries      int
	ActiveStrikes        int
	StrikeBanThreshold   int
}

var errInvalidEligibilityPolicy = errors.New("competition eligibility policy is invalid")

// activePlayerStrikesSQL counts the active conduct strikes of the row aliased
// "player". The registration preflight and the paid-entry callback share it so
// the ban has one definition.
const activePlayerStrikesSQL = `(SELECT count(*)::integer FROM player_strikes strike
		 WHERE strike.user_id=player.id AND strike.revoked_at IS NULL)`

// conductSuspended applies the strike ban (R8, D14). It blocks new
// registrations only; entries that already exist are never touched by it.
func (facts competitionEligibilityFacts) conductSuspended() bool {
	return strikeBanApplies(facts.ActiveStrikes, facts.StrikeBanThreshold)
}

// strikeBanApplies is true once a player holds threshold active strikes. A
// zero threshold disables the ban.
func strikeBanApplies(activeStrikes, threshold int) bool {
	return threshold > 0 && activeStrikes >= threshold
}

// loadCompetitionEligibility is the shared decision boundary for the preflight,
// free registration and paid-entry initiation. Callers that mutate registration
// state should pass their pgx transaction and still retain their row locks and
// final capacity/idempotency checks: this response is a decision, not a lock.
// strikeBanThreshold is the platform's active-strike limit; 0 disables the ban.
func loadCompetitionEligibility(ctx context.Context, queryer eligibilityQueryer, competitionID, userID, gameAccountID string,
	now time.Time, strikeBanThreshold int) (competitionEligibilityResult, error) {
	facts, err := queryCompetitionEligibilityFacts(ctx, queryer, competitionID, userID, gameAccountID)
	if err != nil {
		return competitionEligibilityResult{}, err
	}
	facts.StrikeBanThreshold = strikeBanThreshold
	policy, err := parseCompetitionEligibilityRules(facts.Rules)
	if err != nil {
		return competitionEligibilityResult{}, err
	}
	return assessCompetitionEligibility(facts, policy, now.UTC()), nil
}

func queryCompetitionEligibilityFacts(ctx context.Context, queryer eligibilityQueryer, competitionID, userID, gameAccountID string) (competitionEligibilityFacts, error) {
	var accountID any
	if gameAccountID != "" {
		accountID = gameAccountID
	}
	var facts competitionEligibilityFacts
	err := queryer.QueryRow(ctx, `SELECT
		competition.id::text,competition.game_id,competition.status,competition.max_entries,
		competition.entry_fee_minor,competition.currency,competition.registration_opens_at,
		competition.registration_closes_at,competition.starts_at,competition.rules_snapshot,
		player.country_code,player.birth_date,
		(profile.user_id IS NOT NULL AND player.birth_date IS NOT NULL
		 AND player.terms_accepted_at IS NOT NULL AND player.privacy_accepted_at IS NOT NULL
		 AND player.display_name_set_at IS NOT NULL),
		rating.rating,global_board.rank,country_board.rank,
		account.id::text,account.game_id,account.verification_status,
		entry.id::text,entry.status,payment.id::text,payment.status,
		(SELECT count(*)::integer FROM competition_entries occupied
		 WHERE occupied.competition_id=competition.id
		   AND occupied.status NOT IN ('withdrawn','disqualified')),
		`+activePlayerStrikesSQL+`
	FROM competitions competition
	JOIN users player ON player.id=$2::uuid AND player.status='active'
	LEFT JOIN player_profiles profile ON profile.user_id=player.id
	LEFT JOIN player_game_ratings rating
	  ON rating.user_id=player.id AND rating.game_id=competition.game_id
	LEFT JOIN game_accounts account
	  ON account.id=$3::uuid AND account.user_id=player.id
	LEFT JOIN competition_entries entry
	  ON entry.competition_id=competition.id AND entry.captain_user_id=player.id
	LEFT JOIN LATERAL (
		SELECT snapshot_row.rank
		FROM leaderboard_snapshots snapshot
		JOIN leaderboard_snapshot_rows snapshot_row
		  ON snapshot_row.snapshot_id=snapshot.id AND snapshot_row.user_id=player.id
		WHERE snapshot.game_id=competition.game_id AND snapshot.scope='global'
		  AND snapshot.country_code IS NULL AND snapshot.status='ready'
		ORDER BY snapshot.snapshot_at DESC,snapshot.id DESC LIMIT 1
	) global_board ON true
	LEFT JOIN LATERAL (
		SELECT snapshot_row.rank
		FROM leaderboard_snapshots snapshot
		JOIN leaderboard_snapshot_rows snapshot_row
		  ON snapshot_row.snapshot_id=snapshot.id AND snapshot_row.user_id=player.id
		WHERE snapshot.game_id=competition.game_id AND snapshot.scope='country'
		  AND snapshot.country_code=player.country_code AND snapshot.status='ready'
		ORDER BY snapshot.snapshot_at DESC,snapshot.id DESC LIMIT 1
	) country_board ON true
	LEFT JOIN LATERAL (
		SELECT intent.id,intent.status
		FROM payment_intents intent
		WHERE intent.competition_id=competition.id AND intent.user_id=player.id
		ORDER BY intent.created_at DESC,intent.id DESC LIMIT 1
	) payment ON true
	WHERE competition.id=$1::uuid AND `+readableCompetitionSQL,
		competitionID, userID, accountID).Scan(
		&facts.CompetitionID, &facts.GameID, &facts.Status, &facts.MaxEntries,
		&facts.EntryFeeMinor, &facts.Currency, &facts.RegistrationOpensAt,
		&facts.RegistrationClosesAt, &facts.StartsAt, &facts.Rules,
		&facts.CountryCode, &facts.BirthDate, &facts.ProfileComplete,
		&facts.Rating, &facts.GlobalRank, &facts.CountryRank,
		&facts.GameAccountID, &facts.GameAccountGameID, &facts.GameAccountStatus,
		&facts.EntryID, &facts.EntryStatus, &facts.PaymentID, &facts.PaymentStatus,
		&facts.OccupiedEntries, &facts.ActiveStrikes,
	)
	return facts, err
}

func parseCompetitionEligibilityRules(raw []byte) (competitionEligibilityRules, error) {
	policy := competitionEligibilityRules{AllowedCountries: []string{}}
	if len(raw) == 0 {
		return policy, nil
	}
	var document competitionRulesEnvelope
	if err := json.Unmarshal(raw, &document); err != nil {
		return competitionEligibilityRules{}, fmt.Errorf("%w: %v", errInvalidEligibilityPolicy, err)
	}
	if document.Eligibility == nil {
		return policy, nil
	}
	policy = *document.Eligibility
	if policy.MinimumAge != nil && (*policy.MinimumAge < 0 || *policy.MinimumAge > 100) {
		return competitionEligibilityRules{}, fmt.Errorf("%w: minimumAge must be between 0 and 100", errInvalidEligibilityPolicy)
	}
	if policy.MinimumRating != nil && (*policy.MinimumRating < 0 || *policy.MinimumRating > 10000) {
		return competitionEligibilityRules{}, fmt.Errorf("%w: minimumRating must be between 0 and 10000", errInvalidEligibilityPolicy)
	}
	if policy.MaximumRank != nil && (*policy.MaximumRank < 1 || *policy.MaximumRank > 10_000_000) {
		return competitionEligibilityRules{}, fmt.Errorf("%w: maximumRank must be positive", errInvalidEligibilityPolicy)
	}
	policy.RankingScope = strings.ToLower(strings.TrimSpace(policy.RankingScope))
	if policy.MaximumRank != nil && policy.RankingScope == "" {
		policy.RankingScope = "global"
	}
	if policy.MaximumRank == nil && policy.RankingScope != "" {
		return competitionEligibilityRules{}, fmt.Errorf("%w: rankingScope requires maximumRank", errInvalidEligibilityPolicy)
	}
	if policy.RankingScope != "" && policy.RankingScope != "global" && policy.RankingScope != "country" {
		return competitionEligibilityRules{}, fmt.Errorf("%w: rankingScope must be global or country", errInvalidEligibilityPolicy)
	}
	countries := make([]string, 0, len(policy.AllowedCountries))
	for _, value := range policy.AllowedCountries {
		country := strings.ToUpper(strings.TrimSpace(value))
		if !eligibilityCountryPattern.MatchString(country) {
			return competitionEligibilityRules{}, fmt.Errorf("%w: allowedCountries contains an invalid country", errInvalidEligibilityPolicy)
		}
		if !slices.Contains(countries, country) {
			countries = append(countries, country)
		}
	}
	if len(countries) > 64 {
		return competitionEligibilityRules{}, fmt.Errorf("%w: too many allowed countries", errInvalidEligibilityPolicy)
	}
	policy.AllowedCountries = countries
	return policy, nil
}

func assessCompetitionEligibility(facts competitionEligibilityFacts, policy competitionEligibilityRules, now time.Time) competitionEligibilityResult {
	result := competitionEligibilityResult{
		CompetitionID: facts.CompetitionID, GameID: facts.GameID,
		Status: eligibilityStatusEligible, Eligible: true, RequiredAction: "none",
		EntryFeeMinor: facts.EntryFeeMinor, Currency: facts.Currency,
		Requirements: policy, Issues: []eligibilityIssue{}, PaymentID: facts.PaymentID, EvaluatedAt: now.UTC(),
	}
	blocking := func(code, category, message string) {
		result.Issues = append(result.Issues, eligibilityIssue{Code: code, Category: category, Severity: "blocking", Message: message})
		result.Eligible = false
		result.Status = eligibilityStatusIneligible
	}
	action := func(code, category, message string) {
		result.Issues = append(result.Issues, eligibilityIssue{Code: code, Category: category, Severity: "action_required", Message: message})
	}

	if facts.EntryStatus != nil && (*facts.EntryStatus == "registered" || *facts.EntryStatus == "checked_in" ||
		*facts.EntryStatus == "accepted" || *facts.EntryStatus == "withdrawal_pending") {
		result.Status = eligibilityStatusRegistered
		result.RequiredAction = "view_registration"
		if *facts.EntryStatus == "withdrawal_pending" {
			result.RequiredAction = "view_refund"
		}
		return result
	}
	if facts.conductSuspended() {
		blocking("conduct_suspended", "conduct", "Your account has too many active conduct strikes to register for new competitions. Contact Gamics support.")
	}
	if !facts.ProfileComplete {
		blocking("profile_incomplete", "profile", "Complete your profile, choose a display name and accept the current terms and privacy notice.")
	}
	if facts.EntryStatus != nil && (*facts.EntryStatus == "withdrawn" || *facts.EntryStatus == "disqualified") {
		blocking("registration_not_reusable", "registration", "This player cannot create another entry for this competition.")
	}
	if facts.Status == "cancelled" {
		blocking("competition_cancelled", "registration", "This competition has been cancelled.")
	} else if facts.Status != "registration_open" || now.Before(facts.RegistrationOpensAt) {
		blocking("registration_not_open", "registration", "Registration is not open yet.")
	} else if !now.Before(facts.RegistrationClosesAt) {
		blocking("registration_closed", "registration", "Registration has closed.")
	}
	if facts.OccupiedEntries >= facts.MaxEntries {
		blocking("competition_full", "capacity", "This competition has no available entries.")
	}
	if policy.MinimumAge != nil {
		if facts.BirthDate == nil {
			blocking("age_required", "age", "Add a birth date before registering.")
		} else if ageOn(*facts.BirthDate, facts.StartsAt) < *policy.MinimumAge {
			blocking("minimum_age_not_met", "age", "The player does not meet this competition's minimum age.")
		}
	}
	if len(policy.AllowedCountries) > 0 && !slices.Contains(policy.AllowedCountries, strings.ToUpper(facts.CountryCode)) {
		blocking("country_not_allowed", "country", "This competition is not open to the player's country.")
	}
	if policy.MinimumRating != nil {
		if facts.Rating == nil {
			blocking("rating_unavailable", "ranking", "The player needs an established rating for this game.")
		} else if *facts.Rating < *policy.MinimumRating {
			blocking("minimum_rating_not_met", "ranking", "The player's rating is below the competition minimum.")
		}
	}
	if policy.MaximumRank != nil {
		rank := facts.GlobalRank
		if policy.RankingScope == "country" {
			rank = facts.CountryRank
		}
		if rank == nil {
			blocking("ranking_unavailable", "ranking", "The player does not have a current eligible leaderboard rank.")
		} else if *rank > *policy.MaximumRank {
			blocking("maximum_rank_not_met", "ranking", "The player's leaderboard rank is outside this competition's qualifying range.")
		}
	}
	if facts.GameAccountID == nil {
		blocking("game_account_required", "game_account", "Choose a connected game account.")
	} else if facts.GameAccountGameID == nil || *facts.GameAccountGameID != facts.GameID {
		blocking("game_account_game_mismatch", "game_account", "Choose a game account for this competition's game.")
	} else if policy.RequireVerifiedGameAccount && (facts.GameAccountStatus == nil || *facts.GameAccountStatus != "verified") {
		blocking("verified_game_account_required", "game_account", "This competition requires a verified game account.")
	}

	if !result.Eligible {
		// An unfinished payment still settles after registration closed or the
		// places filled: it registers the payer or is refunded, so the app keeps
		// polling it whatever now blocks a new entry.
		if paymentUnfinished(facts.PaymentStatus) {
			result.RequiredAction = "poll_payment"
		}
		return result
	}
	if facts.EntryFeeMinor == 0 {
		result.CanRegisterNow = true
		result.RequiredAction = "submit_registration"
		return result
	}
	switch eligibilityString(facts.PaymentStatus) {
	case "initiating", "pending", "callback_received", "review":
		result.RequiredAction = "poll_payment"
		action("payment_pending", "payment", "Payment is still being confirmed.")
	case "succeeded":
		result.RequiredAction = "poll_payment"
		action("payment_processing", "payment", "Payment succeeded and the competition entry is being finalized.")
	case "failed":
		result.RequiredAction = "start_payment"
		action("payment_retry_required", "payment", "The previous payment did not complete. Start a new payment attempt.")
	default:
		result.RequiredAction = "start_payment"
		action("payment_required", "payment", "Complete the administration-fee payment to register.")
	}
	return result
}

func (result competitionEligibilityResult) firstBlockingIssue() *eligibilityIssue {
	for index := range result.Issues {
		if result.Issues[index].Severity == "blocking" {
			return &result.Issues[index]
		}
	}
	return nil
}

func ageOn(birthDate, at time.Time) int {
	birthDate = birthDate.UTC()
	at = at.UTC()
	age := at.Year() - birthDate.Year()
	if at.Month() < birthDate.Month() || (at.Month() == birthDate.Month() && at.Day() < birthDate.Day()) {
		age--
	}
	return age
}

func eligibilityString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// paymentUnfinished reports whether a payment has yet to settle, so the app
// polls it rather than starting another charge.
func paymentUnfinished(status *string) bool {
	switch eligibilityString(status) {
	case "initiating", "pending", "callback_received", "review":
		return true
	}
	return false
}

func (s *Server) getCompetitionEligibility(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	competitionID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(competitionID) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	gameAccountID := strings.TrimSpace(r.URL.Query().Get("gameAccountId"))
	if gameAccountID != "" && !uuidPattern.MatchString(gameAccountID) {
		writeError(w, http.StatusBadRequest, "invalid_game_account", "Choose a valid game account.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	result, err := loadCompetitionEligibility(r.Context(), s.db.Writer, competitionID, userID, gameAccountID, time.Now().UTC(),
		s.config.StrikeBanThreshold)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "competition_not_found", "Competition not found.")
		return
	}
	if errors.Is(err, errInvalidEligibilityPolicy) {
		s.logger.Error("invalid competition eligibility policy", "competition_id", competitionID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "eligibility_policy_invalid", "This competition's eligibility policy is unavailable.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "eligibility_unavailable", "Eligibility cannot be evaluated right now.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}
