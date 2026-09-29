package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/username"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A display name is chosen by the player during onboarding. Until then it
// mirrors the handle, and it is never derived from the email address.

const (
	displayNameMigration = "000023_display_name_onboarding"
	testSignupUserID     = "77777777-7777-4777-8777-777777777777"
)

func TestOnboardingRequiresAChosenDisplayName(t *testing.T) {
	cases := []struct {
		name                                               string
		personalDetails, displayName, profile, gameAccount bool
		complete                                           bool
	}{
		{"every step done", true, true, true, true, true},
		{"display name still mirrors the handle", true, false, true, true, false},
		{"personal details missing", false, true, true, true, false},
		{"no profile", true, true, false, true, false},
		{"no game account", true, true, true, false, false},
		{"fresh signup", false, false, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newOnboardingStatus(tc.personalDetails, tc.displayName, tc.profile, tc.gameAccount)
			want := onboardingStatus{PersonalDetails: tc.personalDetails, DisplayName: tc.displayName,
				Profile: tc.profile, GameAccount: tc.gameAccount, Complete: tc.complete}
			if got != want {
				t.Fatalf("onboarding = %+v, want %+v", got, want)
			}
		})
	}
	raw, err := json.Marshal(newOnboardingStatus(true, false, true, true))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"personalDetails":true,"displayName":false,"profile":true,"gameAccount":true,"complete":false}`; string(raw) != want {
		t.Fatalf("onboarding JSON = %s, want %s", raw, want)
	}
}

// signupCaptureTx answers the signup statements without a database. The user
// insert returns testSignupUserID, and the first taken handle claims find the
// handle already in use.
type signupCaptureTx struct {
	pgx.Tx
	taken       int
	userArgs    []any
	claimedArgs [][]any
	execArgs    [][]any
}

type signupCaptureRow struct {
	value string
	err   error
}

func (row signupCaptureRow) Scan(dest ...any) error {
	if row.err != nil {
		return row.err
	}
	*dest[0].(*string) = row.value
	return nil
}

func (tx *signupCaptureTx) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	if strings.HasPrefix(query, "INSERT INTO users") {
		tx.userArgs = args
		return signupCaptureRow{value: testSignupUserID}
	}
	tx.claimedArgs = append(tx.claimedArgs, args)
	if len(tx.claimedArgs) <= tx.taken {
		return signupCaptureRow{err: pgx.ErrNoRows}
	}
	return signupCaptureRow{value: args[1].(string)}
}

func (tx *signupCaptureTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	tx.execArgs = append(tx.execArgs, args)
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func TestSignupDefaultsTheDisplayNameToTheGeneratedHandle(t *testing.T) {
	const email = "brian.otieno@example.com"
	for _, taken := range []int{0, 2} {
		tx := &signupCaptureTx{taken: taken}
		userID, err := createPlayerAccount(t.Context(), tx, email, rand.Reader)
		if err != nil || userID != testSignupUserID {
			t.Fatalf("taken=%d: createPlayerAccount = %q, %v", taken, userID, err)
		}
		if len(tx.userArgs) != 2 || tx.userArgs[0] != email {
			t.Fatalf("taken=%d: user insert args = %v", taken, tx.userArgs)
		}
		inserted, _ := tx.userArgs[1].(string)
		claimed, _ := tx.claimedArgs[taken][1].(string)
		if !username.Valid(inserted) || !username.Valid(claimed) || strings.Contains(strings.ToLower(inserted), "brian") {
			t.Fatalf("taken=%d: display name %q / handle %q are not generated handles", taken, inserted, claimed)
		}
		displayName := inserted
		if taken > 0 {
			if len(tx.execArgs) != 1 || tx.execArgs[0][0] != testSignupUserID {
				t.Fatalf("taken=%d: display name was not moved to the claimed handle: %v", taken, tx.execArgs)
			}
			displayName, _ = tx.execArgs[0][1].(string)
		} else if len(tx.execArgs) != 0 {
			t.Fatalf("taken=0: unexpected update %v", tx.execArgs)
		}
		if displayName != claimed {
			t.Fatalf("taken=%d: display name %q, want the claimed handle %q", taken, displayName, claimed)
		}
	}
	tx := &signupCaptureTx{taken: signupHandleAttempts}
	if _, err := createPlayerAccount(t.Context(), tx, email, rand.Reader); !errors.Is(err, errHandleUnavailable) {
		t.Fatalf("every handle taken = %v, want errHandleUnavailable", err)
	}
	if len(tx.claimedArgs) != signupHandleAttempts {
		t.Fatalf("tried %d handles, want %d", len(tx.claimedArgs), signupHandleAttempts)
	}
}

func TestDisplayNameIsNeverDerivedFromTheEmail(t *testing.T) {
	assertFileOmits(t, "auth_handlers.go", "provisionalDisplayName", `SplitN(email, "@"`)
	assertFileOrder(t, "auth_handlers.go", "func (s *Server) verifyOTP",
		"userID, err = createPlayerAccount(r.Context(), tx, email, rand.Reader)",
		`writeError(w, http.StatusServiceUnavailable, "handle_unavailable"`)
	assertFileOrder(t, "account_handlers.go", "func (s *Server) patchMe",
		`appendValue("display_name", value)`, `assignments = append(assignments, "display_name_set_at=now()")`,
		"func (s *Server) putProfile",
		"SELECT true FROM users WHERE id=$1 AND status='active' FOR NO KEY UPDATE`, userID)",
		"INSERT INTO player_profiles",
		"UPDATE users SET display_name=$2,updated_at=now()\n\t\tWHERE id=$1 AND display_name_set_at IS NULL`, userID, input.Handle)",
		"func (s *Server) loadMe", "u.display_name_set_at IS NOT NULL")
	// Free and paid entry share the onboarding gate, display name included,
	// and the eligibility preflight reports it as profile_incomplete.
	for _, path := range []string{"competition_handlers.go", "payment_handlers.go"} {
		assertFileContains(t, path,
			"AND player.privacy_accepted_at IS NOT NULL AND profile.user_id IS NOT NULL AND player.display_name_set_at IS NOT NULL)")
	}
	assertFileOrder(t, "competition_eligibility.go", "func queryCompetitionEligibilityFacts",
		"(profile.user_id IS NOT NULL AND player.birth_date IS NOT NULL",
		"AND player.display_name_set_at IS NOT NULL),", "rating.rating")
}

