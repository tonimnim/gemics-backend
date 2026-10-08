package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Staff look players up to resolve registration, payment and conduct issues.
// Every staff role can view players; operators and admins can suspend them. A
// suspended player cannot sign in, and every session they hold stops at once.

const adminPlayerCursorKind = "admin_players"

var konamiSearchCleaner = regexp.MustCompile(`[^A-Za-z0-9]+`)

type adminPlayer struct {
	ID            string    `json:"id"`
	Username      *string   `json:"username"`
	DisplayName   string    `json:"displayName"`
	KonamiID      *string   `json:"konamiId"`
	InGameName    *string   `json:"inGameName"`
	Verification  *string   `json:"verification"`
	CountryCode   string    `json:"countryCode"`
	Status        string    `json:"status"`
	StaffRole     *string   `json:"staffRole"`
	HasEmail      bool      `json:"hasEmail"`
	HasPhone      bool      `json:"hasPhone"`
	ActiveStrikes int       `json:"activeStrikes"`
	Competitions  int       `json:"competitions"`
	CreatedAt     time.Time `json:"createdAt"`
}

type adminPlayerDetail struct {
	adminPlayer
	Email           *string                  `json:"email"`
	EmailVerified   bool                     `json:"emailVerified"`
	Phone           *string                  `json:"phone"`
	LastSeenAt      *time.Time               `json:"lastSeenAt"`
	TotalStrikes    int                      `json:"totalStrikes"`
	Suspension      *adminPlayerStatusChange `json:"suspension"`
	CompetitionList []adminPlayerCompetition `json:"competitionList"`
}

// adminPlayerStatusChange is the latest suspension of a suspended player,
// read from the audit log.
type adminPlayerStatusChange struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
	By     *string   `json:"by"`
}

type adminPlayerCompetition struct {
	CompetitionID string    `json:"competitionId"`
	Name          string    `json:"name"`
	Status        string    `json:"status"`
	EntryStatus   string    `json:"entryStatus"`
	Placement     *int      `json:"placement"`
	JoinedAt      time.Time `json:"joinedAt"`
}

type adminPlayerStatusInput struct {
	Reason string `json:"reason"`
}

func (s *Server) registerAdminPlayerRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/admin/players", s.platformRoute(platformPlayerView, s.listAdminPlayers))
	mux.Handle("GET /v1/admin/players/{id}", s.platformRoute(platformPlayerView, s.getAdminPlayer))
	mux.Handle("POST /v1/admin/players/{id}/suspension", s.platformRoute(platformPlayerSuspend, s.suspendPlayer))
	mux.Handle("POST /v1/admin/players/{id}/reactivation", s.platformRoute(platformPlayerSuspend, s.reactivatePlayer))
}

// adminPlayerSelect reads one row of the player list. The Konami ID comes from
// the player's eFootball account, the one they registered with.
const adminPlayerSelect = `SELECT player.id::text,profile.handle,player.display_name,
	account.publisher_player_id,account.in_game_name,account.verification_status,
	player.country_code,player.status,staff.role,
	player.email IS NOT NULL,player.phone_e164 IS NOT NULL,
	(SELECT count(*) FROM player_strikes strike WHERE strike.user_id=player.id AND strike.revoked_at IS NULL)::integer,
	(SELECT count(*) FROM entry_members member WHERE member.user_id=player.id
	 AND member.roster_role IN ('starter','substitute'))::integer,
	player.created_at
	FROM users player
	LEFT JOIN player_profiles profile ON profile.user_id=player.id
	LEFT JOIN LATERAL (SELECT publisher_player_id,in_game_name,verification_status FROM game_accounts
		WHERE user_id=player.id AND game_id=$1
		ORDER BY publisher_player_id IS NULL,created_at LIMIT 1) account ON true
	LEFT JOIN platform_staff_roles staff ON staff.user_id=player.id AND staff.revoked_at IS NULL
	`

func scanAdminPlayer(row pgx.Row, player *adminPlayer) error {
	return row.Scan(&player.ID, &player.Username, &player.DisplayName, &player.KonamiID, &player.InGameName,
		&player.Verification, &player.CountryCode, &player.Status, &player.StaffRole, &player.HasEmail,
		&player.HasPhone, &player.ActiveStrikes, &player.Competitions, &player.CreatedAt)
}

