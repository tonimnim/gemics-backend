package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/gamics-io/gamics/services/api/internal/username"
	"github.com/jackc/pgx/v5"
)

// Players sign in with Google or Apple. The first sign-in creates the account
// with a generated handle and nothing else; onboarding then adds the Konami ID,
// phone and country in one step (POST /v1/me/onboarding). A Google or Apple
// account whose verified email already belongs to a player signs in to that
// player instead of creating a second one. Staff keep Konami ID and password.

type googleSignInInput struct {
	IDToken    string `json:"idToken"`
	Nonce      string `json:"nonce"`
	DeviceName string `json:"deviceName"`
}

type appleSignInInput struct {
	IdentityToken string `json:"identityToken"`
	Nonce         string `json:"nonce"`
	DeviceName    string `json:"deviceName"`
}

type onboardingInput struct {
	KonamiID    string `json:"konamiId"`
	PhoneNumber string `json:"phoneNumber"`
	CountryCode string `json:"countryCode"`
	// Username is optional: when given it replaces the generated handle.
	Username *string `json:"username"`
}

const maxIdentityTokenLength = 8192

func (s *Server) signInWithGoogle(w http.ResponseWriter, r *http.Request) {
	var input googleSignInInput
	if !decodeJSON(w, r, &input) {
		return
	}
	s.signInWithIdentity(w, r, "google", "Google", input.IDToken, input.Nonce, input.DeviceName)
}

func (s *Server) signInWithApple(w http.ResponseWriter, r *http.Request) {
	var input appleSignInInput
	if !decodeJSON(w, r, &input) {
		return
	}
	s.signInWithIdentity(w, r, "apple", "Apple", input.IdentityToken, input.Nonce, input.DeviceName)
}

// signInWithIdentity answers 201 when it created the account and 200 when the
// player already had one.
func (s *Server) signInWithIdentity(w http.ResponseWriter, r *http.Request, provider, providerLabel, token, nonce, deviceName string) {
	if !s.requireDatabase(w) {
		return
	}
	if !s.identity.Enabled(provider) {
		writeError(w, http.StatusServiceUnavailable, "sign_in_unavailable", "Sign in with "+providerLabel+" isn't available yet.")
		return
	}
	if len(deviceName) > 120 {
		writeError(w, http.StatusBadRequest, "invalid_device_name", "Device name cannot exceed 120 characters.")
		return
	}
	if token == "" || len(token) > maxIdentityTokenLength || len(nonce) > 512 {
		writeError(w, http.StatusUnauthorized, "invalid_identity_token", "Sign-in didn't work. Try again.")
		return
	}
	claims, err := s.identity.Verify(r.Context(), provider, token, nonce)
	if errors.Is(err, gamicsauth.ErrInvalidIdentityToken) {
		writeError(w, http.StatusUnauthorized, "invalid_identity_token", "Sign-in didn't work. Try again.")
		return
	}
	if err != nil {
		s.logger.Warn("verify identity token", "provider", provider, "error", err)
		writeError(w, http.StatusServiceUnavailable, "identity_provider_unavailable",
			"We couldn't reach "+providerLabel+". Try again in a moment.")
		return
	}

	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to sign in.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	email := ""
	if claims.EmailVerified {
		email = strings.ToLower(claims.Email)
	}
	var userID, status string
	err = tx.QueryRow(r.Context(), `SELECT player.id::text,player.status
		FROM auth_identities identity JOIN users player ON player.id=identity.user_id
		WHERE identity.provider=$1 AND identity.subject=$2
		FOR UPDATE OF identity`, provider, claims.Subject).Scan(&userID, &status)
	known := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to sign in.")
		return
	}
	if !known && email != "" {
		// A verified email that is already a player's verified email: same person.
		err = tx.QueryRow(r.Context(), `SELECT id::text,status FROM users
			WHERE lower(email)=$1 AND email_verified_at IS NOT NULL FOR NO KEY UPDATE`, email).Scan(&userID, &status)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to sign in.")
			return
		}
	}
	if userID != "" && status != "active" {
		writeError(w, http.StatusForbidden, "account_unavailable", "This player account is not available.")
		return
	}
	created := false
	if userID == "" {
		ip := s.clientIP(r)
		if !s.allowRegistration(r.Context(), ip) {
			w.Header().Set("Retry-After", retryAfterSeconds(s.config.RegistrationWindow))
			writeError(w, http.StatusTooManyRequests, "rate_limited", "Too many accounts created from this network. Try again later.")
			return
		}
		if userID, err = createIdentityPlayer(r.Context(), tx, claims.GivenName, email, ip); err != nil {
			s.logger.Warn("create identity player", "provider", provider, "error", err)
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the player account.")
			return
		}
		created = true
	}
	if known {
		_, err = tx.Exec(r.Context(), `UPDATE auth_identities SET email=$3,email_verified=$4,last_sign_in_at=now()
			WHERE provider=$1 AND subject=$2`, provider, claims.Subject, nullIfEmpty(claims.Email), claims.EmailVerified)
	} else {
		_, err = tx.Exec(r.Context(), `INSERT INTO auth_identities(user_id,provider,subject,email,email_verified)
			VALUES ($1,$2,$3,$4,$5)`, userID, provider, claims.Subject, nullIfEmpty(claims.Email), claims.EmailVerified)
	}
	if uniqueViolationOn(err, "auth_identities_provider_subject_unique") {
		// The same first sign-in raced from two requests; the other one won.
		writeError(w, http.StatusConflict, "sign_in_conflict", "Sign-in is already in progress. Try again.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to sign in.")
		return
	}
	sessionID, refreshToken, err := s.createSession(r.Context(), tx, r, userID, deviceName)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create a session.")
		return
	}
	s.markRecentWrite(r.Context(), userID)
	statusCode := http.StatusOK
	if created {
		statusCode = http.StatusCreated
	}
	s.writeSessionResponse(w, r, statusCode, userID, sessionID, refreshToken)
}

