package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Exchange rates turn money paid in many currencies into one reporting
// currency, USD, the way payment companies do it:
//
//   - a payment or refund is stored in the currency it was paid in, forever;
//     refunds and payouts go back in exactly that amount;
//   - when it succeeds, its USD value and the rate used are frozen on the row,
//     so last month's revenue never moves when a currency does;
//   - a refund reuses its payment's rate, so a full refund nets to zero;
//   - totals are summed per currency, then in USD; amounts in different
//     currencies are never added together.
//
// Rates are fetched daily from FX_RATES_URL, by default Frankfurter: free,
// keyless, and sourced from central banks, with each rate dated by its
// source. When a payment's own day has no rate, its exact day is fetched from
// the provider's history. An admin can enter a rate by hand when the provider
// is down; a fetch never overwrites a manual rate.

const (
	fxFetchTimeout     = 15 * time.Second
	fxMaxResponseBytes = 1 << 20
	fxBackfillBatch    = 500
	// fxStaleAfter is how old the newest rate may get before the dashboard
	// warns that conversions are using an old rate.
	fxStaleAfter = 3 * 24 * time.Hour
)

var fxDecimalPattern = regexp.MustCompile(`^[0-9]{1,12}(\.[0-9]{1,10})?$`)

// fxRateDaySQL picks the rate a row converts at: the newest rate on or before
// the day it succeeded, or, when none is that old, the earliest one after it.
// USD converts at 1. target must expose currency and day.
const fxRateDaySQL = `CROSS JOIN LATERAL (
		SELECT 1::numeric AS units_per_usd,target.day AS rate_date WHERE target.currency='USD'
		UNION ALL
		(SELECT fx.units_per_usd,fx.rate_date FROM fx_rates fx WHERE fx.currency=target.currency
		 ORDER BY fx.rate_date>target.day,abs(fx.rate_date-target.day) LIMIT 1)
	) rate`

// stampPaymentsUSDSQL freezes the USD value of succeeded payments that have
// none yet: one payment when $1 is set, otherwise up to $2 of them.
const stampPaymentsUSDSQL = `WITH target AS (
		SELECT payment.id,payment.amount_minor,payment.currency,currency.minor_unit,
			(COALESCE(payment.completed_at,now()) AT TIME ZONE 'UTC')::date AS day
		FROM payment_intents payment JOIN currencies currency ON currency.code=payment.currency
		WHERE payment.status='succeeded' AND payment.amount_usd_minor IS NULL
		  AND ($1::uuid IS NULL OR payment.id=$1::uuid)
		  -- Only rows a rate can price, so a batch never stalls behind
		  -- currencies that have no rate yet.
		  AND (payment.currency='USD' OR EXISTS (SELECT 1 FROM fx_rates fx WHERE fx.currency=payment.currency))
		ORDER BY payment.completed_at,payment.id LIMIT $2
		FOR UPDATE OF payment SKIP LOCKED
	), priced AS (
		SELECT target.id,target.amount_minor,target.minor_unit,rate.units_per_usd,rate.rate_date
		FROM target ` + fxRateDaySQL + `
	)
	UPDATE payment_intents payment SET
		amount_usd_minor=round(priced.amount_minor*100/(power(10::numeric,priced.minor_unit)*priced.units_per_usd))::bigint,
		fx_units_per_usd=priced.units_per_usd,fx_rate_date=priced.rate_date
	FROM priced WHERE payment.id=priced.id`

// stampRefundsUSDSQL freezes succeeded refunds at their payment's rate, once
// the payment has one.
const stampRefundsUSDSQL = `WITH target AS (
		SELECT refund.id FROM payment_refunds refund
		JOIN payment_intents payment ON payment.id=refund.payment_id
		WHERE refund.status='succeeded' AND refund.amount_usd_minor IS NULL
		  AND payment.fx_units_per_usd IS NOT NULL AND ($1::uuid IS NULL OR refund.id=$1::uuid)
		ORDER BY refund.completed_at,refund.id LIMIT $2
		FOR UPDATE OF refund SKIP LOCKED
	)
	UPDATE payment_refunds refund SET
		amount_usd_minor=round(refund.amount_minor*100/(power(10::numeric,currency.minor_unit)*payment.fx_units_per_usd))::bigint,
		fx_units_per_usd=payment.fx_units_per_usd,fx_rate_date=payment.fx_rate_date
	FROM target,payment_intents payment,currencies currency
	WHERE refund.id=target.id AND payment.id=refund.payment_id AND currency.code=refund.currency`

