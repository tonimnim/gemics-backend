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
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	"github.com/gamics-io/gamics/services/api/internal/mpesa"
	"github.com/gamics-io/gamics/services/api/internal/organizer"
	"github.com/gamics-io/gamics/services/api/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a disposable PostgreSQL database in
// GAMICS_TEST_DATABASE_URL and skip without it. They cancel competitions
// through the organizer transition and read them back through the public and
// player handlers.

type competitionVisibilityDetail struct {
	Data competitionDetail `json:"data"`
}

type competitionVisibilityBracket struct {
	Data struct {
		CompetitionID string         `json:"competitionId"`
		Stages        []bracketStage `json:"stages"`
	} `json:"data"`
}

type competitionVisibilityEligibility struct {
	Data competitionEligibilityResult `json:"data"`
}

// competitionVisibilityMPesa fails any STK prompt: a refused paid entry must
// never reach the provider.
type competitionVisibilityMPesa struct{}

func (competitionVisibilityMPesa) Initiate(context.Context, mpesa.InitiateRequest) (mpesa.InitiateResponse, error) {
	return mpesa.InitiateResponse{}, errors.New("a cancelled competition must not start an STK prompt")
}

func (competitionVisibilityMPesa) Query(context.Context, string) (mpesa.QueryResponse, error) {
	return mpesa.QueryResponse{}, errors.New("a cancelled competition must not query M-Pesa")
}

func competitionVisibilityServer(pool *pgxpool.Pool) *Server {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(config.Config{AccessTokenSecret: "competition-visibility-secret", RequestTimeout: 30 * time.Second},
		logger, "test", Dependencies{Database: &database.Cluster{Writer: pool, Reader: pool}, MPesa: competitionVisibilityMPesa{}})
}

// competitionVisibilityCall runs one handler with the competition bound to the
// id path value. A non-empty userID signs the request in, and a POST carries a
// fresh Idempotency-Key.
func competitionVisibilityCall(t *testing.T, handler http.HandlerFunc, method, target, userID, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := t.Context()
	if userID != "" {
		ctx = context.WithValue(ctx, identityContextKey{}, identity{UserID: userID})
	}
	request := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(ctx)
	if id != "" {
		request.SetPathValue("id", id)
	}
	if method == http.MethodPost {
		request.Header.Set("Idempotency-Key", "visibility-"+rand.Text())
	}
	recorder := httptest.NewRecorder()
	handler(recorder, request)
	return recorder
}

func competitionVisibilityDecode[T any](t *testing.T, recorder *httptest.ResponseRecorder, status int) T {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	var value T
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", recorder.Body.String(), err)
	}
	return value
}

func competitionVisibilityRefused(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	body := competitionVisibilityDecode[map[string]any](t, recorder, status)
	if body["error"] != code {
		t.Fatalf("error %v, want %s: %s", body["error"], code, recorder.Body.String())
	}
}

// competitionVisibilityCancel cancels a competition through the organizer
// transition, as the organization's owner.
func competitionVisibilityCancel(t *testing.T, server *Server, seeded integrationCompetition, competitionID string) {
	t.Helper()
	request := resultFlowStaffRequest(t, http.MethodPost, "/", seeded.OrganizerID, `{"status":"cancelled","reason":"Venue closed."}`)
	request = request.WithContext(context.WithValue(request.Context(), organizerContextKey{},
		organizerMembership{OrganizationID: seeded.OrganizationID, Role: organizer.RoleOwner}))
	request.SetPathValue("competitionId", competitionID)
	recorder := httptest.NewRecorder()
	server.transitionOrganizerCompetition(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cancel competition %s: %d %s", competitionID, recorder.Code, recorder.Body.String())
	}
}

// competitionVisibilityRegister gives a seeded player a free entry that stays
// registered, like every free entry of a cancelled competition.
func competitionVisibilityRegister(t *testing.T, pool *pgxpool.Pool, competitionID, userID string) string {
	t.Helper()
	var entryID string
	if err := pool.QueryRow(t.Context(), `WITH entry AS (
		INSERT INTO competition_entries(competition_id,display_name,captain_user_id)
		VALUES ($1,'Visibility player',$2) RETURNING id)
		INSERT INTO entry_members(entry_id,competition_id,user_id,game_account_id)
		SELECT entry.id,$1,$2,account.id FROM entry JOIN game_accounts account ON account.user_id=$2
		RETURNING entry_id::text`, competitionID, userID).Scan(&entryID); err != nil {
		t.Fatal(err)
	}
	return entryID
}

