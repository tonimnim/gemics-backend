package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A push installation is bound to the session that registered it. These tests
// pin every path that ends a session and the delivery queries that skip an
// installation whose session ended.

const pushTokenSessionMigration = "000022_push_token_sessions"

var sessionRevocationSQL = regexp.MustCompile(`UPDATE refresh_sessions\s+SET revoked_at`)

// sessionEndingFunctions returns the source of every non-test function in this
// package that revokes a refresh session, keyed by function name.
func sessionEndingFunctions(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	functions := map[string]string{}
	fileSet := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		file, parseErr := parser.ParseFile(fileSet, name, source, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			body := string(source[fileSet.Position(function.Body.Pos()).Offset:fileSet.Position(function.Body.End()).Offset])
			if sessionRevocationSQL.MatchString(body) {
				functions[function.Name.Name] = body
			}
		}
	}
	return functions
}

func TestEverySessionEndingPathRevokesItsPushTokens(t *testing.T) {
	functions := sessionEndingFunctions(t)
	for _, required := range []string{"logout", "revokeSession", "executeAccountDeletion"} {
		if _, found := functions[required]; !found {
			t.Errorf("%s no longer revokes a refresh session; update this test", required)
		}
	}
	for name, body := range functions {
		ended := sessionRevocationSQL.FindStringIndex(body)[0]
		revoked := max(strings.Index(body, "revokeSessionPushTokens("), strings.Index(body, "DELETE FROM push_tokens"))
		if revoked < ended {
			t.Errorf("%s ends a session without revoking its push tokens afterwards in the same transaction", name)
		}
		if strings.Contains(body, "s.db.Writer.Exec(r.Context(), `UPDATE refresh_sessions") {
			t.Errorf("%s revokes the session outside the transaction that revokes its push tokens", name)
		}
	}
}

func TestPushTokenRevocationTargetsLiveTokensOfTheSessions(t *testing.T) {
	assertFileContains(t, "push_notification_handlers.go",
		"func revokeSessionPushTokens(ctx context.Context, tx pgx.Tx, sessionIDs []string) error {",
		"UPDATE push_tokens SET revoked_at=now(),updated_at=now()\n\t\tWHERE session_id=ANY($1::text[]::uuid[]) AND revoked_at IS NULL")
}

func TestPushTokenRegistrationBindsTheCallersLiveSession(t *testing.T) {
	// The player row is locked before the session row, matching account
	// deletion, which locks the player FOR UPDATE and then revokes sessions.
	assertFileOrder(t, "push_notification_handlers.go", "func (s *Server) upsertPushToken",
		"WHERE id=$1 AND status='active' FOR KEY SHARE`, current.UserID)",
		"WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL FOR SHARE`, current.SessionID, current.UserID)",
		`writeError(w, http.StatusUnauthorized, "invalid_session"`,
		"pg_advisory_xact_lock(hashtextextended($1, 910310))",
		"WHERE expo_push_token=$1 AND (user_id<>$2 OR device_id<>$3) AND revoked_at IS NULL",
		"(user_id,device_id,expo_push_token,platform,app_version,user_agent,session_id)",
		"session_id=EXCLUDED.session_id,last_seen_at=now(),revoked_at=NULL",
		"func (s *Server) revokePushToken")
	assertFileOrder(t, "account_security_handlers.go", "func (s *Server) executeAccountDeletion",
		"SELECT email FROM users WHERE id=$1 AND status='active' FOR UPDATE",
		"UPDATE refresh_sessions SET revoked_at=COALESCE(revoked_at,now())",
		"DELETE FROM push_tokens WHERE user_id=$1")
	// Refresh must lock only its session: taking the player row as well, after
	// the session, would deadlock with the player-then-session order above.
	assertFileOrder(t, "auth_handlers.go", "func (s *Server) refreshSession",
		"AND s.revoked_at IS NULL AND u.status='active' FOR UPDATE OF s`, presentedHash)")
}

func TestPushDeliveryRequiresALiveSession(t *testing.T) {
	for _, required := range []string{"token.revoked_at IS NULL", "session.id=token.session_id",
		"session.user_id=token.user_id", "session.revoked_at IS NULL"} {
		if !strings.Contains(notificationLivePushTokenSQL, required) {
			t.Errorf("live push token condition is missing %q", required)
		}
	}
	const path = "notification_pipeline.go"
	// Fan-out at projection, claim-time suppression and the claim itself.
	assertFileOrder(t, path, "func (s *Server) projectNotificationBatch",
		"WHERE token.user_id=$2 AND `+notificationLivePushTokenSQL+` AND CASE $3",
		"func (s *Server) claimNotificationPushBatch",
		"AND (NOT EXISTS (SELECT 1 FROM push_tokens token WHERE token.id=delivery.push_token_id\n\t\t\tAND `+notificationLivePushTokenSQL+`)",
		"JOIN push_tokens token ON token.id=delivery.push_token_id AND `+notificationLivePushTokenSQL+`")
	source := readSourceFile(t, path)
	if count := strings.Count(source, "token.revoked_at IS"); count != 1 {
		t.Errorf("%s checks token.revoked_at %d times; only notificationLivePushTokenSQL may", path, count)
	}
}

