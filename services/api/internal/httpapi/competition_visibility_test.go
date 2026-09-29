package httpapi

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/migrations"
)

const competitionPublicationMigration = "000024_competition_publication"

// The addressable reads accept every discovery status and a cancellation of a
// published competition; nothing else, so drafts stay hidden.
func TestReadableCompetitionSQLExtendsDiscoveryWithPublishedCancellations(t *testing.T) {
	statusList, found := strings.CutPrefix(readableCompetitionSQL, "(competition.status IN (")
	if !found {
		t.Fatalf("readableCompetitionSQL does not start with the discovery statuses: %s", readableCompetitionSQL)
	}
	statusList, _, found = strings.Cut(statusList, ")")
	if !found {
		t.Fatalf("readableCompetitionSQL has no closed status list: %s", readableCompetitionSQL)
	}
	statuses := strings.Split(statusList, ",")
	for index := range statuses {
		statuses[index] = strings.Trim(statuses[index], "'")
	}
	if !slices.Equal(statuses, publicCompetitionStatuses) {
		t.Fatalf("readable statuses %v drifted from the discovery statuses %v", statuses, publicCompetitionStatuses)
	}
	if slices.Contains(publicCompetitionStatuses, "cancelled") || slices.Contains(publicCompetitionStatuses, "draft") {
		t.Fatalf("discovery lists must not include cancelled or draft competitions: %v", publicCompetitionStatuses)
	}
	if !strings.Contains(readableCompetitionSQL, "OR (competition.status='cancelled' AND competition.published_at IS NOT NULL))") {
		t.Fatalf("readableCompetitionSQL does not limit cancelled competitions to published ones: %s", readableCompetitionSQL)
	}
	// The detail joins an active game and organizer; every read sharing the
	// rule must hide the same competitions.
	for _, predicate := range []string{"readable_game.id=competition.game_id AND readable_game.active",
		"readable_organization.id=competition.organization_id AND readable_organization.status='active'"} {
		if !strings.Contains(readableCompetitionSQL, predicate) {
			t.Errorf("readableCompetitionSQL lacks %q, so it can return what the detail hides", predicate)
		}
	}
}

// Detail, bracket and eligibility share the readable rule while the list keeps
// the discovery statuses. Free and paid entry answer 404 for what the detail
// hides and refuse a cancelled competition through the eligibility decision,
// whose first issue is the cancellation.
func TestCompetitionReadsShareTheVisibilityRule(t *testing.T) {
	for _, read := range []struct{ path, declaration string }{
		{"competition_handlers.go", "func (s *Server) loadCompetitionDetail("},
		{"competition_handlers.go", "func competitionIsReadable("},
		{"competition_eligibility.go", "func queryCompetitionEligibilityFacts("},
	} {
		source := resultReportsFunctionSource(t, read.path, read.declaration)
		if !strings.Contains(source, "readableCompetitionSQL") || strings.Contains(source, "publicCompetitionStatuses") {
			t.Errorf("%s does not apply readableCompetitionSQL alone", read.declaration)
		}
	}
	assertOrder(t, "queryCompetitionBracket",
		resultReportsFunctionSource(t, "competition_handlers.go", "func queryCompetitionBracket("),
		"competitionIsReadable(ctx, pool, id)", "return nil, pgx.ErrNoRows", "FROM competition_stages stage")
	list := resultReportsFunctionSource(t, "competition_handlers.go", "func queryCompetitionPage(")
	if !strings.Contains(list, "statuses := publicCompetitionStatuses") || strings.Contains(list, "readableCompetitionSQL") {
		t.Error("the discovery list must keep excluding cancelled competitions")
	}
	notFound := `writeError(w, http.StatusNotFound, "competition_not_found"`
	for _, entry := range []struct {
		path, declaration string
		fragments         []string
	}{
		{"competition_handlers.go", "func (s *Server) createFreeRegistration(", []string{"FOR UPDATE",
			"loadCompetitionEligibility(", "errors.Is(eligibilityErr, pgx.ErrNoRows)", notFound,
			"writeCompetitionIneligible(w, eligibility, *issue)"}},
		{"payment_handlers.go", "func (s *Server) initiateMPesa(", []string{"FOR UPDATE",
			"loadCompetitionEligibility(", "errors.Is(eligibilityErr, pgx.ErrNoRows)", notFound,
			"writeCompetitionIneligible(w, eligibility, *issue)", `"payment_not_available"`}},
	} {
		source := resultReportsFunctionSource(t, entry.path, entry.declaration)
		assertOrder(t, entry.declaration, source, entry.fragments...)
		if strings.Contains(source, `"competition_cancelled"`) {
			t.Errorf("%s refuses a cancellation itself instead of through the eligibility issue", entry.declaration)
		}
	}
}

