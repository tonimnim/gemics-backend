package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var handlePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.]{2,23}$`)
var countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)

type mePatch struct {
	DisplayName   *string `json:"displayName"`
	CountryCode   *string `json:"countryCode"`
	BirthDate     *string `json:"birthDate"`
	AcceptTerms   *bool   `json:"acceptTerms"`
	AcceptPrivacy *bool   `json:"acceptPrivacy"`
}

type profilePut struct {
	Handle           string `json:"handle"`
	Bio              string `json:"bio"`
	Discoverable     bool   `json:"discoverable"`
	AnalyticsConsent bool   `json:"analyticsConsent"`
	ScoutingConsent  bool   `json:"scoutingConsent"`
}

type gameAccountInput struct {
	GameID            string  `json:"gameId"`
	Platform          string  `json:"platform"`
	InGameName        string  `json:"inGameName"`
	PublisherPlayerID *string `json:"publisherPlayerId"`
}

type gameAccountPatch struct {
	Platform          *string `json:"platform"`
	InGameName        *string `json:"inGameName"`
	PublisherPlayerID *string `json:"publisherPlayerId"`
}

type gameAccount struct {
	ID                 string     `json:"id"`
	GameID             string     `json:"gameId"`
	Platform           string     `json:"platform"`
	InGameName         string     `json:"inGameName"`
	PublisherPlayerID  *string    `json:"publisherPlayerId"`
	VerificationStatus string     `json:"verificationStatus"`
	VerificationMethod *string    `json:"verificationMethod"`
	PublisherVerified  bool       `json:"publisherVerified"`
	VerifiedAt         *time.Time `json:"verifiedAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

