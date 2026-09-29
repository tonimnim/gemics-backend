package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Every combination of payment, entry and refund status maps to exactly one
// registration outcome. Only a succeeded payment can hold a place or be
// refunded, and a refund that is owed or paid outranks the entry.
func TestPaymentRegistrationStatusCoversEveryState(t *testing.T) {
	paymentStatuses := []string{"initiating", "pending", "callback_received", "review", "succeeded", "failed"}
	entryStatuses := []string{"", "registered", "checked_in", "accepted", "withdrawal_pending", "withdrawn", "disqualified"}
	refundStatuses := []string{"", "requested", "approved", "processing", "manual_review", "failed", "succeeded", "rejected"}
	// A succeeded payment without a live refund answers from its entry; ""
	// is a read that raced the completing transaction.
	byEntry := map[string]string{
		"": "pending", "registered": "registered", "checked_in": "registered", "accepted": "registered",
		"withdrawal_pending": "refund_pending", "withdrawn": "not_registered", "disqualified": "removed",
	}
	// A rejected refund is missing here: the entry decides again.
	byRefund := map[string]string{
		"requested": "refund_pending", "approved": "refund_pending", "processing": "refund_pending",
		"manual_review": "refund_pending", "failed": "refund_pending", "succeeded": "refunded",
	}
	for _, payment := range paymentStatuses {
		for _, entry := range entryStatuses {
			for _, refundStatus := range refundStatuses {
				var want string
				switch {
				case payment == "failed":
					want = paymentRegistrationNotRegistered
				case payment != "succeeded":
					want = paymentRegistrationPending
				case byRefund[refundStatus] != "":
					want = byRefund[refundStatus]
				default:
					want = byEntry[entry]
				}
				var entryStatus *string
				if entry != "" {
					entryStatus = &entry
				}
				var refund *paymentRefundView
				if refundStatus != "" {
					refund = &paymentRefundView{Status: refundStatus}
				}
				if got := paymentRegistrationStatus(payment, entryStatus, refund); got != want {
					t.Errorf("payment=%s entry=%q refund=%q: registrationStatus=%s, want %s",
						payment, entry, refundStatus, got, want)
				}
			}
		}
	}
}

func TestPaymentIntentJSONCarriesTheRegistrationOutcome(t *testing.T) {
	raw, err := json.Marshal(paymentIntent{Status: "succeeded", RegistrationStatus: paymentRegistrationRegistered,
		QueryAttempts: 3, NextQueryAt: &time.Time{}})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["registrationStatus"]) != `"registered"` || string(fields["refund"]) != "null" {
		t.Fatalf("payment JSON lacks the registration outcome: %s", raw)
	}
	for _, internal := range []string{"QueryAttempts", "NextQueryAt", "queryAttempts", "nextQueryAt"} {
		if _, leaked := fields[internal]; leaked {
			t.Fatalf("payment JSON exposes %s: %s", internal, raw)
		}
	}
}

// Every payment view reads through one select that binds the payer, so the
// polled payment, the replayed STK response and the history agree.
func TestPaymentViewsShareThePayerBoundSelect(t *testing.T) {
	load := resultReportsFunctionSource(t, "payment_handlers.go", "func (s *Server) loadPayment(")
	if !strings.Contains(load, "paymentIntentSelect+`WHERE payment.id=$1 AND payment.user_id=$2`") {
		t.Fatalf("loadPayment does not read the payer-bound payment view:\n%s", load)
	}
	history := resultReportsFunctionSource(t, "payment_lifecycle_handlers.go", "func (s *Server) listMyPayments(")
	if !strings.Contains(history, "paymentIntentSelect+`WHERE payment.user_id=$1") ||
		!strings.Contains(history, "scanPaymentIntent(rows)") {
		t.Fatalf("payment history does not read the payer-bound payment view:\n%s", history)
	}
	for _, join := range []string{"LEFT JOIN competition_entries entry ON entry.id=payment.entry_id",
		"candidate.payment_id=payment.id AND candidate.user_id=payment.user_id", "ORDER BY candidate.requested_at DESC,candidate.id DESC LIMIT 1"} {
		if !strings.Contains(paymentIntentSelect, join) {
			t.Errorf("paymentIntentSelect lacks %q", join)
		}
	}
}

func TestOpenAPIPaymentRegistrationStatusEnum(t *testing.T) {
	want := []string{paymentRegistrationPending, paymentRegistrationRegistered, paymentRegistrationRefundPending,
		paymentRegistrationRefunded, paymentRegistrationRemoved, paymentRegistrationNotRegistered}
	schema := openAPIBlock(t, openAPIFile(t, "openapi.yaml"), "PaymentIntent", 4)
	if got := openAPIEnum(t, openAPIBlock(t, schema, "registrationStatus", 8)); !slices.Equal(got, want) {
		t.Fatalf("PaymentIntent.registrationStatus enum = %v, want %v", got, want)
	}
	for _, field := range []string{"- registrationStatus\n", "- refund\n", "$ref: '#/components/schemas/PaymentRefund'"} {
		if !strings.Contains(schema, field) {
			t.Errorf("PaymentIntent does not declare %q", field)
		}
	}
}

func TestCompetitionEligibilityNamesThePaymentToPoll(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	facts := eligibilityStrikesFacts(now, 0, 0)
	facts.EntryFeeMinor = 10_000
	paymentID, pending := "4d3e5536-bf3c-4dba-a643-575e43f56970", "pending"
	facts.PaymentID, facts.PaymentStatus = &paymentID, &pending
	result := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
	if result.RequiredAction != "poll_payment" || result.PaymentID == nil || *result.PaymentID != paymentID {
		t.Fatalf("a pending payment is not named for polling: %+v", result)
	}
	facts.PaymentID, facts.PaymentStatus = nil, nil
	if result = assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now); result.PaymentID != nil {
		t.Fatalf("a player without a payment was given one: %+v", result)
	}
}

