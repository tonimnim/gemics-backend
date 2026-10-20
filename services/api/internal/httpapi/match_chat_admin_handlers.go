package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Staff read a match chat only to work a result review or a chat report, and
// every read is audited. Both routes need result_review.manage.

type matchChatStaffPlayer struct {
	UserID string `json:"userId"`
	Handle string `json:"handle"`
}

type matchChatReportView struct {
	ID              string               `json:"id"`
	MatchID         string               `json:"matchId"`
	CompetitionID   string               `json:"competitionId"`
	CompetitionName string               `json:"competitionName"`
	MessageSeq      int64                `json:"messageSeq"`
	MessageKind     string               `json:"messageKind"`
	MessageText     string               `json:"messageText"`
	MessageSentAt   time.Time            `json:"messageSentAt"`
	Reporter        matchChatStaffPlayer `json:"reporter"`
	Reported        matchChatStaffPlayer `json:"reported"`
	Reason          string               `json:"reason"`
	Note            string               `json:"note"`
	Status          string               `json:"status"`
	ResolutionNote  string               `json:"resolutionNote"`
	ResolvedBy      *string              `json:"resolvedBy"`
	ResolvedAt      *time.Time           `json:"resolvedAt"`
	CreatedAt       time.Time            `json:"createdAt"`
}

const matchChatReportColumns = `SELECT report.id::text,report.match_id::text,match.competition_id::text,competition.name,
		report.message_seq,message.kind,COALESCE(message.quick_reply,message.body),message.created_at,
		report.reporter_user_id::text,COALESCE(reporter_profile.handle,reporter.display_name),
		report.reported_user_id::text,COALESCE(reported_profile.handle,reported.display_name),
		report.reason,report.note,report.status,report.resolution_note,report.resolved_by::text,report.resolved_at,
		report.created_at
	FROM match_chat_reports report
	JOIN match_chat_messages message ON message.match_id=report.match_id AND message.seq=report.message_seq
	JOIN matches match ON match.id=report.match_id
	JOIN competitions competition ON competition.id=match.competition_id
	JOIN users reporter ON reporter.id=report.reporter_user_id
	LEFT JOIN player_profiles reporter_profile ON reporter_profile.user_id=report.reporter_user_id
	JOIN users reported ON reported.id=report.reported_user_id
	LEFT JOIN player_profiles reported_profile ON reported_profile.user_id=report.reported_user_id`

func scanMatchChatReports(rows pgx.Rows) ([]matchChatReportView, error) {
	defer rows.Close()
	reports := []matchChatReportView{}
	for rows.Next() {
		var report matchChatReportView
		if err := rows.Scan(&report.ID, &report.MatchID, &report.CompetitionID, &report.CompetitionName,
			&report.MessageSeq, &report.MessageKind, &report.MessageText, &report.MessageSentAt,
			&report.Reporter.UserID, &report.Reporter.Handle, &report.Reported.UserID, &report.Reported.Handle,
			&report.Reason, &report.Note, &report.Status, &report.ResolutionNote, &report.ResolvedBy, &report.ResolvedAt,
			&report.CreatedAt); err != nil {
			return nil, err
		}
		report.MessageSentAt, report.CreatedAt, report.ResolvedAt =
			report.MessageSentAt.UTC(), report.CreatedAt.UTC(), utcTime(report.ResolvedAt)
		reports = append(reports, report)
	}
	return reports, rows.Err()
}