// listAdminPlayers lists every player, newest first. q matches the username,
// display name, in-game name or Konami ID.
func (s *Server) listAdminPlayers(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("q"))
	if utf8.RuneCountInString(search) > 64 {
		writeError(w, http.StatusBadRequest, "invalid_search", "Search with 64 characters or fewer.")
		return
	}
	limit, cursor, ok := s.staffQueuePageInput(w, r, adminPlayerCursorKind, search)
	if !ok {
		return
	}
	text := ""
	if search != "" {
		text = escapeLike(strings.ToLower(search))
	}
	konami := ""
	if cleaned := strings.ToUpper(konamiSearchCleaner.ReplaceAllString(search, "")); len(cleaned) >= 4 {
		konami = cleaned
	}
	var beforeTime *time.Time
	var beforeID *string
	if cursor != nil {
		value := cursorTime(cursor.SortTime)
		beforeTime, beforeID = &value, &cursor.ID
	}
	rows, err := s.db.Writer.Query(r.Context(), adminPlayerSelect+`
		WHERE ($2='' OR lower(profile.handle) LIKE '%'||$2||'%' ESCAPE E'\\'
			OR lower(player.display_name) LIKE '%'||$2||'%' ESCAPE E'\\'
			OR lower(account.in_game_name) LIKE '%'||$2||'%' ESCAPE E'\\'
			OR ($3<>'' AND `+konamiKeyExpression+` LIKE '%'||$3||'%'))
		AND ($4::timestamptz IS NULL OR (player.created_at,player.id)<($4,$5::uuid))
		ORDER BY player.created_at DESC,player.id DESC LIMIT $6`,
		registrationGameID, text, konami, beforeTime, beforeID, limit+1)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load players.")
		return
	}
	players, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adminPlayer, error) {
		var player adminPlayer
		err := scanAdminPlayer(row, &player)
		return player, err
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load players.")
		return
	}
	page := staffQueuePage{}
	if len(players) > limit {
		players = players[:limit]
		page.HasMore = true
		last := players[len(players)-1]
		next, encodeErr := s.encodeStaffQueueCursor(adminPlayerCursorKind, search,
			identityFromContext(r.Context()).UserID, last.CreatedAt, last.ID)
		if encodeErr != nil {
			writeError(w, http.StatusInternalServerError, "internal_error", "Unable to paginate players.")
			return
		}
		page.NextCursor = &next
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": players, "page": page})
}

func (s *Server) getAdminPlayer(w http.ResponseWriter, r *http.Request) {
	playerID := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !uuidPattern.MatchString(playerID) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	detail, err := s.loadAdminPlayer(r.Context(), playerID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the player.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
}

// loadAdminPlayer reads one player with contact details, recent competitions
// and the current suspension. The phone number is masked: staff need to know
// a player has one, not to read it.
func (s *Server) loadAdminPlayer(ctx context.Context, playerID string) (adminPlayerDetail, error) {
	var detail adminPlayerDetail
	if err := scanAdminPlayer(s.db.Writer.QueryRow(ctx, adminPlayerSelect+`WHERE player.id=$2`,
		registrationGameID, playerID), &detail.adminPlayer); err != nil {
		return detail, err
	}
	var phone *string
	err := s.db.Writer.QueryRow(ctx, `SELECT player.email,player.email_verified_at IS NOT NULL,player.phone_e164,
		(SELECT max(last_used_at) FROM refresh_sessions WHERE user_id=player.id),
		(SELECT count(*) FROM player_strikes WHERE user_id=player.id)::integer
		FROM users player WHERE player.id=$1`, playerID).Scan(
		&detail.Email, &detail.EmailVerified, &phone, &detail.LastSeenAt, &detail.TotalStrikes)
	if err != nil {
		return detail, err
	}
	if phone != nil {
		masked := maskPhone(*phone)
		detail.Phone = &masked
	}
	if detail.Status == "suspended" {
		var change adminPlayerStatusChange
		err = s.db.Writer.QueryRow(ctx, `SELECT COALESCE(event.after_state->>'reason',''),event.occurred_at,profile.handle
			FROM audit_events event LEFT JOIN player_profiles profile ON profile.user_id=event.actor_user_id
			WHERE event.subject_type='user' AND event.subject_id=$1 AND event.action='player.suspended'
			ORDER BY event.occurred_at DESC,event.id DESC LIMIT 1`, playerID).Scan(&change.Reason, &change.At, &change.By)
		if err == nil {
			detail.Suspension = &change
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return detail, err
		}
	}
	rows, err := s.db.Writer.Query(ctx, `SELECT competition.id::text,competition.name,competition.status,entry.status,
		placement.placement,member.created_at
		FROM entry_members member
		JOIN competition_entries entry ON entry.id=member.entry_id
		JOIN competitions competition ON competition.id=member.competition_id
		LEFT JOIN competition_entry_placements placement
		  ON placement.competition_id=member.competition_id AND placement.entry_id=member.entry_id
		WHERE member.user_id=$1 AND member.roster_role IN ('starter','substitute')
		ORDER BY member.created_at DESC LIMIT 25`, playerID)
	if err != nil {
		return detail, err
	}
	detail.CompetitionList, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (adminPlayerCompetition, error) {
		var item adminPlayerCompetition
		err := row.Scan(&item.CompetitionID, &item.Name, &item.Status, &item.EntryStatus, &item.Placement, &item.JoinedAt)
		return item, err
	})
	return detail, err
}