func TestPushTokenSessionMigrationShape(t *testing.T) {
	up := filepath.Join("..", "..", "migrations", pushTokenSessionMigration+".up.sql")
	down := filepath.Join("..", "..", "migrations", pushTokenSessionMigration+".down.sql")
	for _, path := range []string{up, down} {
		contents := strings.TrimSpace(readSourceFile(t, path))
		if !strings.HasPrefix(contents, "BEGIN;") || !strings.HasSuffix(contents, "COMMIT;") {
			t.Errorf("%s must be wrapped in BEGIN; ... COMMIT;", path)
		}
	}
	assertFileOrder(t, up,
		"ADD COLUMN session_id uuid REFERENCES refresh_sessions(id) ON DELETE CASCADE;",
		"UPDATE push_tokens SET revoked_at=now(),updated_at=now()\nWHERE session_id IS NULL AND revoked_at IS NULL;",
		"CREATE INDEX push_tokens_session_idx ON push_tokens (session_id);")
	assertFileContains(t, down, "DROP INDEX IF EXISTS push_tokens_session_idx;",
		"ALTER TABLE push_tokens DROP COLUMN IF EXISTS session_id;")
}

// TestIntegrationPushTokensStopWithTheirSession runs registration, logout,
// remote revoke and the production projector and claim against PostgreSQL.
func TestIntegrationPushTokensStopWithTheirSession(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := &Server{db: &database.Cluster{Writer: pool, Reader: pool},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: config.Config{NotificationProject: 100, NotificationPushBatch: 100, ExpoPushTimeout: time.Second}}
	userID := pushSessionIntegrationUser(t, pool)
	phone, laptop, tablet := pushSessionIntegrationSession(t, pool, userID),
		pushSessionIntegrationSession(t, pool, userID), pushSessionIntegrationSession(t, pool, userID)
	phoneToken := pushSessionIntegrationRegister(t, server, userID, phone, "phone", http.StatusOK)
	tabletToken := pushSessionIntegrationRegister(t, server, userID, tablet, "tablet", http.StatusOK)

	pushSessionIntegrationNotify(t, pool, server, userID)
	pushSessionIntegrationExpectClaims(t, server, phoneToken, tabletToken)

	// Logout revokes the phone's installation, and a push queued before it
	// is suppressed rather than sent.
	pushSessionIntegrationNotify(t, pool, server, userID)
	logout := pushSessionIntegrationRequest(t, http.MethodPost, "/v1/auth/logout", "", userID, phone)
	recorder := httptest.NewRecorder()
	server.logout(recorder, logout)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("logout = %d %s", recorder.Code, recorder.Body)
	}
	pushSessionIntegrationExpectRevoked(t, pool, phoneToken, true)
	pushSessionIntegrationExpectClaims(t, server, tabletToken)
	pushSessionIntegrationExpectSuppressed(t, pool, phoneToken, 1)
	pushSessionIntegrationRegister(t, server, userID, phone, "phone", http.StatusUnauthorized)

	// Signing the tablet out from the laptop stops the tablet as well.
	pushSessionIntegrationNotify(t, pool, server, userID)
	revoke := pushSessionIntegrationRequest(t, http.MethodDelete, "/v1/me/sessions/"+tablet, "", userID, laptop)
	revoke.SetPathValue("id", tablet)
	recorder = httptest.NewRecorder()
	server.revokeSession(recorder, revoke)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("remote revoke = %d %s", recorder.Code, recorder.Body)
	}
	pushSessionIntegrationExpectRevoked(t, pool, tabletToken, true)
	pushSessionIntegrationExpectClaims(t, server)
	pushSessionIntegrationExpectSuppressed(t, pool, tabletToken, 1)

	// Signing in again rebinds the same installation to the new session.
	phoneAgain := pushSessionIntegrationSession(t, pool, userID)
	if again := pushSessionIntegrationRegister(t, server, userID, phoneAgain, "phone", http.StatusOK); again != phoneToken {
		t.Fatalf("re-registration created installation %s, want %s", again, phoneToken)
	}
	pushSessionIntegrationNotify(t, pool, server, userID)
	pushSessionIntegrationExpectClaims(t, server, phoneToken)

	// Another player signing in on the phone takes the provider token over.
	otherID := pushSessionIntegrationUser(t, pool)
	otherToken := pushSessionIntegrationRegister(t, server, otherID, pushSessionIntegrationSession(t, pool, otherID),
		"phone", http.StatusOK)
	pushSessionIntegrationExpectRevoked(t, pool, phoneToken, true)
	pushSessionIntegrationExpectRevoked(t, pool, otherToken, false)

	// A deleted player cannot register, even from a session row left behind.
	deletedID := pushSessionIntegrationUser(t, pool)
	deletedSession := pushSessionIntegrationSession(t, pool, deletedID)
	if _, err := pool.Exec(t.Context(), `UPDATE users SET status='deleted' WHERE id=$1`, deletedID); err != nil {
		t.Fatal(err)
	}
	pushSessionIntegrationRegister(t, server, deletedID, deletedSession, "tablet", http.StatusUnauthorized)
}

