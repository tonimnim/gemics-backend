package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The admin dashboard is the one operator tool in V1. Staff sign in with the
// same Konami ID and password as players; their platform role decides what
// the dashboard shows and what the API lets them do.

type staffMember struct {
	UserID      string    `json:"userId"`
	Username    *string   `json:"username"`
	DisplayName string    `json:"displayName"`
	KonamiID    *string   `json:"konamiId"`
	Role        string    `json:"role"`
	GrantedAt   time.Time `json:"grantedAt"`
	GrantedBy   *string   `json:"grantedBy"`
}

type staffGrantInput struct {
	KonamiID string `json:"konamiId"`
	Role     string `json:"role"`
}

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/admin/me", s.requireAuth(http.HandlerFunc(s.getStaffSelf)))
	mux.Handle("GET /v1/admin/overview", s.platformRoute(platformOverviewView, s.getAdminOverview))
	mux.Handle("GET /v1/admin/staff", s.platformRoute(platformStaffManage, s.listStaff))
	mux.Handle("POST /v1/admin/staff", s.platformRoute(platformStaffManage, s.grantStaffRole))
	mux.Handle("DELETE /v1/admin/staff/{userId}", s.platformRoute(platformStaffManage, s.revokeStaffRole))
}

// getStaffSelf tells the dashboard who is signed in and what they may do. A
// player without a staff role gets 403, which the dashboard shows as "no
// access" rather than a sign-in error.
func (s *Server) getStaffSelf(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	role, err := s.staffRole(r.Context(), userID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify staff access.")
		return
	}
	if role == "" {
		writeError(w, http.StatusForbidden, "platform_access_denied", "This account does not have Gamics staff access.")
		return
	}
	var username *string
	var displayName string
	if err := s.db.Writer.QueryRow(r.Context(), `SELECT profile.handle,player.display_name FROM users player
		LEFT JOIN player_profiles profile ON profile.user_id=player.id WHERE player.id=$1`, userID).
		Scan(&username, &displayName); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the staff account.")
		return
	}
	permissions := []string{}
	for _, permission := range platformRolePermissions[role] {
		permissions = append(permissions, string(permission))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"userId": userID, "username": username, "displayName": displayName, "role": role,
		"permissions": permissions, "gamicsOrganizationId": gamicsOrganizationID,
	})
}

type overviewFinance struct {
	PaymentReviews    int   `json:"paymentReviews"`
	Refunds           int   `json:"refunds"`
	SucceededLast30d  int   `json:"succeededLast30Days"`
	CollectedMinor30d int64 `json:"collectedMinorLast30Days"`
	// Unconverted30d counts the period's payments still waiting for an
	// exchange rate, which CollectedMinor30d leaves out.
	Unconverted30d int    `json:"unconvertedLast30Days"`
	Currency       string `json:"currency"`
}

