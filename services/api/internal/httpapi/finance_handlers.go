package httpapi

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The finance report is the admins' view of money in and out. Every amount is
// shown in its own currency and, where it has one, at its frozen USD value;
// USD totals add up only converted rows and say how many are still waiting
// for a rate.

var financePeriods = []int{7, 30, 90, 365}

type financeTotals struct {
	CollectedUSDMinor      int64 `json:"collectedUsdMinor"`
	RefundedUSDMinor       int64 `json:"refundedUsdMinor"`
	NetUSDMinor            int64 `json:"netUsdMinor"`
	Payments               int   `json:"payments"`
	Refunds                int   `json:"refunds"`
	Unconverted            int   `json:"unconverted"`
	PrizesUSDMinor         int64 `json:"prizesUsdMinor"`
	UpcomingPrizesUSDMinor int64 `json:"upcomingPrizesUsdMinor"`
}

type financeCurrencyRow struct {
	Currency          string `json:"currency"`
	MinorUnit         int    `json:"minorUnit"`
	CollectedMinor    int64  `json:"collectedMinor"`
	RefundedMinor     int64  `json:"refundedMinor"`
	NetMinor          int64  `json:"netMinor"`
	CollectedUSDMinor int64  `json:"collectedUsdMinor"`
	RefundedUSDMinor  int64  `json:"refundedUsdMinor"`
	Payments          int    `json:"payments"`
	Refunds           int    `json:"refunds"`
}

type financeDay struct {
	Date              string `json:"date"`
	CollectedUSDMinor int64  `json:"collectedUsdMinor"`
	RefundedUSDMinor  int64  `json:"refundedUsdMinor"`
}

type financePrizeRow struct {
	Currency         string `json:"currency"`
	MinorUnit        int    `json:"minorUnit"`
	PrizesMinor      int64  `json:"prizesMinor"`
	UpcomingMinor    int64  `json:"upcomingMinor"`
	PrizesUSDMinor   *int64 `json:"prizesUsdMinor"`
	UpcomingUSDMinor *int64 `json:"upcomingUsdMinor"`
	Competitions     int    `json:"competitions"`
}

type financeMovement struct {
	Kind        string    `json:"kind"`
	ID          string    `json:"id"`
	At          time.Time `json:"at"`
	Player      string    `json:"player"`
	Competition string    `json:"competition"`
	AmountMinor int64     `json:"amountMinor"`
	Currency    string    `json:"currency"`
	USDMinor    *int64    `json:"usdMinor"`
}

type financeReport struct {
	Days       int                  `json:"days"`
	From       time.Time            `json:"from"`
	To         time.Time            `json:"to"`
	Totals     financeTotals        `json:"totals"`
	ByCurrency []financeCurrencyRow `json:"byCurrency"`
	Daily      []financeDay         `json:"daily"`
	Prizes     []financePrizeRow    `json:"prizes"`
	Recent     []financeMovement    `json:"recent"`
	Rates      []fxRateView         `json:"rates"`
}

func (s *Server) registerFinanceRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/admin/finance", s.platformRoute(platformFinanceView, s.getFinanceReport))
}

func (s *Server) getFinanceReport(w http.ResponseWriter, r *http.Request) {
	days := 30
	if raw := r.URL.Query().Get("days"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || !slices.Contains(financePeriods, parsed) {
			writeError(w, http.StatusBadRequest, "invalid_period", "Choose 7, 30, 90 or 365 days.")
			return
		}
		days = parsed
	}
	to := time.Now().UTC()
	report, err := s.loadFinanceReport(r.Context(), days, to)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the finance report.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": report})
}

// financeMoneySQL lists the period's succeeded payments (money in) and
// refunds (money out) with their frozen USD values.
const financeMoneySQL = `WITH money AS (
		SELECT 'payment' AS kind,payment.id,payment.completed_at AS at,payment.user_id,payment.competition_id,
			payment.amount_minor,payment.currency,payment.amount_usd_minor AS usd
		FROM payment_intents payment
		WHERE payment.status='succeeded' AND payment.completed_at>=$1 AND payment.completed_at<=$2
		UNION ALL
		SELECT 'refund',refund.id,refund.completed_at,refund.user_id,payment.competition_id,
			refund.amount_minor,refund.currency,refund.amount_usd_minor
		FROM payment_refunds refund JOIN payment_intents payment ON payment.id=refund.payment_id
		WHERE refund.status='succeeded' AND refund.completed_at>=$1 AND refund.completed_at<=$2
	) `