// TestIntegrationPushTokenSessionMigration upgrades a schema holding a legacy
// installation, then rolls the migration back and forward again.
func TestIntegrationPushTokenSessionMigration(t *testing.T) {
	pool := openIntegrationSchema(t)
	applyEmbeddedMigrationsBefore(t, pool, pushTokenSessionMigration)
	userID := pushSessionIntegrationUser(t, pool)
	var legacyID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO push_tokens(user_id,device_id,expo_push_token,platform)
		VALUES ($1,'legacy-device','ExpoPushToken[legacy-installation-0001]','android') RETURNING id::text`,
		userID).Scan(&legacyID); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"up", "down", "up"} {
		raw := readSourceFile(t, filepath.Join("..", "..", "migrations", pushTokenSessionMigration+"."+direction+".sql"))
		if err := execMigrationSQL(t.Context(), pool, raw); err != nil {
			t.Fatalf("apply %s %s: %v", pushTokenSessionMigration, direction, err)
		}
	}
	var revoked bool
	var sessionID *string
	if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL,session_id::text FROM push_tokens WHERE id=$1`,
		legacyID).Scan(&revoked, &sessionID); err != nil {
		t.Fatal(err)
	}
	if !revoked || sessionID != nil {
		t.Fatalf("legacy installation revoked=%v session=%v, want revoked without a session", revoked, sessionID)
	}
}

func pushSessionIntegrationUser(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var userID string
	handle := "push_" + strings.ToLower(rand.Text())[:12]
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,display_name,status) VALUES ($1,$2,'active')
		RETURNING id::text`, handle+"@gamics.test", handle).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return userID
}

func pushSessionIntegrationSession(t *testing.T, pool *pgxpool.Pool, userID string) string {
	t.Helper()
	var sessionID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO refresh_sessions(id,user_id,token_hash)
		VALUES (gen_random_uuid(),$1,$2) RETURNING id::text`, userID, []byte(rand.Text())).Scan(&sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return sessionID
}

func pushSessionIntegrationRequest(t *testing.T, method, target, body, userID, sessionID string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{
		UserID: userID, SessionID: sessionID, ExpiresAt: time.Now().Add(time.Minute)}))
}

// pushSessionIntegrationRegister registers the named device's fixed provider
// token and returns the installation ID when the call succeeds.
func pushSessionIntegrationRegister(t *testing.T, server *Server, userID, sessionID, device string, wantStatus int) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"deviceId": device, "platform": "android",
		"token": "ExpoPushToken[" + device + "-installation-000000000]"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.upsertPushToken(recorder, pushSessionIntegrationRequest(t, http.MethodPost, "/v1/me/push-tokens",
		string(body), userID, sessionID))
	if recorder.Code != wantStatus {
		t.Fatalf("register %s = %d %s, want %d", device, recorder.Code, recorder.Body, wantStatus)
	}
	var registered pushTokenView
	if wantStatus == http.StatusOK {
		if err = json.NewDecoder(bytes.NewReader(recorder.Body.Bytes())).Decode(&registered); err != nil {
			t.Fatal(err)
		}
	}
	return registered.ID
}

// pushSessionIntegrationNotify projects one push-enabled notification to the
// player through the production projector.
func pushSessionIntegrationNotify(t *testing.T, pool *pgxpool.Pool, server *Server, userID string) {
	t.Helper()
	notificationIntegrationEmit(t, pool, "user", userID, "player.strike_recorded",
		map[string]any{"strikeId": testNotificationStrikeID, "userId": userID})
	notificationIntegrationProject(t, server)
}

// pushSessionIntegrationExpectClaims claims every due delivery and requires
// exactly the given installations.
func pushSessionIntegrationExpectClaims(t *testing.T, server *Server, want ...string) {
	t.Helper()
	claims, err := server.claimNotificationPushBatch(t.Context())
	if err != nil {
		t.Fatalf("claim pushes: %v", err)
	}
	got := make([]string, 0, len(claims))
	for _, claim := range claims {
		got = append(got, claim.PushTokenID)
	}
	slices.Sort(got)
	want = slices.Clone(want)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("claimed pushes for %v, want %v", got, want)
	}
}

func pushSessionIntegrationExpectRevoked(t *testing.T, pool *pgxpool.Pool, tokenID string, want bool) {
	t.Helper()
	var revoked bool
	if err := pool.QueryRow(t.Context(), `SELECT revoked_at IS NOT NULL FROM push_tokens WHERE id=$1`, tokenID).
		Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked != want {
		t.Fatalf("installation %s revoked=%v, want %v", tokenID, revoked, want)
	}
}

func pushSessionIntegrationExpectSuppressed(t *testing.T, pool *pgxpool.Pool, tokenID string, want int) {
	t.Helper()
	var suppressed int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_push_deliveries
		WHERE push_token_id=$1 AND state='suppressed'`, tokenID).Scan(&suppressed); err != nil {
		t.Fatal(err)
	}
	if suppressed != want {
		t.Fatalf("installation %s has %d suppressed pushes, want %d", tokenID, suppressed, want)
	}
}