// getAdminOverview returns the counts on the dashboard home page: the size of
// the platform and every queue waiting on staff. Money (revenue, payment
// reviews and refunds) is included only for staff who may see finance, and is
// otherwise null.
func (s *Server) getAdminOverview(w http.ResponseWriter, r *http.Request) {
	var overview struct {
		Players struct {
			Total     int `json:"total"`
			NewLast7d int `json:"newLast7Days"`
		} `json:"players"`
		Competitions struct {
			RegistrationOpen int `json:"registrationOpen"`
			Running          int `json:"running"`
			Draft            int `json:"draft"`
		} `json:"competitions"`
		Queues struct {
			ResultReviews        int `json:"resultReviews"`
			AccountVerifications int `json:"accountVerifications"`
		} `json:"queues"`
		// Daily counts for the last 14 days, oldest first, in Nairobi days.
		PlayersByDay       []overviewDay    `json:"playersByDay"`
		RegistrationsByDay []overviewDay    `json:"registrationsByDay"`
		CompetitionStatus  map[string]int   `json:"competitionsByStatus"`
		Finance            *overviewFinance `json:"finance"`
	}
	err := s.db.Writer.QueryRow(r.Context(), `SELECT
		(SELECT count(*) FROM users WHERE status='active'),
		(SELECT count(*) FROM users WHERE status='active' AND created_at>now()-interval '7 days'),
		(SELECT count(*) FROM competitions WHERE status IN ('published','registration_open')),
		(SELECT count(*) FROM competitions WHERE status IN ('check_in','running')),
		(SELECT count(*) FROM competitions WHERE status='draft'),
		(SELECT count(*) FROM match_result_reviews WHERE status='queued'),
		(SELECT count(*) FROM game_account_verification_requests WHERE status IN ('requested','under_review'))`).Scan(
		&overview.Players.Total, &overview.Players.NewLast7d,
		&overview.Competitions.RegistrationOpen, &overview.Competitions.Running, &overview.Competitions.Draft,
		&overview.Queues.ResultReviews, &overview.Queues.AccountVerifications)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the overview.")
		return
	}
	if overview.PlayersByDay, err = s.overviewSeries(r, "users", "status='active'"); err == nil {
		overview.RegistrationsByDay, err = s.overviewSeries(r, "competition_entries", "true")
	}
	if err == nil {
		overview.CompetitionStatus, err = s.competitionStatusCounts(r)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the overview.")
		return
	}
	finance, err := s.staffCan(r.Context(), identityFromContext(r.Context()).UserID, platformFinanceView)
	if err == nil && finance {
		// Payments arrive in many currencies; the total is their frozen USD
		// values, never a sum of mixed amounts.
		overview.Finance = &overviewFinance{Currency: reportingCurrency}
		err = s.db.Writer.QueryRow(r.Context(), `SELECT
			(SELECT count(*) FROM payment_intents WHERE status='review'),
			(SELECT count(*) FROM payment_refunds WHERE status=ANY($1::text[])),
			(SELECT count(*) FROM payment_intents WHERE status='succeeded' AND completed_at>now()-interval '30 days'),
			(SELECT COALESCE(sum(amount_usd_minor),0) FROM payment_intents
			 WHERE status='succeeded' AND completed_at>now()-interval '30 days'),
			(SELECT count(*) FROM payment_intents
			 WHERE status='succeeded' AND completed_at>now()-interval '30 days' AND amount_usd_minor IS NULL)`,
			refundStages["action"]).Scan(
			&overview.Finance.PaymentReviews, &overview.Finance.Refunds,
			&overview.Finance.SucceededLast30d, &overview.Finance.CollectedMinor30d, &overview.Finance.Unconverted30d)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the overview.")
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

type overviewDay struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// overviewSeries counts rows created on each of the last 14 days. Days are
// Nairobi calendar days, where Gamics launched; table and filter are fixed
// strings from this file, never input.
func (s *Server) overviewSeries(r *http.Request, table, filter string) ([]overviewDay, error) {
	rows, err := s.db.Writer.Query(r.Context(), `WITH days AS (
			SELECT generate_series((now() AT TIME ZONE 'Africa/Nairobi')::date-13,
				(now() AT TIME ZONE 'Africa/Nairobi')::date, interval '1 day')::date AS day)
		SELECT to_char(days.day,'YYYY-MM-DD'),count(created.created_at)::integer FROM days
		LEFT JOIN (SELECT created_at FROM `+table+` WHERE `+filter+`
			AND created_at>=now()-interval '15 days') created
		  ON (created.created_at AT TIME ZONE 'Africa/Nairobi')::date=days.day
		GROUP BY days.day ORDER BY days.day`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (overviewDay, error) {
		var day overviewDay
		err := row.Scan(&day.Date, &day.Count)
		return day, err
	})
}

func (s *Server) competitionStatusCounts(r *http.Request) (map[string]int, error) {
	rows, err := s.db.Writer.Query(r.Context(), `SELECT status,count(*)::integer FROM competitions GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func (s *Server) listStaff(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Writer.Query(r.Context(), `SELECT player.id::text,profile.handle,player.display_name,
		(SELECT account.publisher_player_id FROM game_accounts account
		 WHERE account.user_id=player.id AND account.game_id=$1 AND account.publisher_player_id IS NOT NULL
		 ORDER BY account.created_at LIMIT 1),
		staff.role,staff.granted_at,granter_profile.handle
		FROM platform_staff_roles staff
		JOIN users player ON player.id=staff.user_id AND player.status='active'
		LEFT JOIN player_profiles profile ON profile.user_id=player.id
		LEFT JOIN player_profiles granter_profile ON granter_profile.user_id=staff.granted_by
		WHERE staff.revoked_at IS NULL
		ORDER BY array_position($2::text[],staff.role) DESC,staff.granted_at`, registrationGameID, platformRoles)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load staff.")
		return
	}
	data, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (staffMember, error) {
		var member staffMember
		err := row.Scan(&member.UserID, &member.Username, &member.DisplayName, &member.KonamiID,
			&member.Role, &member.GrantedAt, &member.GrantedBy)
		return member, err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load staff.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "roles": platformRoles})
}

// grantStaffRole gives a registered player a staff role, or changes it. Staff
// cannot change their own role, so at least one admin always remains.
func (s *Server) grantStaffRole(w http.ResponseWriter, r *http.Request) {
	var input staffGrantInput
	if !decodeJSON(w, r, &input) {
		return
	}
	role := strings.ToLower(strings.TrimSpace(input.Role))
	if !slices.Contains(platformRoles, role) {
		writeError(w, http.StatusBadRequest, "invalid_role", "Role must be support, reviewer, operator or admin.")
		return
	}
	_, key, ok := normalizeKonamiID(input.KonamiID)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_konami_id", "Enter the player's Konami ID.")
		return
	}
	userID, status, _, err := s.lookupKonamiAccount(r.Context(), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to find the player.")
		return
	}
	if userID == "" || status != "active" {
		writeError(w, http.StatusNotFound, "player_not_found", "No active player has that Konami ID. They must register first.")
		return
	}
	actor := identityFromContext(r.Context()).UserID
	if userID == actor {
		writeError(w, http.StatusConflict, "cannot_change_own_role", "Ask another admin to change your role.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to grant the role.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	_, err = tx.Exec(r.Context(), `INSERT INTO platform_staff_roles(user_id,role,granted_by) VALUES ($1,$2,$3)
		ON CONFLICT (user_id) DO UPDATE SET role=EXCLUDED.role,granted_by=EXCLUDED.granted_by,
		granted_at=now(),revoked_at=NULL`, userID, role, actor)
	if err == nil {
		err = writeStaffAudit(r, tx, actor, "staff.role_granted", userID, map[string]any{"role": role})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to grant the role.")
		return
	}
	s.listStaff(w, r)
}

func (s *Server) revokeStaffRole(w http.ResponseWriter, r *http.Request) {
	userID := strings.TrimSpace(r.PathValue("userId"))
	if !uuidPattern.MatchString(userID) {
		writeError(w, http.StatusBadRequest, "invalid_user_id", "User ID is invalid.")
		return
	}
	actor := identityFromContext(r.Context()).UserID
	if userID == actor {
		writeError(w, http.StatusConflict, "cannot_change_own_role", "Ask another admin to remove your role.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to remove the role.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var role string
	err = tx.QueryRow(r.Context(), `UPDATE platform_staff_roles SET revoked_at=now()
		WHERE user_id=$1 AND revoked_at IS NULL RETURNING role`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "staff_not_found", "That account has no staff role.")
		return
	}
	if err == nil {
		err = writeStaffAudit(r, tx, actor, "staff.role_revoked", userID, map[string]any{"role": role})
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to remove the role.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeStaffAudit(r *http.Request, tx pgx.Tx, actor, action, subject string, state map[string]any) error {
	after, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO audit_events
		(actor_user_id,action,subject_type,subject_id,request_id,after_state)
		VALUES ($1,$2,'user',$3,$4,$5::jsonb)`, actor, action, subject, r.Header.Get("X-Request-ID"), string(after))
	return err
}