func TestDisplayNameMigrationShape(t *testing.T) {
	up := filepath.Join("..", "..", "migrations", displayNameMigration+".up.sql")
	down := filepath.Join("..", "..", "migrations", displayNameMigration+".down.sql")
	for _, path := range []string{up, down} {
		contents := strings.TrimSpace(readSourceFile(t, path))
		if !strings.HasPrefix(contents, "BEGIN;") || !strings.HasSuffix(contents, "COMMIT;") {
			t.Errorf("%s must be wrapped in BEGIN; ... COMMIT;", path)
		}
	}
	assertFileOrder(t, up, "ALTER TABLE users ADD COLUMN display_name_set_at timestamptz;",
		"UPDATE users SET display_name_set_at=now()\nWHERE email IS NULL OR lower(display_name) NOT IN (",
		"lower(split_part(email,'@',1)), lower(left(split_part(email,'@',1),64)));",
		"(SELECT profile.handle FROM player_profiles profile WHERE profile.user_id=player.id),'Player')",
		"WHERE player.display_name_set_at IS NULL;")
	assertFileContains(t, down, "ALTER TABLE users DROP COLUMN IF EXISTS display_name_set_at;")
}

type displayNamePlayer struct {
	DisplayName string `json:"displayName"`
	Profile     *struct {
		Handle string `json:"handle"`
	} `json:"profile"`
	Onboarding onboardingStatus `json:"onboarding"`
}