func competitionVisibilityRegistration(t *testing.T, server *Server, userID, competitionID string) registrationItem {
	t.Helper()
	page := competitionVisibilityDecode[registrationPage](t,
		competitionVisibilityCall(t, server.listMyRegistrations, http.MethodGet, "/v1/me/registrations", userID, "", ""),
		http.StatusOK)
	index := slices.IndexFunc(page.Data, func(item registrationItem) bool { return item.CompetitionID == competitionID })
	if index < 0 {
		t.Fatalf("the registrations of %s omit competition %s: %+v", userID, competitionID, page.Data)
	}
	return page.Data[index]
}

// A cancelled competition stays addressable with status cancelled, leaves
// discovery, keeps its entrants' registrations and refuses new entries with
// competition_cancelled, whether it had a draw or was still registering.
func TestIntegrationCancelledCompetitionStaysReadable(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)
	drawn := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	registering := resultFlowInsertOpenCompetition(t, pool, drawn, 0)
	player := drawn.Entries[0].UserID
	freeEntryID := competitionVisibilityRegister(t, pool, registering, player)
	competitionVisibilityCancel(t, server, drawn, drawn.ID)
	competitionVisibilityCancel(t, server, drawn, registering)

	outsider := resultReportsInsertUser(t, pool, "visibility-outsider")
	for _, competitionID := range []string{drawn.ID, registering} {
		detail := competitionVisibilityDecode[competitionVisibilityDetail](t, competitionVisibilityCall(t,
			server.getCompetition, http.MethodGet, "/v1/competitions/"+competitionID, "", competitionID, ""), http.StatusOK)
		if detail.Data.ID != competitionID || detail.Data.Status != "cancelled" {
			t.Fatalf("detail of cancelled %s = %+v", competitionID, detail.Data.competitionSummary)
		}
		bracket := competitionVisibilityDecode[competitionVisibilityBracket](t, competitionVisibilityCall(t,
			server.getCompetitionBracket, http.MethodGet, "/v1/competitions/"+competitionID+"/bracket", "", competitionID, ""),
			http.StatusOK)
		wantStages := 0
		if competitionID == drawn.ID {
			wantStages = 1
		}
		if bracket.Data.CompetitionID != competitionID || len(bracket.Data.Stages) != wantStages {
			t.Fatalf("bracket of cancelled %s has %d stages, want %d", competitionID, len(bracket.Data.Stages), wantStages)
		}
		for _, stage := range bracket.Data.Stages {
			for _, round := range stage.Rounds {
				for _, match := range round.Matches {
					if match.State != "cancelled" || match.Progression.CompletionReason == nil ||
						*match.Progression.CompletionReason != "competition_cancelled" {
						t.Fatalf("bracket match %s of the cancelled competition = %+v", match.ID, match)
					}
				}
			}
		}

		list := competitionVisibilityDecode[competitionPage](t, competitionVisibilityCall(t, server.listCompetitions,
			http.MethodGet, "/v1/competitions?q="+url.QueryEscape(detail.Data.Name), "", "", ""), http.StatusOK)
		if slices.ContainsFunc(list.Data, func(item competitionSummary) bool { return item.ID == competitionID }) {
			t.Fatalf("discovery still lists cancelled %s", competitionID)
		}

		eligibility := competitionVisibilityDecode[competitionVisibilityEligibility](t, competitionVisibilityCall(t,
			server.getCompetitionEligibility, http.MethodGet, "/", outsider, competitionID, ""), http.StatusOK)
		if eligibility.Data.Eligible || !slices.ContainsFunc(eligibility.Data.Issues,
			func(issue eligibilityIssue) bool { return issue.Code == "competition_cancelled" }) {
			t.Fatalf("eligibility of cancelled %s = %+v", competitionID, eligibility.Data)
		}
		competitionVisibilityRefused(t, competitionVisibilityCall(t, server.createFreeRegistration, http.MethodPost, "/",
			outsider, competitionID, `{"gameAccountId":"11111111-1111-4111-8111-111111111111"}`),
			http.StatusConflict, "competition_cancelled")
		competitionVisibilityRefused(t, competitionVisibilityCall(t, server.initiateMPesa, http.MethodPost, "/", outsider, "",
			`{"competitionId":"`+competitionID+`","gameAccountId":"11111111-1111-4111-8111-111111111111","phoneNumber":"0712345678"}`),
			http.StatusConflict, "competition_cancelled")
	}
	competitionVisibilityRefused(t, competitionVisibilityCall(t, server.listCompetitions, http.MethodGet,
		"/v1/competitions?status=cancelled", "", "", ""), http.StatusBadRequest, "invalid_status")
	if intents := resultReportsCount(t, pool, `SELECT count(*) FROM payment_intents WHERE user_id=$1`, outsider); intents != 0 {
		t.Fatalf("refused paid entries created %d payment intents", intents)
	}

	drawnEntry := competitionVisibilityRegistration(t, server, player, drawn.ID)
	freeEntry := competitionVisibilityRegistration(t, server, player, registering)
	if drawnEntry.CompetitionStatus != "cancelled" || drawnEntry.Status != "accepted" ||
		freeEntry.ID != freeEntryID || freeEntry.CompetitionStatus != "cancelled" || freeEntry.Status != "registered" {
		t.Fatalf("registrations of cancelled competitions = %+v, %+v", drawnEntry, freeEntry)
	}
	// The registered player keeps the registration view on the preflight.
	registered := competitionVisibilityDecode[competitionVisibilityEligibility](t, competitionVisibilityCall(t,
		server.getCompetitionEligibility, http.MethodGet, "/", player, registering, ""), http.StatusOK)
	if registered.Data.Status != eligibilityStatusRegistered || registered.Data.RequiredAction != "view_registration" {
		t.Fatalf("entrant eligibility of the cancelled competition = %+v", registered.Data)
	}
}