func (s *Server) getMe(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	result, err := s.loadMe(r.Context(), s.db.Writer, identityFromContext(r.Context()).UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the player account.")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input mePatch
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.AcceptTerms != nil || input.AcceptPrivacy != nil {
		writeError(w, http.StatusBadRequest, "versioned_legal_acceptance_required",
			"Accept the current terms and privacy versions through POST /v1/me/legal-acceptances.")
		return
	}
	assignments := make([]string, 0, 6)
	args := []any{identityFromContext(r.Context()).UserID}
	appendValue := func(column string, value any) {
		args = append(args, value)
		assignments = append(assignments, column+"=$"+itoa(len(args)))
	}
	if input.DisplayName != nil {
		value := strings.TrimSpace(*input.DisplayName)
		if len(value) < 2 || len(value) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_display_name", "Display name must be between 2 and 80 characters.")
			return
		}
		appendValue("display_name", value)
	}
	if input.CountryCode != nil {
		value := strings.ToUpper(strings.TrimSpace(*input.CountryCode))
		if !countryPattern.MatchString(value) {
			writeError(w, http.StatusBadRequest, "invalid_country", "Country code must be a two-letter ISO code.")
			return
		}
		appendValue("country_code", value)
		assignments = append(assignments, "country_chosen_at=now()")
	}
	if input.BirthDate != nil {
		value, err := time.Parse("2006-01-02", *input.BirthDate)
		if err != nil || !value.Before(time.Now().UTC()) {
			writeError(w, http.StatusBadRequest, "invalid_birth_date", "Birth date must be a valid past date.")
			return
		}
		appendValue("birth_date", value)
	}
	if len(assignments) == 0 {
		writeError(w, http.StatusBadRequest, "empty_update", "Provide at least one field to update.")
		return
	}
	query := "UPDATE users SET " + strings.Join(assignments, ",") + ",updated_at=now() WHERE id=$1"
	if _, err := s.db.Writer.Exec(r.Context(), query, args...); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the player account.")
		return
	}
	s.getMe(w, r)
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input profilePut
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Handle = strings.TrimSpace(input.Handle)
	input.Bio = strings.TrimSpace(input.Bio)
	if !handlePattern.MatchString(input.Handle) {
		writeError(w, http.StatusBadRequest, "invalid_handle", "Handle must be 3-24 letters, numbers, underscores or dots.")
		return
	}
	if len(input.Bio) > 280 {
		writeError(w, http.StatusBadRequest, "invalid_bio", "Bio cannot exceed 280 characters.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the player profile.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	// The player row is locked before the profile row, the order account
	// deletion takes them in, so a save racing a deletion waits for it and then
	// finds no active player instead of deadlocking.
	var active bool
	err = tx.QueryRow(r.Context(), `SELECT true FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE`, userID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_session", "The session is no longer valid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the player profile.")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO player_profiles
        (user_id,handle,bio,discoverable,analytics_consent_at,scouting_consent_at)
        VALUES ($1,$2,$3,$4,CASE WHEN $5 THEN now() END,CASE WHEN $6 THEN now() END)
        ON CONFLICT (user_id) DO UPDATE SET
          handle=EXCLUDED.handle,bio=EXCLUDED.bio,discoverable=EXCLUDED.discoverable,
          analytics_consent_at=CASE WHEN $5 THEN COALESCE(player_profiles.analytics_consent_at,now()) END,
          scouting_consent_at=CASE WHEN $6 THEN COALESCE(player_profiles.scouting_consent_at,now()) END,
          updated_at=now()`, userID, input.Handle, input.Bio, input.Discoverable, input.AnalyticsConsent, input.ScoutingConsent)
	if err != nil {
		if strings.Contains(err.Error(), "player_profiles_handle_unique") {
			writeError(w, http.StatusConflict, "handle_taken", "That player handle is already taken.")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the player profile.")
		return
	}
	if tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the player profile.")
		return
	}
	s.getMe(w, r)
}

func (s *Server) listGameAccounts(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	data, err := s.queryGameAccounts(r.Context(), s.db.Reader, userID)
	if err != nil {
		s.logger.Warn("reader query failed; falling back to writer", "operation", "list_game_accounts", "error", err)
		data, err = s.queryGameAccounts(r.Context(), s.db.Writer, userID)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load game accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) createGameAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input gameAccountInput
	if !decodeJSON(w, r, &input) || !validateGameAccountInput(w, input.GameID, input.Platform, input.InGameName) {
		return
	}
	input.GameID = strings.TrimSpace(input.GameID)
	var publisherID any
	if input.PublisherPlayerID != nil && strings.TrimSpace(*input.PublisherPlayerID) != "" {
		publisherID = strings.TrimSpace(*input.PublisherPlayerID)
		if _, _, ok := normalizeKonamiID(*input.PublisherPlayerID); input.GameID == registrationGameID && !ok {
			writeError(w, http.StatusBadRequest, "invalid_konami_id", "Enter the Konami ID shown in eFootball.")
			return
		}
	}
	row := s.db.Writer.QueryRow(r.Context(), `INSERT INTO game_accounts
        (user_id,game_id,platform,in_game_name,publisher_player_id)
        VALUES ($1,$2,$3,$4,$5)
		RETURNING id,game_id,platform,in_game_name,publisher_player_id,verification_status,
		verification_method,publisher_verified,verified_at,created_at,updated_at`,
		identityFromContext(r.Context()).UserID, input.GameID, strings.ToLower(strings.TrimSpace(input.Platform)), strings.TrimSpace(input.InGameName), publisherID)
	account, err := scanGameAccount(row)
	if err != nil {
		if uniqueViolationOn(err, "game_accounts_publisher_id_unique") {
			writeError(w, http.StatusConflict, "game_account_exists", "That publisher player ID is already connected.")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_game_account", "The game account could not be created.")
		return
	}
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) patchGameAccount(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input gameAccountPatch
	if !decodeJSON(w, r, &input) {
		return
	}
	assignments := []string{}
	args := []any{r.PathValue("id"), identityFromContext(r.Context()).UserID}
	appendValue := func(column string, value any) {
		args = append(args, value)
		assignments = append(assignments, column+"=$"+itoa(len(args)))
	}
	if input.Platform != nil {
		value := strings.ToLower(strings.TrimSpace(*input.Platform))
		if value != "android" && value != "ios" {
			writeError(w, http.StatusBadRequest, "invalid_platform", "Platform must be android or ios.")
			return
		}
		appendValue("platform", value)
	}
	if input.InGameName != nil {
		value := strings.TrimSpace(*input.InGameName)
		if len(value) < 2 || len(value) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_in_game_name", "In-game name must be between 2 and 80 characters.")
			return
		}
		appendValue("in_game_name", value)
	}
	if input.PublisherPlayerID != nil {
		value := strings.TrimSpace(*input.PublisherPlayerID)
		if value == "" {
			appendValue("publisher_player_id", nil)
		} else {
			appendValue("publisher_player_id", value)
		}
	}
	if len(assignments) == 0 {
		writeError(w, http.StatusBadRequest, "empty_update", "Provide at least one field to update.")
		return
	}
	assignments = append(assignments, "verification_status='unverified'", "verification_method=NULL",
		"publisher_verified=false", "verified_at=NULL")
	query := `UPDATE game_accounts SET ` + strings.Join(assignments, ",") + `,updated_at=now()
        WHERE id=$1 AND user_id=$2
		RETURNING id,game_id,platform,in_game_name,publisher_player_id,verification_status,
		verification_method,publisher_verified,verified_at,created_at,updated_at`
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "The game account could not be updated.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	account, err := scanGameAccount(tx.QueryRow(r.Context(), query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "game_account_not_found", "Game account not found.")
		return
	}
	if uniqueViolationOn(err, "game_accounts_publisher_id_unique") {
		writeError(w, http.StatusConflict, "game_account_exists", "That publisher player ID is already connected.")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_game_account", "The game account could not be updated.")
		return
	}
	if account.GameID == registrationGameID && !s.keepKonamiSignIn(w, r.Context(), tx, account) {
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE game_account_verification_requests
		SET status='withdrawn',updated_at=now() WHERE game_account_id=$1 AND user_id=$2
		AND status IN ('requested','under_review','approved')`, account.ID, identityFromContext(r.Context()).UserID); err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "The game account could not be updated.")
		return
	}
	writeJSON(w, http.StatusOK, account)
}