func (s *Server) getAdminMatchChat(w http.ResponseWriter, r *http.Request) {
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var result struct {
		MatchID         string                 `json:"matchId"`
		CompetitionID   string                 `json:"competitionId"`
		CompetitionName string                 `json:"competitionName"`
		State           string                 `json:"state"`
		Home            *matchChatStaffPlayer  `json:"home"`
		Away            *matchChatStaffPlayer  `json:"away"`
		RoomCode        *string                `json:"roomCode"`
		AgreedStartAt   *time.Time             `json:"agreedStartAt"`
		Messages        []matchChatMessageView `json:"messages"`
		Reports         []matchChatReportView  `json:"reports"`
	}
	var homeID, homeHandle, awayID, awayHandle *string
	err := s.db.Writer.QueryRow(r.Context(), `SELECT m.id::text,m.competition_id::text,c.name,m.state,
		home.captain_user_id::text,COALESCE(home_profile.handle,home.display_name),
		away.captain_user_id::text,COALESCE(away_profile.handle,away.display_name),
		chat.room_code,chat.agreed_start_at
		FROM matches m JOIN competitions c ON c.id=m.competition_id
		LEFT JOIN competition_entries home ON home.id=m.home_entry_id
		LEFT JOIN player_profiles home_profile ON home_profile.user_id=home.captain_user_id
		LEFT JOIN competition_entries away ON away.id=m.away_entry_id
		LEFT JOIN player_profiles away_profile ON away_profile.user_id=away.captain_user_id
		LEFT JOIN match_chats chat ON chat.match_id=m.id
		WHERE m.id=$1`, matchID).Scan(&result.MatchID, &result.CompetitionID, &result.CompetitionName, &result.State,
		&homeID, &homeHandle, &awayID, &awayHandle, &result.RoomCode, &result.AgreedStartAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	unavailable := func() {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the match chat.")
	}
	if err != nil {
		unavailable()
		return
	}
	if homeID != nil {
		result.Home = &matchChatStaffPlayer{UserID: *homeID, Handle: valueOrEmpty(homeHandle)}
	}
	if awayID != nil {
		result.Away = &matchChatStaffPlayer{UserID: *awayID, Handle: valueOrEmpty(awayHandle)}
	}
	result.AgreedStartAt = utcTime(result.AgreedStartAt)
	rows, err := s.db.Writer.Query(r.Context(), matchChatMessageColumns+` WHERE message.match_id=$1 ORDER BY message.seq`, matchID)
	if err == nil {
		result.Messages, err = scanMatchChatMessages(rows, "")
	}
	if err == nil {
		rows, err = s.db.Writer.Query(r.Context(), matchChatReportColumns+` WHERE report.match_id=$1
			ORDER BY report.created_at,report.id`, matchID)
	}
	if err == nil {
		result.Reports, err = scanMatchChatReports(rows)
	}
	if err == nil {
		_, err = s.db.Writer.Exec(r.Context(), `INSERT INTO audit_events(actor_user_id,action,subject_type,subject_id,request_id,after_state)
			VALUES ($1::uuid,'match_chat.viewed','match',$2::text,$3,jsonb_build_object('messages',$4::int))`,
			identityFromContext(r.Context()).UserID, matchID, r.Header.Get("X-Request-ID"), len(result.Messages))
	}
	if err != nil {
		unavailable()
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": result})
}

// listMatchChatReports pages open reports oldest first, or resolved ones newest
// first. The cursor is a keyset position, not an authorization token.
func (s *Server) listMatchChatReports(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := query.Get("status")
	if status == "" {
		status = "open"
	}
	if status != "open" && status != "resolved" {
		writeError(w, http.StatusBadRequest, "invalid_status", "status must be open or resolved.")
		return
	}
	limit := 50
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100.")
			return
		}
		limit = parsed
	}
	var cursorAt *time.Time
	var cursorID *string
	if raw := query.Get("cursor"); raw != "" {
		at, id, ok := decodeMatchChatReportCursor(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The cursor is invalid.")
			return
		}
		cursorAt, cursorID = &at, &id
	}
	sql := matchChatReportColumns + ` WHERE report.status='open'
		AND ($1::timestamptz IS NULL OR (report.created_at,report.id)>($1,$2::uuid))
		ORDER BY report.created_at,report.id LIMIT $3`
	if status == "resolved" {
		sql = matchChatReportColumns + ` WHERE report.status<>'open'
			AND ($1::timestamptz IS NULL OR (report.resolved_at,report.id)<($1,$2::uuid))
			ORDER BY report.resolved_at DESC,report.id DESC LIMIT $3`
	}
	rows, err := s.db.Writer.Query(r.Context(), sql, cursorAt, cursorID, limit+1)
	var reports []matchChatReportView
	if err == nil {
		reports, err = scanMatchChatReports(rows)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load chat reports.")
		return
	}
	page := cursorPage{}
	if len(reports) > limit {
		reports, page.HasMore = reports[:limit], true
		last := reports[limit-1]
		at := last.CreatedAt
		if status == "resolved" && last.ResolvedAt != nil {
			at = *last.ResolvedAt
		}
		next := encodeMatchChatReportCursor(at, last.ID)
		page.NextCursor = &next
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": reports, "page": page})
}

func encodeMatchChatReportCursor(at time.Time, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(at.UnixMicro(), 10) + "_" + id))
}

func decodeMatchChatReportCursor(raw string) (time.Time, string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", false
	}
	micros, id, found := strings.Cut(string(decoded), "_")
	at, err := strconv.ParseInt(micros, 10, 64)
	if !found || err != nil || !canonicalUUIDPattern.MatchString(id) {
		return time.Time{}, "", false
	}
	return time.UnixMicro(at).UTC(), id, true
}

type matchChatResolutionInput struct {
	Outcome string `json:"outcome"`
	Note    string `json:"note"`
}

// resolveMatchChatReport records what staff decided. Acting on a report, such
// as suspending the player, is done through the player's own admin routes.
func (s *Server) resolveMatchChatReport(w http.ResponseWriter, r *http.Request) {
	reportID := r.PathValue("id")
	if !canonicalUUIDPattern.MatchString(reportID) {
		writeError(w, http.StatusNotFound, "chat_report_not_found", "Chat report not found.")
		return
	}
	var input matchChatResolutionInput
	if !decodeJSON(w, r, &input) {
		return
	}
	note, _ := cleanChatText(input.Note)
	if (input.Outcome != "dismissed" && input.Outcome != "actioned") || len([]rune(note)) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_resolution",
			"outcome must be dismissed or actioned, with an optional note of up to 500 characters.")
		return
	}
	staffID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to resolve the report.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status FROM match_chat_reports WHERE id=$1 FOR UPDATE`, reportID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "chat_report_not_found", "Chat report not found.")
		return
	}
	if err == nil && status != "open" {
		writeError(w, http.StatusConflict, "chat_report_resolved", "This report has already been resolved.")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE match_chat_reports SET status=$2,resolution_note=$3,resolved_by=$4,
			resolved_at=now() WHERE id=$1`, reportID, input.Outcome, note, staffID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(actor_user_id,action,subject_type,subject_id,request_id,
			before_state,after_state) VALUES ($1::uuid,'match_chat.report_resolved','match_chat_report',$2::text,$3,
			jsonb_build_object('status','open'),jsonb_build_object('status',$4::text))`,
			staffID, reportID, r.Header.Get("X-Request-ID"), input.Outcome)
	}
	var reports []matchChatReportView
	if err == nil {
		var rows pgx.Rows
		if rows, err = tx.Query(r.Context(), matchChatReportColumns+` WHERE report.id=$1`, reportID); err == nil {
			reports, err = scanMatchChatReports(rows)
		}
	}
	if err != nil || len(reports) != 1 || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to resolve the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": reports[0]})
}