type sqlExecer interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

// stampPaymentUSD freezes one succeeded payment's USD value inside the
// transaction that completed it. Without a rate yet it is left for
// backfillUSD, which runs after every fetch.
func stampPaymentUSD(ctx context.Context, tx sqlExecer, paymentID string) error {
	_, err := tx.Exec(ctx, stampPaymentsUSDSQL, paymentID, 1)
	return err
}

func stampRefundUSD(ctx context.Context, tx sqlExecer, refundID string) error {
	_, err := tx.Exec(ctx, stampRefundsUSDSQL, refundID, 1)
	return err
}

// backfillUSD stamps every succeeded payment, then refund, still without a
// USD value, in batches.
func (s *Server) backfillUSD(ctx context.Context) error {
	for _, query := range []string{stampPaymentsUSDSQL, stampRefundsUSDSQL} {
		for {
			command, err := s.db.Writer.Exec(ctx, query, nil, fxBackfillBatch)
			if err != nil {
				return err
			}
			if command.RowsAffected() < fxBackfillBatch {
				break
			}
		}
	}
	return nil
}

// runFXRateRefresher fetches rates on start and every FXRefreshInterval, then
// stamps anything still unconverted. Every replica may run it: rate writes
// are upserts and stamping skips locked rows.
func (s *Server) runFXRateRefresher(ctx context.Context) {
	interval := s.config.FXRefreshInterval
	if interval <= 0 {
		interval = 6 * time.Hour
	}
	s.refreshFXRates(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshFXRates(ctx)
		}
	}
}

func (s *Server) refreshFXRates(ctx context.Context) {
	if s.config.FXRatesURL != "" {
		if count, err := s.fetchFXRates(ctx, s.config.FXRatesURL); err != nil {
			s.logger.Warn("fetch exchange rates", "error", err)
		} else {
			s.logger.Info("exchange rates updated", "currencies", count)
		}
		if err := s.fetchMissingFXDays(ctx); err != nil && ctx.Err() == nil {
			s.logger.Warn("fetch historical exchange rates", "error", err)
		}
	}
	if err := s.backfillUSD(ctx); err != nil && ctx.Err() == nil {
		s.logger.Warn("convert payments to USD", "error", err)
	}
}

// fxMissingDaysPerRefresh bounds how many past days one refresh asks the
// provider for.
const fxMissingDaysPerRefresh = 10

// fetchMissingFXDays asks a provider that serves history (Frankfurter) for the
// exact days of unconverted payments that have no rate from the week before
// them, so a payment made while rates were unavailable still converts at its
// own day's rate rather than a later one.
func (s *Server) fetchMissingFXDays(ctx context.Context) error {
	if _, ok := fxHistoryURL(s.config.FXRatesURL, time.Now()); !ok {
		return nil
	}
	rows, err := s.db.Writer.Query(ctx, `SELECT DISTINCT (payment.completed_at AT TIME ZONE 'UTC')::date
		FROM payment_intents payment
		WHERE payment.status='succeeded' AND payment.amount_usd_minor IS NULL AND payment.currency<>'USD'
		  AND NOT EXISTS (SELECT 1 FROM fx_rates fx WHERE fx.currency=payment.currency
			AND fx.rate_date<=(payment.completed_at AT TIME ZONE 'UTC')::date
			AND fx.rate_date>=(payment.completed_at AT TIME ZONE 'UTC')::date-7)
		ORDER BY 1 LIMIT $1`, fxMissingDaysPerRefresh)
	if err != nil {
		return err
	}
	days, err := pgx.CollectRows(rows, pgx.RowTo[time.Time])
	if err != nil {
		return err
	}
	for _, day := range days {
		historyURL, _ := fxHistoryURL(s.config.FXRatesURL, day)
		if _, err := s.fetchFXRates(ctx, historyURL); err != nil {
			return fmt.Errorf("%s: %w", day.Format(time.DateOnly), err)
		}
	}
	return nil
}