// keepKonamiSignIn checks an edited eFootball account: its Konami ID must be
// well formed, and the player must keep at least one Konami ID, because that
// is how they sign in.
func (s *Server) keepKonamiSignIn(w http.ResponseWriter, ctx context.Context, tx pgx.Tx, account gameAccount) bool {
	if account.PublisherPlayerID != nil {
		if _, _, ok := normalizeKonamiID(*account.PublisherPlayerID); !ok {
			writeError(w, http.StatusBadRequest, "invalid_konami_id", "Enter the Konami ID shown in eFootball.")
			return false
		}
	}
	var keeps bool
	err := tx.QueryRow(ctx, `SELECT player.password_hash IS NULL OR EXISTS(SELECT 1 FROM game_accounts account
		WHERE account.user_id=player.id AND account.game_id=$2 AND account.publisher_player_id IS NOT NULL)
		FROM users player WHERE player.id=$1`, identityFromContext(ctx).UserID, registrationGameID).Scan(&keeps)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "The game account could not be updated.")
		return false
	}
	if !keeps {
		writeError(w, http.StatusConflict, "konami_id_required", "Your Konami ID is how you sign in, so it cannot be removed.")
		return false
	}
	return true
}

func (s *Server) loadMe(ctx context.Context, pool *pgxpool.Pool, userID string) (map[string]any, error) {
	var id, displayName, countryCode, status string
	var email, phone, birthDate, konamiID *string
	var emailVerifiedAt, createdAt, updatedAt *time.Time
	var handle, bio *string
	var discoverable *bool
	var analyticsAt, scoutingAt *time.Time
	err := pool.QueryRow(ctx, `SELECT u.id,u.email,u.email_verified_at,u.phone_e164,u.display_name,u.country_code,
        to_char(u.birth_date,'YYYY-MM-DD'),u.status,u.created_at,u.updated_at,
        p.handle,p.bio,p.discoverable,p.analytics_consent_at,p.scouting_consent_at,
        (SELECT ga.publisher_player_id FROM game_accounts ga
         WHERE ga.user_id=u.id AND ga.game_id=$2 AND ga.publisher_player_id IS NOT NULL
         ORDER BY ga.created_at LIMIT 1)
        FROM users u LEFT JOIN player_profiles p ON p.user_id=u.id WHERE u.id=$1`, userID, registrationGameID).
		Scan(&id, &email, &emailVerifiedAt, &phone, &displayName, &countryCode, &birthDate, &status,
			&createdAt, &updatedAt, &handle, &bio, &discoverable, &analyticsAt, &scoutingAt, &konamiID)
	if err != nil {
		return nil, err
	}
	var profile any
	if handle != nil {
		profile = map[string]any{"handle": *handle, "bio": valueOrEmpty(bio), "discoverable": boolOrFalse(discoverable),
			"analyticsConsent": analyticsAt != nil, "scoutingConsent": scoutingAt != nil}
	}
	return map[string]any{
		"id": id, "username": handle, "konamiId": konamiID, "displayName": displayName,
		"email": email, "emailVerified": email != nil && emailVerifiedAt != nil, "phoneNumber": phone,
		"countryCode": countryCode, "birthDate": birthDate,
		"status": status, "createdAt": createdAt, "updatedAt": updatedAt, "profile": profile,
	}, nil
}

func (s *Server) queryGameAccounts(ctx context.Context, pool *pgxpool.Pool, userID string) ([]gameAccount, error) {
	rows, err := pool.Query(ctx, `SELECT id,game_id,platform,in_game_name,publisher_player_id,
		verification_status,verification_method,publisher_verified,verified_at,created_at,updated_at
		FROM game_accounts WHERE user_id=$1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := []gameAccount{}
	for rows.Next() {
		account, err := scanGameAccount(rows)
		if err != nil {
			return nil, err
		}
		data = append(data, account)
	}
	return data, rows.Err()
}

type accountScanner interface{ Scan(...any) error }

func scanGameAccount(row accountScanner) (gameAccount, error) {
	var result gameAccount
	err := row.Scan(&result.ID, &result.GameID, &result.Platform, &result.InGameName, &result.PublisherPlayerID,
		&result.VerificationStatus, &result.VerificationMethod, &result.PublisherVerified,
		&result.VerifiedAt, &result.CreatedAt, &result.UpdatedAt)
	return result, err
}

func validateGameAccountInput(w http.ResponseWriter, gameID, platform, inGameName string) bool {
	if strings.TrimSpace(gameID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_game", "Game ID is required.")
		return false
	}
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform != "android" && platform != "ios" {
		writeError(w, http.StatusBadRequest, "invalid_platform", "Platform must be android or ios.")
		return false
	}
	name := strings.TrimSpace(inGameName)
	if len(name) < 2 || len(name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_in_game_name", "In-game name must be between 2 and 80 characters.")
		return false
	}
	return true
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func boolOrFalse(value *bool) bool { return value != nil && *value }

func itoa(value int) string {
	const digits = "0123456789"
	if value < 10 {
		return string(digits[value])
	}
	return itoa(value/10) + string(digits[value%10])
}