func TestCompetitionEligibilityReportsCancellation(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	registered, pendingRefund, withdrawn := "registered", "withdrawal_pending", "withdrawn"
	cancelled := func(entryStatus *string) competitionEligibilityFacts {
		facts := eligibilityStrikesFacts(now, 0, 3)
		facts.Status, facts.EntryStatus = "cancelled", entryStatus
		return facts
	}
	unfinished := cancelled(nil)
	unfinished.ProfileComplete, unfinished.ActiveStrikes = false, 3
	tests := []struct {
		name       string
		facts      competitionEligibilityFacts
		wantStatus string
		wantAction string
		wantCodes  []string
	}{
		{"a new player is blocked by the cancellation", cancelled(nil),
			eligibilityStatusIneligible, "none", []string{"competition_cancelled"}},
		{"a withdrawn player is blocked by both rules, the cancellation first", cancelled(&withdrawn),
			eligibilityStatusIneligible, "none", []string{"competition_cancelled", "registration_not_reusable"}},
		{"the cancellation comes before the player's own issues", unfinished, eligibilityStatusIneligible, "none",
			[]string{"competition_cancelled", "conduct_suspended", "profile_incomplete"}},
		{"a registered player still sees the registration", cancelled(&registered),
			eligibilityStatusRegistered, "view_registration", []string{}},
		{"a paid entrant still sees the refund", cancelled(&pendingRefund),
			eligibilityStatusRegistered, "view_refund", []string{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := assessCompetitionEligibility(test.facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
			codes := make([]string, 0, len(result.Issues))
			for _, issue := range result.Issues {
				codes = append(codes, issue.Code)
				if issue.Code == "competition_cancelled" && (issue.Severity != "blocking" || issue.Category != "registration") {
					t.Fatalf("unexpected cancellation issue %+v", issue)
				}
			}
			if result.Status != test.wantStatus || result.RequiredAction != test.wantAction ||
				result.CanRegisterNow || !slices.Equal(codes, test.wantCodes) {
				t.Fatalf("status=%s action=%s codes=%v, want %s %s %v", result.Status, result.RequiredAction, codes,
					test.wantStatus, test.wantAction, test.wantCodes)
			}
		})
	}
}

func TestCompetitionPublicationMigrationShape(t *testing.T) {
	up := readSourceFile(t, filepath.Join("..", "..", "migrations", competitionPublicationMigration+".up.sql"))
	down := readSourceFile(t, filepath.Join("..", "..", "migrations", competitionPublicationMigration+".down.sql"))
	for name, contents := range map[string]string{"up": up, "down": down} {
		if !strings.HasPrefix(contents, "BEGIN;") || !strings.HasSuffix(strings.TrimSpace(contents), "COMMIT;") {
			t.Fatalf("the %s migration must be wrapped in BEGIN; ... COMMIT;", name)
		}
	}
	assertOrder(t, "up migration", up,
		"ALTER TABLE competitions ADD COLUMN published_at timestamptz;",
		"IF NEW.published_at IS NULL AND NEW.status NOT IN ('draft', 'cancelled') THEN",
		"BEFORE INSERT OR UPDATE OF status ON competitions",
		"UPDATE competitions competition SET published_at",
		"audit.action = 'competition.published'",
		"audit.before_state ->> 'status' <> 'draft'",
		"FROM competition_entries entry")
	assertOrder(t, "down migration", down,
		"DROP TRIGGER IF EXISTS competitions_stamp_published_at ON competitions;",
		"DROP FUNCTION IF EXISTS stamp_competition_published_at();",
		"ALTER TABLE competitions DROP COLUMN IF EXISTS published_at;")
	if _, err := fs.Stat(migrations.FS, competitionPublicationMigration+".up.sql"); err != nil {
		t.Fatalf("the up migration is not embedded: %v", err)
	}
}

// Competition.status and Registration.competitionStatus admit cancelled in the
// assembled contract and in its fragment, while discovery stays filterable by
// the discovery statuses only.
func TestOpenAPICompetitionStatusesAdmitCancelled(t *testing.T) {
	readable := append(slices.Clone(publicCompetitionStatuses), "cancelled")
	for _, name := range []string{"openapi.yaml", "competition.paths.yaml"} {
		contract := openAPIFile(t, name)
		for _, field := range []struct{ schema, property string }{
			{"Competition", "status"}, {"Registration", "competitionStatus"},
		} {
			block := openAPIBlock(t, openAPIBlock(t, contract, field.schema, 4), field.property, 8)
			if got := openAPIEnum(t, block); !slices.Equal(got, readable) {
				t.Errorf("%s %s.%s enum = %v, want %v", name, field.schema, field.property, got, readable)
			}
		}
		registration := openAPIBlock(t, contract, "Registration", 4)
		required, _, _ := strings.Cut(registration, "\n      properties:")
		if !strings.Contains(required, "competitionStatus") {
			t.Errorf("%s Registration does not require competitionStatus", name)
		}
		listParameters, _, _ := strings.Cut(openAPIBlock(t, contract, "/v1/competitions", 2), "responses:")
		if strings.Contains(listParameters, "cancelled") {
			t.Errorf("%s lets discovery filter by cancelled", name)
		}
	}
}

// The eligibility contract lists every issue code the assessment can emit.
func TestOpenAPIListsEveryEligibilityIssueCode(t *testing.T) {
	issuePattern := regexp.MustCompile(`(?:blocking|action)\("([a-z_]+)"`)
	var codes []string
	for _, match := range issuePattern.FindAllStringSubmatch(readSourceFile(t, "competition_eligibility.go"), -1) {
		codes = append(codes, match[1])
	}
	if !slices.Contains(codes, "competition_cancelled") {
		t.Fatalf("the extraction pattern no longer matches the assessment: %v", codes)
	}
	for _, name := range []string{"openapi.yaml", "competition-match-policy.paths.yaml"} {
		documented := openAPIEnum(t, openAPIBlock(t, openAPIFile(t, name), "CompetitionEligibilityIssue", 4))
		for _, code := range codes {
			if !slices.Contains(documented, code) {
				t.Errorf("%s CompetitionEligibilityIssue does not list %s", name, code)
			}
		}
	}
}