// suspendPlayer stops a player from signing in and ends every session they
// hold. Staff cannot suspend themselves or another staff member; remove the
// staff role first.
func (s *Server) suspendPlayer(w http.ResponseWriter, r *http.Request) {
	s.changePlayerStatus(w, r, "active", "suspended", "player.suspended")
}

func (s *Server) reactivatePlayer(w http.ResponseWriter, r *http.Request) {
	s.changePlayerStatus(w, r, "suspended", "active", "player.reactivated")
}

func (s *Server) changePlayerStatus(w http.ResponseWriter, r *http.Request, from, to, action string) {
	playerID := strings.ToLower(strings.TrimSpace(r.PathValue("id")))
	if !uuidPattern.MatchString(playerID) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	var input adminPlayerStatusInput
	if !decodeJSON(w, r, &input) {
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if runes := utf8.RuneCountInString(input.Reason); runes < 10 || runes > 500 {
		writeError(w, http.StatusBadRequest, "invalid_reason", "Give a reason of 10 to 500 characters.")
		return
	}
	ctx := r.Context()
	actor := identityFromContext(ctx).UserID
	if playerID == actor {
		writeError(w, http.StatusConflict, "cannot_change_own_status", "Ask another staff member to do this.")
		return
	}
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the player.")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var status string
	var staff bool
	err = tx.QueryRow(ctx, `SELECT player.status,EXISTS (SELECT 1 FROM platform_staff_roles staff
		WHERE staff.user_id=player.id AND staff.revoked_at IS NULL)
		FROM users player WHERE player.id=$1 FOR UPDATE`, playerID).Scan(&status, &staff)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the player.")
		return
	}
	if to == "suspended" && staff {
		writeError(w, http.StatusConflict, "player_is_staff", "Remove this player's staff role before suspending them.")
		return
	}
	if status != from {
		writeError(w, http.StatusConflict, "player_status_conflict", "This player is "+status+".")
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET status=$2,updated_at=now() WHERE id=$1`, playerID, to); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the player.")
		return
	}
	var sessionIDs []string
	if to == "suspended" {
		rows, queryErr := tx.Query(ctx, `UPDATE refresh_sessions SET revoked_at=now()
			WHERE user_id=$1 AND revoked_at IS NULL RETURNING id::text`, playerID)
		if queryErr == nil {
			sessionIDs, queryErr = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if queryErr == nil {
			queryErr = revokeSessionPushTokens(ctx, tx, sessionIDs)
		}
		err = queryErr
	}
	if err == nil {
		err = writeStaffAudit(r, tx, actor, action, playerID, map[string]any{"status": to, "reason": input.Reason})
	}
	if err != nil || tx.Commit(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the player.")
		return
	}
	if err := s.propagateSessionRevocations(ctx, sessionIDs); err != nil {
		// The sessions are revoked in the database, which every request checks
		// once its cached session state expires.
		s.logger.Warn("cache session revocation", "player_id", playerID, "error", err)
	}
	detail, err := s.loadAdminPlayer(ctx, playerID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the player.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": detail})
}