// TestIntegrationSignupRequiresAChosenDisplayName signs a new player up and
// walks onboarding: every other step finished still leaves it incomplete
// until the player chooses a display name.
func TestIntegrationSignupRequiresAChosenDisplayName(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	const otpSecret, code = "display-name-otp-secret", "123456"
	server := &Server{db: &database.Cluster{Writer: pool, Reader: pool},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		tokens: gamicsauth.NewTokenManager("display-name-access-secret", time.Minute),
		config: config.Config{OTPHashSecret: otpSecret, OTPMaxAttempts: 5}}
	localPart := "brian.otieno." + strings.ToLower(rand.Text())[:8]
	email := localPart + "@gamics.test"
	if _, err := pool.Exec(t.Context(), `INSERT INTO email_otp_challenges(email,code_hash,request_ip,expires_at)
		VALUES ($1,$2,'127.0.0.1',now()+interval '10 minutes')`, email, gamicsauth.HashOTP(otpSecret, email, code)); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.verifyOTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/auth/otp/verify",
		strings.NewReader(`{"email":"`+email+`","code":"`+code+`"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("signup = %d %s", recorder.Code, recorder.Body)
	}
	var session struct {
		Player displayNamePlayer `json:"player"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	signedUp := session.Player
	if signedUp.Profile == nil || signedUp.DisplayName != signedUp.Profile.Handle ||
		strings.Contains(strings.ToLower(signedUp.DisplayName), "brian") || signedUp.Onboarding.DisplayName ||
		!signedUp.Onboarding.Profile || signedUp.Onboarding.Complete {
		t.Fatalf("new player = %+v, want a pending display name equal to the generated handle", signedUp)
	}
	var userID string
	if err := pool.QueryRow(t.Context(), `SELECT id::text FROM users WHERE lower(email)=lower($1)`, email).
		Scan(&userID); err != nil {
		t.Fatal(err)
	}

	// Every other step is finished; onboarding still waits for the name.
	if _, err := pool.Exec(t.Context(), `UPDATE users SET birth_date='2000-01-01',terms_accepted_at=now(),
		privacy_accepted_at=now() WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO game_accounts(user_id,game_id,platform,in_game_name)
		VALUES ($1,'efootball-mobile','android','BrianO')`, userID); err != nil {
		t.Fatal(err)
	}
	pending := displayNameIntegrationCall(t, server.getMe, http.MethodGet, "/v1/me", "", userID)
	if !pending.Onboarding.PersonalDetails || !pending.Onboarding.GameAccount || pending.Onboarding.DisplayName ||
		pending.Onboarding.Complete {
		t.Fatalf("onboarding before a display name = %+v", pending.Onboarding)
	}

	// While the name is pending it follows the handle.
	renamed := displayNameIntegrationCall(t, server.putProfile, http.MethodPut, "/v1/me/profile",
		`{"handle":"Brian_`+localPart[13:]+`","bio":"","discoverable":false,"analyticsConsent":false,"scoutingConsent":false}`, userID)
	if renamed.Profile == nil || renamed.DisplayName != renamed.Profile.Handle || renamed.Onboarding.Complete {
		t.Fatalf("handle change while pending = %+v", renamed)
	}

	// Choosing a name completes onboarding, and later handle changes keep it.
	named := displayNameIntegrationCall(t, server.patchMe, http.MethodPatch, "/v1/me", `{"displayName":"Brian O"}`, userID)
	if named.DisplayName != "Brian O" || !named.Onboarding.DisplayName || !named.Onboarding.Complete {
		t.Fatalf("after choosing a display name = %+v", named)
	}
	kept := displayNameIntegrationCall(t, server.putProfile, http.MethodPut, "/v1/me/profile",
		`{"handle":"BrianO_`+localPart[13:]+`","bio":"","discoverable":false,"analyticsConsent":false,"scoutingConsent":false}`, userID)
	if kept.DisplayName != "Brian O" || !kept.Onboarding.Complete {
		t.Fatalf("handle change after choosing a name = %+v", kept)
	}
}

// TestIntegrationDisplayNameMigration upgrades players whose name was copied
// from their email, then rolls the migration back and forward again.
func TestIntegrationDisplayNameMigration(t *testing.T) {
	pool := openIntegrationSchema(t)
	applyEmbeddedMigrationsBefore(t, pool, displayNameMigration)
	suffix := strings.ToLower(rand.Text())[:8]
	withHandle := displayNameIntegrationUser(t, pool, "brian."+suffix+"@gamics.test", "brian."+suffix)
	if _, err := pool.Exec(t.Context(), `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`,
		withHandle, "Swift_Falcon_"+suffix[:4]); err != nil {
		t.Fatal(err)
	}
	withoutHandle := displayNameIntegrationUser(t, pool, "wanjiku."+suffix+"@gamics.test", "Wanjiku."+suffix)
	chosen := displayNameIntegrationUser(t, pool, "kamau."+suffix+"@gamics.test", "Kamau the Great")
	raw := readSourceFile(t, filepath.Join("..", "..", "migrations", displayNameMigration+".up.sql"))
	if err := execMigrationSQL(t.Context(), pool, raw); err != nil {
		t.Fatalf("apply %s: %v", displayNameMigration, err)
	}
	for userID, want := range map[string]struct {
		name string
		set  bool
	}{
		withHandle:    {"Swift_Falcon_" + suffix[:4], false},
		withoutHandle: {"Player", false},
		chosen:        {"Kamau the Great", true},
	} {
		var name string
		var set bool
		if err := pool.QueryRow(t.Context(), `SELECT display_name,display_name_set_at IS NOT NULL FROM users WHERE id=$1`,
			userID).Scan(&name, &set); err != nil {
			t.Fatal(err)
		}
		if name != want.name || set != want.set {
			t.Errorf("user %s = %q set=%v, want %q set=%v", userID, name, set, want.name, want.set)
		}
	}
	for _, direction := range []string{"down", "up"} {
		raw = readSourceFile(t, filepath.Join("..", "..", "migrations", displayNameMigration+"."+direction+".sql"))
		if err := execMigrationSQL(t.Context(), pool, raw); err != nil {
			t.Fatalf("apply %s %s: %v", displayNameMigration, direction, err)
		}
	}
}

func displayNameIntegrationUser(t *testing.T, pool *pgxpool.Pool, email, displayName string) string {
	t.Helper()
	var userID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,display_name,status) VALUES ($1,$2,'active')
		RETURNING id::text`, email, displayName).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return userID
}

// displayNameIntegrationCall runs an authenticated /v1/me handler and decodes
// the player it returns.
func displayNameIntegrationCall(t *testing.T, handler http.HandlerFunc, method, target, body, userID string) displayNamePlayer {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s %s = %d %s", method, target, recorder.Code, recorder.Body)
	}
	var player displayNamePlayer
	if err := json.Unmarshal(recorder.Body.Bytes(), &player); err != nil {
		t.Fatal(err)
	}
	return player
}