// createIdentityPlayer creates a player from a Google or Apple sign-in. The
// handle comes from the first name when there is one, otherwise it is
// generated; the provider's name is never stored or shown. A verified email
// is kept unless another account already uses it.
func createIdentityPlayer(ctx context.Context, tx pgx.Tx, givenName, verifiedEmail, ip string) (string, error) {
	var emailTaken bool
	if verifiedEmail != "" {
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=$1)`, verifiedEmail).
			Scan(&emailTaken); err != nil {
			return "", err
		}
	}
	email := verifiedEmail
	if emailTaken {
		email = ""
	}
	candidates, err := handleCandidates(givenName)
	if err != nil {
		return "", err
	}
	var userID string
	if err = tx.QueryRow(ctx, `INSERT INTO users(display_name,email,email_verified_at,registration_ip,status)
		VALUES ($1,NULLIF($2,''),CASE WHEN $2='' THEN NULL ELSE now() END,$3,'active') RETURNING id::text`,
		candidates[0], email, ip).Scan(&userID); err != nil {
		return "", err
	}
	for _, handle := range candidates {
		savepoint, beginErr := tx.Begin(ctx)
		if beginErr != nil {
			return "", beginErr
		}
		_, insertErr := savepoint.Exec(ctx, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`, userID, handle)
		if uniqueViolationOn(insertErr, "player_profiles_handle_unique") {
			if rollbackErr := savepoint.Rollback(ctx); rollbackErr != nil {
				return "", rollbackErr
			}
			continue
		}
		if insertErr != nil {
			return "", insertErr
		}
		if err = savepoint.Commit(ctx); err != nil {
			return "", err
		}
		_, err = tx.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1`, userID, handle)
		return userID, err
	}
	return "", errors.New("no free handle among the candidates")
}

// handleCandidates puts a first-name handle first when the name gives one,
// then generated ones to fall back on.
func handleCandidates(givenName string) ([]string, error) {
	generated, err := username.Candidates(nil, 6)
	if err != nil {
		return nil, err
	}
	if fromName, ok, nameErr := username.FromName(givenName, nil); nameErr == nil && ok {
		return append([]string{fromName}, generated...), nil
	}
	return generated, nil
}

// completeOnboarding adds what a Google or Apple player needs before entering
// competitions: the Konami ID, a phone number and the country, and optionally
// a username to replace the generated handle. The phone is not locked to the
// account until a payment from it succeeds. Finishing also records the
// current terms and privacy notice as accepted, so the onboarding screen must
// show them.
func (s *Server) completeOnboarding(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input onboardingInput
	if !decodeJSON(w, r, &input) {
		return
	}
	konamiID, _, ok := normalizeKonamiID(input.KonamiID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_konami_id", "Enter the Konami ID shown in eFootball.")
		return
	}
	phone, ok := normalizeInternationalPhone(input.PhoneNumber)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_phone",
			"Enter the phone number with its country code, for example +254712345678 or +919812345678.")
		return
	}
	country := strings.ToUpper(strings.TrimSpace(input.CountryCode))
	if !countryPattern.MatchString(country) {
		writeError(w, http.StatusBadRequest, "invalid_country", "Country code must be a two-letter ISO code.")
		return
	}
	chosenName := ""
	if input.Username != nil {
		chosenName = strings.TrimSpace(*input.Username)
		if !handlePattern.MatchString(chosenName) {
			writeError(w, http.StatusBadRequest, "invalid_username", "Username must be 3-24 letters, numbers, underscores or dots.")
			return
		}
	}
	current := identityFromContext(r.Context())
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish setting up the account.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var onboarded bool
	err = tx.QueryRow(r.Context(), `SELECT onboarded_at IS NOT NULL FROM users WHERE id=$1 AND status='active'
		FOR NO KEY UPDATE`, current.UserID).Scan(&onboarded)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_session", "The session is no longer valid.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish setting up the account.")
		return
	}
	if onboarded {
		writeError(w, http.StatusConflict, "already_onboarded", "Your account is already set up. Change these details in your profile.")
		return
	}
	taken, err := phoneProvenElsewhere(r.Context(), tx, phone, current.UserID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish setting up the account.")
		return
	}
	if taken {
		writeError(w, http.StatusConflict, "phone_taken",
			"That phone number is linked to another Ozone account. Contact Ozone support if it is yours.")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO game_accounts(user_id,game_id,in_game_name,publisher_player_id)
		VALUES ($1,$2,'',$3)`, current.UserID, registrationGameID, konamiID)
	if uniqueViolationOn(err, "game_accounts_publisher_id_unique") {
		writeError(w, http.StatusConflict, "konami_id_taken",
			"That Konami ID is already linked to another Ozone account. Contact Ozone support if it is yours.")
		return
	}
	if err == nil && chosenName != "" {
		err = replaceGeneratedHandle(r.Context(), tx, current.UserID, chosenName)
		if uniqueViolationOn(err, "player_profiles_handle_unique") {
			writeError(w, http.StatusConflict, "username_taken", "That username is already taken.")
			return
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE users SET phone_e164=$2,phone_verified_at=NULL,country_code=$3,
			country_chosen_at=now(),onboarded_at=now(),updated_at=now() WHERE id=$1`, current.UserID, phone, country)
	}
	if err == nil {
		err = acceptCurrentLegalDocuments(r.Context(), tx, current.UserID, s.clientIP(r), r.UserAgent())
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish setting up the account.")
		return
	}
	s.getMe(w, r)
}

func nullIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// replaceGeneratedHandle sets the player's chosen username as their handle,
// and as their display name while that still shows the generated handle.
func replaceGeneratedHandle(ctx context.Context, tx pgx.Tx, userID, chosen string) error {
	var previous string
	if err := tx.QueryRow(ctx, `UPDATE player_profiles profile SET handle=$2,updated_at=now()
		FROM player_profiles before WHERE before.user_id=profile.user_id AND profile.user_id=$1
		RETURNING before.handle`, userID, chosen).Scan(&previous); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE users SET display_name=$2 WHERE id=$1 AND display_name=$3`, userID, chosen, previous)
	return err
}

// usernameAvailability tells the onboarding screen, as the player types,
// whether a username can be theirs. The player's own current handle counts as
// available.
func (s *Server) usernameAvailability(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	name := strings.TrimSpace(r.PathValue("username"))
	if !handlePattern.MatchString(name) {
		writeJSON(w, http.StatusOK, map[string]any{"username": name, "available": false, "reason": "invalid"})
		return
	}
	var taken bool
	if err := s.db.Writer.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM player_profiles
		WHERE lower(handle)=lower($1) AND user_id::text<>$2)`, name, identityFromContext(r.Context()).UserID).
		Scan(&taken); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check the username.")
		return
	}
	result := map[string]any{"username": name, "available": !taken, "reason": nil}
	if taken {
		result["reason"] = "taken"
	}
	writeJSON(w, http.StatusOK, result)
}