// An unfinished payment still registers or refunds the payer after entry
// closed or filled, so the preflight keeps naming it for polling.
func TestCompetitionEligibilityPollsAnUnfinishedPaymentWhenBlocked(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	paymentID := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	cases := []struct {
		paymentStatus string
		want          string
	}{
		{"initiating", "poll_payment"},
		{"pending", "poll_payment"},
		{"callback_received", "poll_payment"},
		{"review", "poll_payment"},
		{"succeeded", "none"},
		{"failed", "none"},
		{"", "none"},
	}
	blockers := map[string]func(*competitionEligibilityFacts){
		"registration closed": func(facts *competitionEligibilityFacts) { facts.RegistrationClosesAt = now.Add(-time.Minute) },
		"competition full":    func(facts *competitionEligibilityFacts) { facts.OccupiedEntries = facts.MaxEntries },
		"no game account":     func(facts *competitionEligibilityFacts) { facts.GameAccountID = nil },
	}
	for blocker, block := range blockers {
		for _, tc := range cases {
			t.Run(blocker+"/"+tc.paymentStatus, func(t *testing.T) {
				facts := eligibilityStrikesFacts(now, 0, 0)
				facts.EntryFeeMinor = 10_000
				block(&facts)
				if tc.paymentStatus != "" {
					status := tc.paymentStatus
					facts.PaymentID, facts.PaymentStatus = &paymentID, &status
				}
				result := assessCompetitionEligibility(facts, competitionEligibilityRules{AllowedCountries: []string{}}, now)
				if result.Eligible || result.RequiredAction != tc.want {
					t.Fatalf("eligible=%v requiredAction=%q, want blocked with %q", result.Eligible, result.RequiredAction, tc.want)
				}
			})
		}
	}
}

var sqlBindPlaceholder = regexp.MustCompile(`\$([0-9]+)`)

// sqlPlaceholderCount returns the highest $N placeholder of a query, which is
// the number of bind arguments PostgreSQL expects.
func sqlPlaceholderCount(query string) int {
	highest := 0
	for _, match := range sqlBindPlaceholder.FindAllStringSubmatch(query, -1) {
		if number, err := strconv.Atoi(match[1]); err == nil {
			highest = max(highest, number)
		}
	}
	return highest
}

// bindCheckingQueryer answers a single count and, like pgx against
// PostgreSQL, fails a query whose bind arguments do not match its placeholders.
type bindCheckingQueryer struct {
	args []any
}

func (queryer *bindCheckingQueryer) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	queryer.args = args
	if want := sqlPlaceholderCount(query); len(args) != want {
		return capacityCountRow{err: fmt.Errorf("expected %d arguments, got %d", want, len(args))}
	}
	return capacityCountRow{value: 3}
}

type capacityCountRow struct {
	value int
	err   error
}

func (row capacityCountRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != 1 {
		return errors.New("capacityCountRow scans a single int")
	}
	target, ok := destinations[0].(*int)
	if !ok {
		return errors.New("capacityCountRow scans a single int")
	}
	*target = row.value
	return nil
}

// Regression for the STK push capacity check, which ran with $1 and no bind
// argument, so every first payment attempt failed with 503.
func TestPaymentCapacityQueryBindsTheCompetition(t *testing.T) {
	competitionID := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	queryer := &bindCheckingQueryer{}
	occupied, err := countPaymentCapacityOccupied(t.Context(), queryer, competitionID)
	if err != nil || occupied != 3 {
		t.Fatalf("capacity query failed: occupied=%d err=%v", occupied, err)
	}
	if len(queryer.args) != 1 || queryer.args[0] != competitionID {
		t.Fatalf("capacity query bound %v, want the competition ID", queryer.args)
	}
	for _, fragment := range []string{competitionCapacityEntriesSQL, "FROM payment_intents WHERE competition_id=$1",
		"entry_id IS NULL", "status IN ('initiating','pending','callback_received','review')"} {
		if !strings.Contains(paymentCapacityOccupiedSQL, fragment) {
			t.Errorf("paymentCapacityOccupiedSQL lacks %q", fragment)
		}
	}
	initiate := resultReportsFunctionSource(t, "payment_handlers.go", "func (s *Server) initiateMPesa(")
	if !strings.Contains(initiate, "countPaymentCapacityOccupied(r.Context(), tx, input.CompetitionID)") {
		t.Fatal("initiateMPesa does not check capacity through the bound query")
	}
}

// sqlLiteralWithoutArguments matches a query call whose last argument is a SQL
// literal, the shape in which a placeholder shipped without its argument.
var sqlLiteralWithoutArguments = regexp.MustCompile("\\.(?:QueryRow|Query|Exec)\\(\\s*[\\w.]+(?:\\(\\))?,\\s*`([^`]*)`\\s*\\)")

func TestSQLLiteralPlaceholdersHaveBindArguments(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("list the package sources: %v (%d found)", err, len(files))
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		for _, match := range sqlLiteralWithoutArguments.FindAllStringSubmatch(readSourceFile(t, file), -1) {
			if sqlPlaceholderCount(match[1]) > 0 {
				t.Errorf("%s runs a query with placeholders and no bind arguments:\n%s", file, match[1])
			}
		}
	}
	if sqlPlaceholderCount("SELECT $1,$3 WHERE a=$2") != 3 || !sqlLiteralWithoutArguments.MatchString(
		"err = tx.QueryRow(r.Context(), `SELECT count(*) FROM t WHERE id=$1`).Scan(&n)") {
		t.Fatal("the placeholder guard no longer recognises the regression it pins")
	}
}