// fxHistoryURL returns the URL for one day's rates when the provider is a
// Frankfurter v2 rates endpoint (public or self-hosted), which takes a date.
func fxHistoryURL(rawURL string, day time.Time) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || !strings.HasSuffix(strings.TrimRight(parsed.Path, "/"), "/v2/rates") {
		return "", false
	}
	query := parsed.Query()
	query.Set("date", day.UTC().Format(time.DateOnly))
	parsed.RawQuery = query.Encode()
	return parsed.String(), true
}

// fxProviderResponse reads the single-date shape of USD-based rate APIs
// (ExchangeRate-API, Open Exchange Rates, Fixer): a base currency, a time and
// a rates object of units per base unit.
type fxProviderResponse struct {
	Result             string                 `json:"result"`
	Base               string                 `json:"base"`
	BaseCode           string                 `json:"base_code"`
	Date               string                 `json:"date"`
	Timestamp          int64                  `json:"timestamp"`
	TimeLastUpdateUnix int64                  `json:"time_last_update_unix"`
	Rates              map[string]json.Number `json:"rates"`
}

// fxProviderRow is one row of Frankfurter's v2 rates: each rate carries the
// day its source published it, which can differ between currencies.
type fxProviderRow struct {
	Date  string      `json:"date"`
	Base  string      `json:"base"`
	Quote string      `json:"quote"`
	Rate  json.Number `json:"rate"`
}

type fxQuote struct {
	Currency    string
	Day         time.Time
	UnitsPerUSD string
}

// parseFXRates validates a provider response and returns a quote, with its
// day, for every supported currency it includes. It reads Frankfurter's v2
// array of dated rows and the single-date object other providers return.
func parseFXRates(body []byte, now time.Time) ([]fxQuote, error) {
	trimmed := strings.TrimSpace(string(body))
	if strings.HasPrefix(trimmed, "[") {
		return parseFXRows(trimmed, now)
	}
	var response fxProviderResponse
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode rates: %w", err)
	}
	if response.Result != "" && response.Result != "success" {
		return nil, fmt.Errorf("provider result %q", response.Result)
	}
	base := strings.ToUpper(strings.TrimSpace(response.BaseCode))
	if base == "" {
		base = strings.ToUpper(strings.TrimSpace(response.Base))
	}
	if base != reportingCurrency {
		return nil, fmt.Errorf("rates are based on %q, not USD", base)
	}
	day := now.UTC()
	switch {
	case response.TimeLastUpdateUnix > 0:
		day = time.Unix(response.TimeLastUpdateUnix, 0).UTC()
	case response.Timestamp > 0:
		day = time.Unix(response.Timestamp, 0).UTC()
	case response.Date != "":
		parsed, err := time.Parse(time.DateOnly, response.Date)
		if err != nil {
			return nil, fmt.Errorf("rate date %q: %w", response.Date, err)
		}
		day = parsed
	}
	rows := make([]fxProviderRow, 0, len(response.Rates))
	for code, rate := range response.Rates {
		rows = append(rows, fxProviderRow{Date: day.Format(time.DateOnly), Base: base, Quote: code, Rate: rate})
	}
	return fxQuotesFromRows(rows, now)
}

func parseFXRows(body string, now time.Time) ([]fxQuote, error) {
	var rows []fxProviderRow
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&rows); err != nil {
		return nil, fmt.Errorf("decode rates: %w", err)
	}
	for _, row := range rows {
		if !strings.EqualFold(strings.TrimSpace(row.Base), reportingCurrency) {
			return nil, fmt.Errorf("rates are based on %q, not USD", row.Base)
		}
	}
	return fxQuotesFromRows(rows, now)
}