func (s *Server) loadFinanceReport(ctx context.Context, days int, to time.Time) (financeReport, error) {
	from := to.Add(-time.Duration(days) * 24 * time.Hour)
	report := financeReport{Days: days, From: from, To: to, ByCurrency: []financeCurrencyRow{},
		Daily: []financeDay{}, Prizes: []financePrizeRow{}, Recent: []financeMovement{}}

	rows, err := s.db.Writer.Query(ctx, financeMoneySQL+`SELECT money.currency,currency.minor_unit,
		COALESCE(sum(money.amount_minor) FILTER (WHERE kind='payment'),0)::bigint,
		COALESCE(sum(money.amount_minor) FILTER (WHERE kind='refund'),0)::bigint,
		COALESCE(sum(money.usd) FILTER (WHERE kind='payment'),0)::bigint,
		COALESCE(sum(money.usd) FILTER (WHERE kind='refund'),0)::bigint,
		count(*) FILTER (WHERE kind='payment')::integer,count(*) FILTER (WHERE kind='refund')::integer,
		count(*) FILTER (WHERE money.usd IS NULL)::integer
		FROM money JOIN currencies currency ON currency.code=money.currency
		GROUP BY money.currency,currency.minor_unit ORDER BY 5 DESC,money.currency`, from, to)
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var row financeCurrencyRow
		var unconverted int
		if err = rows.Scan(&row.Currency, &row.MinorUnit, &row.CollectedMinor, &row.RefundedMinor,
			&row.CollectedUSDMinor, &row.RefundedUSDMinor, &row.Payments, &row.Refunds, &unconverted); err != nil {
			rows.Close()
			return report, err
		}
		row.NetMinor = row.CollectedMinor - row.RefundedMinor
		report.ByCurrency = append(report.ByCurrency, row)
		report.Totals.CollectedUSDMinor += row.CollectedUSDMinor
		report.Totals.RefundedUSDMinor += row.RefundedUSDMinor
		report.Totals.Payments += row.Payments
		report.Totals.Refunds += row.Refunds
		report.Totals.Unconverted += unconverted
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return report, err
	}
	report.Totals.NetUSDMinor = report.Totals.CollectedUSDMinor - report.Totals.RefundedUSDMinor

	// Daily USD in and out, in UTC days, oldest first and with empty days.
	rows, err = s.db.Writer.Query(ctx, financeMoneySQL+`, days AS (
			SELECT generate_series(($1::timestamptz AT TIME ZONE 'UTC')::date,($2::timestamptz AT TIME ZONE 'UTC')::date,
				interval '1 day')::date AS day)
		SELECT to_char(days.day,'YYYY-MM-DD'),
			COALESCE(sum(money.usd) FILTER (WHERE money.kind='payment'),0)::bigint,
			COALESCE(sum(money.usd) FILTER (WHERE money.kind='refund'),0)::bigint
		FROM days LEFT JOIN money ON (money.at AT TIME ZONE 'UTC')::date=days.day
		GROUP BY days.day ORDER BY days.day`, from, to)
	if err != nil {
		return report, err
	}
	report.Daily, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (financeDay, error) {
		var day financeDay
		err := row.Scan(&day.Date, &day.CollectedUSDMinor, &day.RefundedUSDMinor)
		return day, err
	})
	if err != nil {
		return report, err
	}

	// Prizes are promises, not payments, so they are valued at today's rate.
	// "In period" covers running and finished competitions scheduled to start
	// in or after the period's first day (a cup can finish early, so its
	// scheduled start may still be ahead); "upcoming" covers published ones.
	rows, err = s.db.Writer.Query(ctx, `WITH prizes AS (
			SELECT competition.currency,
				CASE WHEN competition.status IN ('running','completed') AND competition.starts_at>=$1
				     THEN competition.prize_amount_minor ELSE 0 END AS in_period,
				CASE WHEN competition.status IN ('published','registration_open','check_in')
				     THEN competition.prize_amount_minor ELSE 0 END AS upcoming
			FROM competitions competition WHERE competition.prize_amount_minor>0)
		SELECT prizes.currency,currency.minor_unit,sum(in_period)::bigint,sum(upcoming)::bigint,
			CASE WHEN prizes.currency='USD' THEN sum(in_period)::bigint
			     ELSE round(sum(in_period)*100/(power(10::numeric,currency.minor_unit)*latest.units_per_usd))::bigint END,
			CASE WHEN prizes.currency='USD' THEN sum(upcoming)::bigint
			     ELSE round(sum(upcoming)*100/(power(10::numeric,currency.minor_unit)*latest.units_per_usd))::bigint END,
			count(*) FILTER (WHERE in_period>0 OR upcoming>0)::integer
		FROM prizes JOIN currencies currency ON currency.code=prizes.currency
		LEFT JOIN LATERAL (SELECT units_per_usd FROM fx_rates WHERE fx_rates.currency=prizes.currency
			ORDER BY rate_date DESC LIMIT 1) latest ON true
		GROUP BY prizes.currency,currency.minor_unit,latest.units_per_usd
		HAVING sum(in_period)>0 OR sum(upcoming)>0
		ORDER BY prizes.currency`, from)
	if err != nil {
		return report, err
	}
	report.Prizes, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (financePrizeRow, error) {
		var prize financePrizeRow
		err := row.Scan(&prize.Currency, &prize.MinorUnit, &prize.PrizesMinor, &prize.UpcomingMinor,
			&prize.PrizesUSDMinor, &prize.UpcomingUSDMinor, &prize.Competitions)
		return prize, err
	})
	if err != nil {
		return report, err
	}
	for _, prize := range report.Prizes {
		if prize.PrizesUSDMinor != nil {
			report.Totals.PrizesUSDMinor += *prize.PrizesUSDMinor
		}
		if prize.UpcomingUSDMinor != nil {
			report.Totals.UpcomingPrizesUSDMinor += *prize.UpcomingUSDMinor
		}
	}

	rows, err = s.db.Writer.Query(ctx, financeMoneySQL+`SELECT money.kind,money.id::text,money.at,
		COALESCE(profile.handle,player.display_name),competition.name,money.amount_minor,money.currency,money.usd
		FROM money
		JOIN users player ON player.id=money.user_id
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		JOIN competitions competition ON competition.id=money.competition_id
		ORDER BY money.at DESC,money.id DESC LIMIT 25`, from, to)
	if err != nil {
		return report, err
	}
	report.Recent, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (financeMovement, error) {
		var item financeMovement
		err := row.Scan(&item.Kind, &item.ID, &item.At, &item.Player, &item.Competition, &item.AmountMinor,
			&item.Currency, &item.USDMinor)
		return item, err
	})
	if err != nil {
		return report, err
	}
	report.Rates, err = s.loadFXRates(ctx)
	return report, err
}