// A draft cancelled before publication was never public, so every public read
// and both entry paths still hide it, as they hide a suspended organizer's
// competitions.
func TestIntegrationCancelledDraftStaysHidden(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 2})
	suffix := strings.ToLower(rand.Text())[:12]
	var draftID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO competitions(organization_id,game_id,name,slug,format,
		max_entries,registration_opens_at,registration_closes_at,starts_at,created_by)
		VALUES ($1,$2,$3,$4,'single_elimination',8,now()+interval '1 hour',now()+interval '2 hours',
		 now()+interval '3 hours',$5) RETURNING id::text`,
		seeded.OrganizationID, seeded.GameID, "Draft cup "+suffix, "draft-"+suffix, seeded.OrganizerID).Scan(&draftID); err != nil {
		t.Fatal(err)
	}
	competitionVisibilityCancel(t, server, seeded, draftID)
	if published := resultReportsCount(t, pool, `SELECT count(*) FROM competitions
		WHERE id=$1 AND status='cancelled' AND published_at IS NOT NULL`, draftID); published != 0 {
		t.Fatal("a cancelled draft was recorded as published")
	}
	if published := resultReportsCount(t, pool, `SELECT count(*) FROM competitions
		WHERE id=$1 AND published_at IS NOT NULL`, seeded.ID); published != 1 {
		t.Fatal("a competition inserted in a public status was not recorded as published")
	}
	outsider := resultReportsInsertUser(t, pool, "draft-outsider")
	hidden := func(competitionID string) map[string]*httptest.ResponseRecorder {
		return map[string]*httptest.ResponseRecorder{
			"detail":      competitionVisibilityCall(t, server.getCompetition, http.MethodGet, "/", "", competitionID, ""),
			"bracket":     competitionVisibilityCall(t, server.getCompetitionBracket, http.MethodGet, "/", "", competitionID, ""),
			"eligibility": competitionVisibilityCall(t, server.getCompetitionEligibility, http.MethodGet, "/", outsider, competitionID, ""),
			"free entry": competitionVisibilityCall(t, server.createFreeRegistration, http.MethodPost, "/", outsider,
				competitionID, `{"gameAccountId":"11111111-1111-4111-8111-111111111111"}`),
			"paid entry": competitionVisibilityCall(t, server.initiateMPesa, http.MethodPost, "/", outsider, "",
				`{"competitionId":"`+competitionID+`","gameAccountId":"11111111-1111-4111-8111-111111111111","phoneNumber":"0712345678"}`),
		}
	}
	for name, recorder := range hidden(draftID) {
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "competition_not_found") {
			t.Fatalf("the %s of a cancelled draft is visible: %d %s", name, recorder.Code, recorder.Body.String())
		}
	}
	resultFlowExec(t, pool, `UPDATE organizations SET status='suspended' WHERE id=$1`, seeded.OrganizationID)
	for name, recorder := range hidden(seeded.ID) {
		if recorder.Code != http.StatusNotFound || !strings.Contains(recorder.Body.String(), "competition_not_found") {
			t.Fatalf("the %s of a suspended organizer's competition is visible: %d %s", name, recorder.Code, recorder.Body.String())
		}
	}
}

// The backfill marks every competition players could have seen as published
// and leaves drafts, including cancelled drafts, unpublished; the trigger then
// stamps new publications and the down migration removes all of it.
func TestIntegrationCompetitionPublicationMigration(t *testing.T) {
	pool := openIntegrationSchema(t)
	applyEmbeddedMigrationsBefore(t, pool, competitionPublicationMigration)
	ctx := t.Context()
	var ownerID, organizationID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(email,display_name,status)
		VALUES ('publication-owner@gamics.test','Owner','active') RETURNING id::text`).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO organizations(name,slug) VALUES ('Publication','publication')
		RETURNING id::text`).Scan(&organizationID); err != nil {
		t.Fatal(err)
	}
	insert := func(label, status string) string {
		t.Helper()
		var competitionID string
		if err := pool.QueryRow(ctx, `INSERT INTO competitions(organization_id,game_id,name,slug,format,status,
			max_entries,registration_opens_at,registration_closes_at,starts_at,created_by,created_at)
			VALUES ($1,'efootball-mobile',$2,$2,'single_elimination',$3,8,now()-interval '3 days',
			 now()-interval '2 days',now()-interval '1 day',$4,now()-interval '10 days') RETURNING id::text`,
			organizationID, label, status, ownerID).Scan(&competitionID); err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
		return competitionID
	}
	audit := func(competitionID, action, before string, occurredAt time.Time) {
		t.Helper()
		resultFlowExec(t, pool, `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,
			subject_id,before_state,occurred_at) VALUES ($1,$2,$3,'competition',$4,
			jsonb_build_object('status',$5::text),$6)`, organizationID, ownerID, action, competitionID, before, occurredAt)
	}
	publishedAt := time.Date(2026, 9, 1, 9, 30, 0, 0, time.UTC)
	running := insert("running", "running")
	draft := insert("draft", "draft")
	cancelledAfterPublication := insert("cancelled-published", "cancelled")
	audit(cancelledAfterPublication, "competition.published", "draft", publishedAt)
	cancelledFromRegistration := insert("cancelled-registration", "cancelled")
	audit(cancelledFromRegistration, "competition.cancelled", "registration_open", publishedAt)
	cancelledDraft := insert("cancelled-draft", "cancelled")
	audit(cancelledDraft, "competition.cancelled", "draft", publishedAt)
	cancelledWithEntry := insert("cancelled-entry", "cancelled")
	resultFlowExec(t, pool, `INSERT INTO competition_entries(competition_id,display_name,captain_user_id)
		VALUES ($1,'Entrant',$2)`, cancelledWithEntry, ownerID)

	raw, err := migrations.FS.ReadFile(competitionPublicationMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err = execMigrationSQL(ctx, pool, string(raw)); err != nil {
		t.Fatalf("apply %s: %v", competitionPublicationMigration, err)
	}
	for _, check := range []struct {
		name, competitionID string
		want                string
	}{
		{"running", running, "created"},
		{"draft", draft, "unpublished"},
		{"cancelled after an audited publication", cancelledAfterPublication, "audited"},
		{"cancelled from registration", cancelledFromRegistration, "created"},
		{"cancelled draft", cancelledDraft, "unpublished"},
		{"cancelled with an entry", cancelledWithEntry, "created"},
	} {
		var got string
		if err = pool.QueryRow(ctx, `SELECT CASE WHEN published_at IS NULL THEN 'unpublished'
			WHEN published_at=$2 THEN 'audited' WHEN published_at=created_at THEN 'created' ELSE 'other' END
			FROM competitions WHERE id=$1`, check.competitionID, publishedAt).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != check.want {
			t.Errorf("%s backfilled as %s, want %s", check.name, got, check.want)
		}
	}

	resultFlowExec(t, pool, `UPDATE competitions SET status='published' WHERE id=$1`, draft)
	stillDraft := insert("still-draft", "draft")
	resultFlowExec(t, pool, `UPDATE competitions SET status='cancelled' WHERE id=$1`, stillDraft)
	opened := insert("opened", "registration_open")
	for competitionID, want := range map[string]bool{draft: true, stillDraft: false, opened: true} {
		if stamped := resultReportsCount(t, pool, `SELECT count(*) FROM competitions
			WHERE id=$1 AND published_at IS NOT NULL`, competitionID); (stamped == 1) != want {
			t.Errorf("competition %s published_at stamped=%d, want %v", competitionID, stamped, want)
		}
	}

	down := readSourceFile(t, filepath.Join("..", "..", "migrations", competitionPublicationMigration+".down.sql"))
	if err = execMigrationSQL(ctx, pool, down); err != nil {
		t.Fatalf("roll back %s: %v", competitionPublicationMigration, err)
	}
	if remaining := resultReportsCount(t, pool, `SELECT
		(SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema()
		  AND table_name='competitions' AND column_name='published_at')
		+ (SELECT count(*) FROM pg_trigger WHERE tgrelid='competitions'::regclass
		  AND tgname='competitions_stamp_published_at')
		+ (CASE WHEN to_regprocedure('stamp_competition_published_at()') IS NULL THEN 0 ELSE 1 END)`); remaining != 0 {
		t.Fatalf("the down migration left %d publication objects behind", remaining)
	}
}