// fxQuotesFromRows keeps the supported currencies, validates each rate and
// day, and keeps one quote per currency and day.
func fxQuotesFromRows(rows []fxProviderRow, now time.Time) ([]fxQuote, error) {
	latest := now.UTC().Add(24 * time.Hour)
	seen := map[string]bool{}
	quotes := make([]fxQuote, 0, len(supportedCurrencies))
	for _, row := range rows {
		code := strings.ToUpper(strings.TrimSpace(row.Quote))
		if _, ok := supportedCurrencies[code]; !ok || code == reportingCurrency {
			continue
		}
		day, err := time.Parse(time.DateOnly, strings.TrimSpace(row.Date))
		if err != nil {
			return nil, fmt.Errorf("%s rate date %q: %w", code, row.Date, err)
		}
		if day.After(latest) {
			return nil, fmt.Errorf("%s rate date %s is in the future", code, row.Date)
		}
		units, err := normalizeFXRate(row.Rate.String())
		if err != nil {
			return nil, fmt.Errorf("%s: %w", code, err)
		}
		key := code + day.Format(time.DateOnly)
		if seen[key] {
			continue
		}
		seen[key] = true
		quotes = append(quotes, fxQuote{Currency: code, Day: day, UnitsPerUSD: units})
	}
	if len(quotes) == 0 {
		return nil, errors.New("no supported currency in the response")
	}
	// A fixed order makes concurrent upserts from several replicas lock rows
	// in the same order.
	sort.Slice(quotes, func(i, j int) bool {
		if quotes[i].Currency != quotes[j].Currency {
			return quotes[i].Currency < quotes[j].Currency
		}
		return quotes[i].Day.Before(quotes[j].Day)
	})
	return quotes, nil
}

// normalizeFXRate accepts a positive decimal of units per USD and returns it
// with at most 10 decimal places, which numeric(24,10) stores exactly.
func normalizeFXRate(raw string) (string, error) {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value >= 1e12 {
		return "", fmt.Errorf("rate %q is not a positive amount", raw)
	}
	formatted := strconv.FormatFloat(value, 'f', 10, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if !fxDecimalPattern.MatchString(formatted) || formatted == "0" {
		return "", fmt.Errorf("rate %q is too small", raw)
	}
	return formatted, nil
}

// fetchFXRates stores every supported rate the provider URL returns.
func (s *Server) fetchFXRates(ctx context.Context, rawURL string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, fxFetchTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, errors.New("rates provider URL is invalid")
	}
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		// The URL can carry a provider API key, so report only the cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return 0, fmt.Errorf("rates provider request: %w", urlErr.Err)
		}
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("rates provider returned %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, fxMaxResponseBytes+1))
	if err != nil {
		return 0, err
	}
	if len(body) > fxMaxResponseBytes {
		return 0, errors.New("rates response is too large")
	}
	quotes, err := parseFXRates(body, time.Now())
	if err != nil {
		return 0, err
	}
	source := "provider"
	if parsed, parseErr := url.Parse(rawURL); parseErr == nil && parsed.Host != "" {
		source = parsed.Host
	}
	batch := &pgx.Batch{}
	for _, quote := range quotes {
		batch.Queue(`INSERT INTO fx_rates(currency,rate_date,units_per_usd,source) VALUES ($1,$2,$3::numeric,$4)
			ON CONFLICT (currency,rate_date) DO UPDATE SET units_per_usd=EXCLUDED.units_per_usd,
				source=EXCLUDED.source,fetched_at=now()
			WHERE fx_rates.source<>'manual'`, quote.Currency, quote.Day, quote.UnitsPerUSD, source)
	}
	if err := s.db.Writer.SendBatch(ctx, batch).Close(); err != nil {
		return 0, err
	}
	return len(quotes), nil
}

type fxRateView struct {
	Currency  string     `json:"currency"`
	Name      string     `json:"name"`
	MinorUnit int        `json:"minorUnit"`
	Units     *string    `json:"unitsPerUsd"`
	RateDate  *string    `json:"rateDate"`
	Source    *string    `json:"source"`
	FetchedAt *time.Time `json:"fetchedAt"`
	Stale     bool       `json:"stale"`
}

type fxRateInput struct {
	UnitsPerUSD string `json:"unitsPerUsd"`
	RateDate    string `json:"rateDate"`
}

