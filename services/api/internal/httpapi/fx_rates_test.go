package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
)

func TestParseFXRatesReadsCommonProviders(t *testing.T) {
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	exchangeRateAPI := `{"result":"success","base_code":"USD","time_last_update_unix":1791331201,
		"rates":{"USD":1,"KES":129.25,"JPY":148.1,"IDR":16250.5,"EUR":0.92}}`
	day, quotes, err := parseFXRates([]byte(exchangeRateAPI), now)
	if err != nil {
		t.Fatal(err)
	}
	if got := day.Format(time.DateOnly); got != "2026-10-07" {
		t.Errorf("rate day = %s", got)
	}
	got := map[string]string{}
	for _, quote := range quotes {
		got[quote.Currency] = quote.UnitsPerUSD
	}
	// USD itself and unsupported currencies are skipped.
	want := map[string]string{"KES": "129.25", "JPY": "148.1", "IDR": "16250.5"}
	if len(got) != len(want) {
		t.Fatalf("quotes = %v, want %v", got, want)
	}
	for currency, units := range want {
		if got[currency] != units {
			t.Errorf("%s = %s, want %s", currency, got[currency], units)
		}
	}

	openExchangeRates := `{"base":"USD","timestamp":1791331201,"rates":{"KES":129.25}}`
	if _, quotes, err := parseFXRates([]byte(openExchangeRates), now); err != nil || len(quotes) != 1 {
		t.Fatalf("Open Exchange Rates shape = %v, %v", quotes, err)
	}

	for name, body := range map[string]string{
		"another base":    `{"base":"EUR","rates":{"KES":140}}`,
		"no base":         `{"rates":{"KES":140}}`,
		"failed result":   `{"result":"error","base_code":"USD","rates":{"KES":140}}`,
		"negative rate":   `{"base":"USD","rates":{"KES":-1}}`,
		"zero rate":       `{"base":"USD","rates":{"KES":0}}`,
		"nothing usable":  `{"base":"USD","rates":{"EUR":0.9}}`,
		"future date":     `{"base":"USD","date":"2026-12-01","rates":{"KES":129}}`,
		"not json":        `<html>`,
		"string rate":     `{"base":"USD","rates":{"KES":"lots"}}`,
		"absurd rate":     `{"base":"USD","rates":{"KES":1e15}}`,
		"too small to be": `{"base":"USD","rates":{"KES":0.00000000001}}`,
	} {
		if _, _, err := parseFXRates([]byte(body), now); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestSupportedCurrenciesMatchTheMigrationAndDashboard(t *testing.T) {
	migration := readSourceFile(t, "../../migrations/000003_competitions.up.sql")
	seed := regexp.MustCompile(`\('([A-Z]{3})', ([0-9]), '([^']+)'\)`).FindAllStringSubmatch(migration, -1)
	if len(seed) != len(supportedCurrencies) {
		t.Fatalf("migration seeds %d currencies, Go knows %d", len(seed), len(supportedCurrencies))
	}
	for _, row := range seed {
		info, ok := supportedCurrencies[row[1]]
		if !ok || strconv.Itoa(info.MinorUnit) != row[2] || info.Name != row[3] {
			t.Errorf("migration row %v disagrees with %+v", row[1:], info)
		}
	}
	for country, currency := range countryCurrencies {
		if _, ok := supportedCurrencies[currency]; !ok {
			t.Errorf("%s uses unsupported %s", country, currency)
		}
	}
	dashboard := readSourceFile(t, "../../../../apps/admin/src/lib/currency.ts")
	for code, info := range supportedCurrencies {
		if !strings.Contains(dashboard, code+": "+strconv.Itoa(info.MinorUnit)) {
			t.Errorf("the dashboard does not format %s with %d decimals", code, info.MinorUnit)
		}
	}
	for country, currency := range countryCurrencies {
		if !strings.Contains(dashboard, country+": '"+currency+"'") {
			t.Errorf("the dashboard does not price %s in %s", country, currency)
		}
	}
}

func fxTestServer(pool *pgxpool.Pool) *Server {
	return &Server{
		db:     &database.Cluster{Writer: pool, Reader: pool},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		config: config.Config{AccessTokenSecret: "fx-test-secret"},
	}
}

func fxInsertSucceededPayment(t *testing.T, pool *pgxpool.Pool, entry integrationEntry, competitionID string,
	amountMinor int64, completedAt time.Time) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(t.Context(), `INSERT INTO payment_intents(user_id,competition_id,game_account_id,entry_id,
		entry_display_name,amount_minor,phone_e164,request_ip,idempotency_key,request_hash,status,provider_receipt,
		completed_at)
		VALUES ($1,$2,(SELECT id FROM game_accounts WHERE user_id=$1 LIMIT 1),$3,'FX player',$4,'254712345678',
		 '127.0.0.1','fx-'||gen_random_uuid()::text,repeat('a',64),'succeeded','FX'||substr(md5(random()::text),1,12),$5)
		RETURNING id::text`, entry.UserID, competitionID, entry.ID, amountMinor, completedAt).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

type fxStamp struct {
	USD      *int64
	Units    *string
	RateDate *string
}

func fxReadStamp(t *testing.T, pool *pgxpool.Pool, table, id string) fxStamp {
	t.Helper()
	var stamp fxStamp
	if err := pool.QueryRow(t.Context(), `SELECT amount_usd_minor,trim(trailing '.' from trim(trailing '0' from fx_units_per_usd::text)),
		to_char(fx_rate_date,'YYYY-MM-DD') FROM `+table+` WHERE id=$1`, id).
		Scan(&stamp.USD, &stamp.Units, &stamp.RateDate); err != nil {
		t.Fatal(err)
	}
	return stamp
}

func (stamp fxStamp) is(usd int64, units, day string) bool {
	return stamp.USD != nil && *stamp.USD == usd && stamp.Units != nil && *stamp.Units == units &&
		stamp.RateDate != nil && *stamp.RateDate == day
}

// TestIntegrationPaymentsFreezeTheirUSDValue checks the rate each payment
// converts at, that the value never moves afterwards, that refunds reuse their
// payment's rate, and that the finance report adds up only USD values.
func TestIntegrationPaymentsFreezeTheirUSDValue(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := fxTestServer(pool)
	seeded := seedIntegrationCompetition(t, pool, integrationSeedOptions{Format: "single_elimination", Entries: 4})
	ctx := t.Context()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := func(offset int) string { return today.AddDate(0, 0, offset).Format(time.DateOnly) }
	if _, err := pool.Exec(ctx, `INSERT INTO fx_rates(currency,rate_date,units_per_usd,source) VALUES
		('KES',$1,130,'test'),('KES',$2,128,'test')`, day(-2), day(0)); err != nil {
		t.Fatal(err)
	}

	// KES 100 a day ago converts at the newest rate on or before that day.
	yesterday := fxInsertSucceededPayment(t, pool, seeded.Entries[0], seeded.ID, 10_000, today.Add(-12*time.Hour))
	// One from before any rate converts at the earliest rate after it.
	early := fxInsertSucceededPayment(t, pool, seeded.Entries[1], seeded.ID, 10_000, today.AddDate(0, 0, -10))
	// One today converts at today's rate: 100/128 = 0.78125, rounded to 78 cents.
	now := fxInsertSucceededPayment(t, pool, seeded.Entries[2], seeded.ID, 10_000, time.Now().UTC())
	if err := server.backfillUSD(ctx); err != nil {
		t.Fatal(err)
	}
	if stamp := fxReadStamp(t, pool, "payment_intents", yesterday); !stamp.is(77, "130", day(-2)) {
		t.Fatalf("yesterday's payment = %+v", stamp)
	}
	if stamp := fxReadStamp(t, pool, "payment_intents", early); !stamp.is(77, "130", day(-2)) {
		t.Fatalf("pre-rate payment = %+v", stamp)
	}
	if stamp := fxReadStamp(t, pool, "payment_intents", now); !stamp.is(78, "128", day(0)) {
		t.Fatalf("today's payment = %+v", stamp)
	}

	// A new rate never moves a frozen value.
	if _, err := pool.Exec(ctx, `UPDATE fx_rates SET units_per_usd=1 WHERE currency='KES'`); err != nil {
		t.Fatal(err)
	}
	if err := server.backfillUSD(ctx); err != nil {
		t.Fatal(err)
	}
	if stamp := fxReadStamp(t, pool, "payment_intents", now); !stamp.is(78, "128", day(0)) {
		t.Fatalf("a frozen value moved: %+v", stamp)
	}

	// A refund converts at its payment's rate, whatever today's rate is.
	var refundID string
	if err := pool.QueryRow(ctx, `INSERT INTO payment_refunds(payment_id,user_id,entry_id,amount_minor,currency,
		reason_code,status,provider_receipt,completed_at)
		SELECT id,user_id,entry_id,amount_minor,currency,'player_withdrawal','succeeded','RF-'||id::text,now()
		FROM payment_intents WHERE id=$1 RETURNING id::text`, yesterday).Scan(&refundID); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = stampRefundUSD(ctx, tx, refundID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if stamp := fxReadStamp(t, pool, "payment_refunds", refundID); !stamp.is(77, "130", day(-2)) {
		t.Fatalf("refund = %+v", stamp)
	}

	// Prizes are valued at today's rate: JPY 150,000 at 150 per dollar is $1,000.
	if _, err := pool.Exec(ctx, `INSERT INTO fx_rates(currency,rate_date,units_per_usd,source) VALUES ('JPY',$1,150,'test')`,
		day(0)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE competitions SET currency='JPY',prize_amount_minor=150000,prize_funding='organizer',
		registration_opens_at=now()-interval '3 days',registration_closes_at=now()-interval '2 days',
		starts_at=now()-interval '1 day' WHERE id=$1`, seeded.ID); err != nil {
		t.Fatal(err)
	}
	report, err := server.loadFinanceReport(ctx, 30, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Prizes) != 1 || report.Prizes[0].Currency != "JPY" || report.Prizes[0].PrizesMinor != 150_000 ||
		report.Prizes[0].PrizesUSDMinor == nil || *report.Prizes[0].PrizesUSDMinor != 100_000 ||
		report.Totals.PrizesUSDMinor != 100_000 {
		t.Fatalf("prizes = %+v, totals %+v", report.Prizes, report.Totals)
	}
	if report.Totals.CollectedUSDMinor != 77+77+78 || report.Totals.RefundedUSDMinor != 77 ||
		report.Totals.NetUSDMinor != 77+78 || report.Totals.Payments != 3 || report.Totals.Refunds != 1 ||
		report.Totals.Unconverted != 0 {
		t.Fatalf("finance totals = %+v", report.Totals)
	}
	if len(report.ByCurrency) != 1 || report.ByCurrency[0].CollectedMinor != 30_000 ||
		report.ByCurrency[0].RefundedMinor != 10_000 || report.ByCurrency[0].NetMinor != 20_000 {
		t.Fatalf("by currency = %+v", report.ByCurrency)
	}
	if len(report.Daily) != 31 || len(report.Recent) != 4 {
		t.Fatalf("daily %d days, recent %d rows", len(report.Daily), len(report.Recent))
	}
	var daily int64
	for _, item := range report.Daily {
		daily += item.CollectedUSDMinor
	}
	if daily != report.Totals.CollectedUSDMinor {
		t.Fatalf("daily collections add up to %d, totals say %d", daily, report.Totals.CollectedUSDMinor)
	}
}

// TestIntegrationExchangeRatesFetchAndManualEntry fetches rates from a fake
// provider, checks a manual rate survives the next fetch, and checks only
// admins may enter one.
func TestIntegrationExchangeRatesFetchAndManualEntry(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	day := time.Now().UTC().Add(-time.Hour)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":"success","base_code":"USD","time_last_update_unix":`+
			strconv.FormatInt(day.Unix(), 10)+`,"rates":{"KES":129.5,"JPY":148,"INR":83.2}}`)
	}))
	defer provider.Close()
	server := fxTestServer(pool)
	server.config.FXRatesURL = provider.URL
	if count, err := server.fetchFXRates(t.Context()); err != nil || count != 3 {
		t.Fatalf("fetch = %d, %v", count, err)
	}
	rates, err := server.loadFXRates(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]string{}
	for _, rate := range rates {
		if rate.Units != nil {
			units[rate.Currency] = *rate.Units
		}
	}
	if units["KES"] != "129.5" || units["JPY"] != "148" || units["INR"] != "83.2" || units["BRL"] != "" {
		t.Fatalf("stored rates = %v", units)
	}

	admin, reviewer := resultFlowInsertStaff(t, pool, "fx-admin", "admin"), resultFlowInsertStaff(t, pool, "fx-reviewer", "reviewer")
	put := func(userID, currency, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, "/v1/admin/fx-rates/"+currency, strings.NewReader(body))
		request = request.WithContext(context.WithValue(t.Context(), identityContextKey{}, identity{UserID: userID}))
		recorder := httptest.NewRecorder()
		// Check the route's permission, then call the handler behind it.
		request.SetPathValue("currency", currency)
		allowed, err := server.staffCan(t.Context(), userID, platformFinanceManage)
		if err != nil {
			t.Fatal(err)
		}
		if !allowed {
			recorder.WriteHeader(http.StatusForbidden)
			return recorder
		}
		server.setFXRate(recorder, request)
		return recorder
	}
	if recorder := put(reviewer, "KES", `{"unitsPerUsd":"131"}`); recorder.Code != http.StatusForbidden {
		t.Fatalf("a reviewer set a rate: %d", recorder.Code)
	}
	for body, code := range map[string]int{
		`{"unitsPerUsd":"-3"}`:                          http.StatusBadRequest,
		`{"unitsPerUsd":"1.12345678901"}`:               http.StatusBadRequest,
		`{"unitsPerUsd":"131","rateDate":"2999-01-01"}`: http.StatusBadRequest,
	} {
		if recorder := put(admin, "KES", body); recorder.Code != code {
			t.Errorf("%s = %d %s", body, recorder.Code, recorder.Body)
		}
	}
	if recorder := put(admin, "USD", `{"unitsPerUsd":"1"}`); recorder.Code != http.StatusNotFound {
		t.Errorf("USD rate accepted: %d", recorder.Code)
	}
	if recorder := put(admin, "KES", `{"unitsPerUsd":"131.25","rateDate":"`+day.Format(time.DateOnly)+`"}`); recorder.Code != http.StatusOK {
		t.Fatalf("admin rate = %d %s", recorder.Code, recorder.Body)
	}
	// The next fetch leaves the manual rate alone.
	if _, err := server.fetchFXRates(t.Context()); err != nil {
		t.Fatal(err)
	}
	var source, stored string
	if err := pool.QueryRow(t.Context(), `SELECT source,units_per_usd::text FROM fx_rates
		WHERE currency='KES' AND rate_date=$1`, day.Format(time.DateOnly)).Scan(&source, &stored); err != nil {
		t.Fatal(err)
	}
	if source != "manual" || !strings.HasPrefix(stored, "131.25") {
		t.Fatalf("manual rate became %s %s", source, stored)
	}
	var audited int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='fx_rate.set'
		AND subject_type='fx_rate' AND subject_id='KES' AND actor_user_id=$1`, admin).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit rows = %d, %v", audited, err)
	}
}