func (s *Server) registerFXRateRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/admin/fx-rates", s.platformRoute(platformOverviewView, s.listFXRates))
	mux.Handle("PUT /v1/admin/fx-rates/{currency}", s.platformRoute(platformFinanceManage, s.setFXRate))
}

// listFXRates returns the newest rate for every supported currency, so staff
// see what a price is worth in USD and admins see when rates went stale.
func (s *Server) listFXRates(w http.ResponseWriter, r *http.Request) {
	data, err := s.loadFXRates(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load exchange rates.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "reportingCurrency": reportingCurrency})
}

func (s *Server) loadFXRates(ctx context.Context) ([]fxRateView, error) {
	rows, err := s.db.Writer.Query(ctx, `SELECT currency.code,currency.name,currency.minor_unit,
		latest.units_per_usd::text,to_char(latest.rate_date,'YYYY-MM-DD'),latest.source,latest.fetched_at,
		latest.rate_date IS NOT NULL AND latest.rate_date<(now() AT TIME ZONE 'UTC')::date-$1::integer
		FROM currencies currency
		LEFT JOIN LATERAL (SELECT units_per_usd,rate_date,source,fetched_at FROM fx_rates
			WHERE fx_rates.currency=currency.code ORDER BY rate_date DESC LIMIT 1) latest ON true
		WHERE currency.code<>'USD' ORDER BY currency.code`, int(fxStaleAfter/(24*time.Hour)))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (fxRateView, error) {
		var view fxRateView
		err := row.Scan(&view.Currency, &view.Name, &view.MinorUnit, &view.Units, &view.RateDate, &view.Source,
			&view.FetchedAt, &view.Stale)
		if view.Units != nil {
			trimmed := strings.TrimRight(strings.TrimRight(*view.Units, "0"), ".")
			view.Units = &trimmed
		}
		return view, err
	})
}

// setFXRate lets an admin enter a day's rate by hand, for example while the
// provider is down. Rows already converted keep their frozen rate; only
// unconverted ones pick the new rate up.
func (s *Server) setFXRate(w http.ResponseWriter, r *http.Request) {
	currency := strings.ToUpper(strings.TrimSpace(r.PathValue("currency")))
	if _, ok := supportedCurrencies[currency]; !ok || currency == reportingCurrency {
		writeError(w, http.StatusNotFound, "currency_not_found", "Choose a supported currency other than USD.")
		return
	}
	var input fxRateInput
	if !decodeJSON(w, r, &input) {
		return
	}
	units, err := normalizeFXRate(input.UnitsPerUSD)
	if err != nil || !fxDecimalPattern.MatchString(strings.TrimSpace(input.UnitsPerUSD)) {
		writeError(w, http.StatusBadRequest, "invalid_rate", "Enter how many units of the currency one US dollar buys.")
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := today
	if strings.TrimSpace(input.RateDate) != "" {
		day, err = time.Parse(time.DateOnly, strings.TrimSpace(input.RateDate))
		if err != nil || day.After(today) {
			writeError(w, http.StatusBadRequest, "invalid_rate_date", "Use a date today or earlier, as YYYY-MM-DD.")
			return
		}
	}
	actor := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the rate.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	_, err = tx.Exec(r.Context(), `INSERT INTO fx_rates(currency,rate_date,units_per_usd,source,set_by)
		VALUES ($1,$2,$3::numeric,'manual',$4)
		ON CONFLICT (currency,rate_date) DO UPDATE SET units_per_usd=EXCLUDED.units_per_usd,source='manual',
			set_by=EXCLUDED.set_by,fetched_at=now()`, currency, day, units, actor)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events
			(actor_user_id,action,subject_type,subject_id,request_id,after_state)
			VALUES ($1,'fx_rate.set','fx_rate',$2,$3,jsonb_build_object('rateDate',$4::text,'unitsPerUsd',$5::text))`,
			actor, currency, r.Header.Get("X-Request-ID"), day.Format(time.DateOnly), units)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the rate.")
		return
	}
	if err := s.backfillUSD(r.Context()); err != nil {
		s.logger.Warn("convert payments to USD", "error", err)
	}
	s.listFXRates(w, r)
}
