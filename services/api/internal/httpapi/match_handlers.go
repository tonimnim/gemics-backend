package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultMatchPageSize = 20
	maximumMatchPageSize = 50
	matchCursorVersion   = 1
)

var canonicalUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type matchParticipantResponse struct {
	PlayerID    string     `json:"playerId"`
	Handle      string     `json:"handle"`
	DisplayName string     `json:"displayName"`
	Initials    string     `json:"initials"`
	AvatarURL   *string    `json:"avatarUrl,omitempty"`
	Side        string     `json:"side"`
	CheckedInAt *time.Time `json:"checkedInAt,omitempty"`
}

type friendMatchInstruction struct {
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type matchRoomResponse struct {
	ID                      string                    `json:"id"`
	Code                    string                    `json:"code"`
	CompetitionID           string                    `json:"competitionId"`
	CompetitionName         string                    `json:"competitionName"`
	GameID                  string                    `json:"gameId"`
	GameName                string                    `json:"gameName"`
	StageID                 string                    `json:"stageId"`
	StageName               string                    `json:"stageName"`
	RoundName               string                    `json:"roundName"`
	RoundNumber             int                       `json:"roundNumber"`
	MatchNumber             int                       `json:"matchNumber"`
	Bracket                 string                    `json:"bracket"`
	BestOf                  int16                     `json:"bestOf"`
	DrawAllowed             bool                      `json:"drawAllowed"`
	ScheduledAt             *time.Time                `json:"scheduledAt"`
	CheckInOpensAt          *time.Time                `json:"checkInOpensAt"`
	CheckInClosesAt         *time.Time                `json:"checkInClosesAt"`
	ResultDueAt             *time.Time                `json:"resultDueAt"`
	CompletedAt             *time.Time                `json:"completedAt"`
	State                   string                    `json:"state"`
	Lifecycle               string                    `json:"lifecycle"`
	Version                 int                       `json:"version"`
	CurrentPlayerID         string                    `json:"currentPlayerId"`
	CurrentPlayerSide       string                    `json:"currentPlayerSide"`
	Home                    *matchParticipantResponse `json:"home"`
	Away                    *matchParticipantResponse `json:"away"`
	PendingResult           *matchPendingResult       `json:"pendingResult,omitempty"`
	AllowedActions          []string                  `json:"allowedActions"`
	FriendMatchInstructions []friendMatchInstruction  `json:"friendMatchInstructions"`
}

type matchPendingResult struct {
	ID          string              `json:"id"`
	SubmittedBy string              `json:"submittedBy"`
	HomeScore   int                 `json:"homeScore"`
	AwayScore   int                 `json:"awayScore"`
	Tiebreak    *tiebreakScoreInput `json:"tiebreak,omitempty"`
	EvidenceIDs []string            `json:"evidenceIds"`
	Status      string              `json:"status"`
	SubmittedAt time.Time           `json:"submittedAt"`
}

type matchSummaryResponse struct {
	ID                string                    `json:"id"`
	Code              string                    `json:"code"`
	CompetitionID     string                    `json:"competitionId"`
	CompetitionName   string                    `json:"competitionName"`
	GameID            string                    `json:"gameId"`
	GameName          string                    `json:"gameName"`
	RoundName         string                    `json:"roundName"`
	ScheduledAt       *time.Time                `json:"scheduledAt"`
	CheckInClosesAt   *time.Time                `json:"checkInClosesAt"`
	State             string                    `json:"state"`
	Lifecycle         string                    `json:"lifecycle"`
	Version           int                       `json:"version"`
	CurrentPlayerSide string                    `json:"currentPlayerSide"`
	Home              *matchParticipantResponse `json:"home"`
	Away              *matchParticipantResponse `json:"away"`
	AllowedActions    []string                  `json:"allowedActions"`
}

type matchRecord struct {
	ID                          string
	CompetitionID               string
	CompetitionName             string
	GameID                      string
	GameName                    string
	StageID                     string
	StageName                   string
	StageFormat                 string
	BestOf                      int16
	StageConfig                 []byte
	RulesSnapshot               []byte
	Bracket                     string
	RoundNumber                 int
	MatchNumber                 int
	State                       string
	ScheduledAt                 *time.Time
	CheckInOpensAt              *time.Time
	CheckInClosesAt             *time.Time
	ResultDueAt                 *time.Time
	CompletedAt                 *time.Time
	Version                     int
	SortAt                      time.Time
	CurrentEntryID              string
	CurrentSide                 string
	HomePlayerID                *string
	HomeHandle                  *string
	HomeDisplayName             *string
	HomeCheckedInAt             *time.Time
	AwayPlayerID                *string
	AwayHandle                  *string
	AwayDisplayName             *string
	AwayCheckedInAt             *time.Time
	SubmissionID                *string
	SubmittedBy                 *string
	SubmissionState             *string
	SubmissionHomeScore         *int
	SubmissionAwayScore         *int
	SubmissionTiebreakType      *string
	SubmissionHomeTiebreakScore *int
	SubmissionAwayTiebreakScore *int
	SubmissionEvidence          []byte
	SubmissionCreatedAt         *time.Time
}

type matchCursor struct {
	Version int    `json:"v"`
	State   string `json:"state"`
	SortAt  int64  `json:"sortAt"`
	ID      string `json:"id"`
}

type matchPageResponse struct {
	Data []matchSummaryResponse `json:"data"`
	Page cursorPage             `json:"page"`
}

type cursorPage struct {
	NextCursor *string `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

type matchQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

const matchSelectColumns = `
	SELECT m.id::text,m.competition_id::text,c.name,c.game_id,g.name,
		m.stage_id::text,stage.name,stage.format,stage.best_of,stage.config,c.rules_snapshot,
		m.bracket,m.round_number,m.match_number,m.state,m.scheduled_at,
		m.check_in_opens_at,m.check_in_closes_at,m.result_due_at,m.completed_at,m.version,
		COALESCE(m.completed_at,m.scheduled_at,m.created_at),mine.entry_id::text,
		CASE WHEN mine.entry_id=m.home_entry_id THEN 'home' ELSE 'away' END,
		home_player.user_id,home_player.handle,home_player.display_name,home_checkin.checked_in_at,
		away_player.user_id,away_player.handle,away_player.display_name,away_checkin.checked_in_at,
		latest_submission.id,latest_submission.submitted_by,latest_submission.status,
		latest_submission.home_score,latest_submission.away_score,latest_submission.tiebreak_type,
		latest_submission.home_tiebreak_score,latest_submission.away_tiebreak_score,
		latest_submission.evidence_objects,latest_submission.submitted_at
	FROM matches m
	JOIN competitions c ON c.id=m.competition_id
	JOIN games g ON g.id=c.game_id
	JOIN competition_stages stage ON stage.id=m.stage_id
	JOIN entry_members mine ON mine.user_id=$1
		AND mine.roster_role IN ('starter','substitute')
		AND (mine.entry_id=m.home_entry_id OR mine.entry_id=m.away_entry_id)
	LEFT JOIN competition_entries home_entry ON home_entry.id=m.home_entry_id
	LEFT JOIN competition_entries away_entry ON away_entry.id=m.away_entry_id
	LEFT JOIN LATERAL (
		SELECT member.user_id::text,
			COALESCE(profile.handle,account.in_game_name,player.display_name) AS handle,
			player.display_name
		FROM entry_members member
		JOIN users player ON player.id=member.user_id
		JOIN game_accounts account ON account.id=member.game_account_id
		LEFT JOIN player_profiles profile ON profile.user_id=member.user_id
		WHERE member.entry_id=m.home_entry_id AND member.roster_role IN ('starter','substitute')
		ORDER BY (member.user_id=home_entry.captain_user_id) DESC,
			CASE member.roster_role WHEN 'starter' THEN 0 ELSE 1 END,member.created_at,member.user_id
		LIMIT 1
	) home_player ON true
	LEFT JOIN LATERAL (
		SELECT member.user_id::text,
			COALESCE(profile.handle,account.in_game_name,player.display_name) AS handle,
			player.display_name
		FROM entry_members member
		JOIN users player ON player.id=member.user_id
		JOIN game_accounts account ON account.id=member.game_account_id
		LEFT JOIN player_profiles profile ON profile.user_id=member.user_id
		WHERE member.entry_id=m.away_entry_id AND member.roster_role IN ('starter','substitute')
		ORDER BY (member.user_id=away_entry.captain_user_id) DESC,
			CASE member.roster_role WHEN 'starter' THEN 0 ELSE 1 END,member.created_at,member.user_id
		LIMIT 1
	) away_player ON true
	LEFT JOIN match_check_ins home_checkin ON home_checkin.match_id=m.id AND home_checkin.entry_id=m.home_entry_id
	LEFT JOIN match_check_ins away_checkin ON away_checkin.match_id=m.id AND away_checkin.entry_id=m.away_entry_id
	LEFT JOIN LATERAL (
		SELECT submission.id::text,submission.submitted_by::text,submission.status,
			submission.home_score,submission.away_score,submission.tiebreak_type,
			submission.home_tiebreak_score,submission.away_tiebreak_score,
			submission.evidence_objects,submission.submitted_at
		FROM result_submissions submission
		WHERE submission.match_id=m.id AND submission.status<>'superseded'
		ORDER BY submission.submitted_at DESC,submission.id DESC LIMIT 1
	) latest_submission ON true
`

func (s *Server) listMyMatches(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	state, limit, cursor, ok := s.parseMatchPageRequest(w, r)
	if !ok {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	records, err := s.queryMatchPage(r.Context(), s.db.Reader, userID, state, limit+1, cursor)
	if err != nil && s.db.Reader != s.db.Writer {
		s.logger.Warn("reader query failed; falling back to writer", "operation", "list_my_matches", "error", err)
		records, err = s.queryMatchPage(r.Context(), s.db.Writer, userID, state, limit+1, cursor)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load matches.")
		return
	}

	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	now := time.Now().UTC()
	data := make([]matchSummaryResponse, 0, len(records))
	for _, record := range records {
		room := record.response(userID, now)
		data = append(data, summarizeMatch(room))
	}
	var nextCursor *string
	if hasMore && len(records) > 0 {
		value := s.encodeMatchCursor(state, records[len(records)-1])
		nextCursor = &value
	}
	writeJSON(w, http.StatusOK, matchPageResponse{Data: data, Page: cursorPage{NextCursor: nextCursor, HasMore: hasMore}})
}

func (s *Server) getMatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	record, err := loadMatchRecord(r.Context(), s.db.Reader, userID, matchID, false)
	if err != nil && s.db.Reader != s.db.Writer {
		record, err = loadMatchRecord(r.Context(), s.db.Writer, userID, matchID, false)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Missing and unauthorized matches deliberately have the same response.
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the match.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": record.response(userID, time.Now().UTC())})
}

func (s *Server) checkInMatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	idempotencyKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if !validIdempotencyKey(idempotencyKey) {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Provide an Idempotency-Key between 8 and 128 printable characters.")
		return
	}

	userID := identityFromContext(r.Context()).UserID
	scope := "match_check_in:" + userID
	digest := sha256.Sum256([]byte("POST:/v1/matches/" + strings.ToLower(matchID) + "/check-ins"))
	requestHash := hex.EncodeToString(digest[:])
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	if _, err = tx.Exec(r.Context(), `DELETE FROM idempotency_keys
		WHERE scope=$1 AND key=$2 AND expires_at<=now()`, scope, idempotencyKey); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
		return
	}
	inserted, err := tx.Exec(r.Context(), `INSERT INTO idempotency_keys(scope,key,request_hash,expires_at)
		VALUES ($1,$2,$3,now()+interval '24 hours') ON CONFLICT DO NOTHING`, scope, idempotencyKey, requestHash)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
		return
	}
	newIdempotencyKey := inserted.RowsAffected() == 1
	if !newIdempotencyKey {
		var storedHash string
		var storedStatus *int
		var storedBody []byte
		err = tx.QueryRow(r.Context(), `SELECT request_hash,response_status,response_body
			FROM idempotency_keys WHERE scope=$1 AND key=$2 FOR UPDATE`, scope, idempotencyKey).
			Scan(&storedHash, &storedStatus, &storedBody)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
			return
		}
		if subtle.ConstantTimeCompare([]byte(storedHash), []byte(requestHash)) != 1 {
			writeError(w, http.StatusConflict, "idempotency_conflict", "That Idempotency-Key was used for another match.")
			return
		}
		if storedStatus == nil || len(storedBody) == 0 {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusConflict, "request_in_progress", "The original check-in is still being processed.")
			return
		}
		if err := tx.Commit(r.Context()); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
			return
		}
		w.Header().Set("Idempotency-Replayed", "true")
		writeRawJSON(w, *storedStatus, storedBody)
		return
	}

	record, err := loadMatchRecord(r.Context(), tx, userID, matchID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
		return
	}
	now := time.Now().UTC()
	if record.currentCheckedInAt() != nil {
		// The side is already checked in. Avoid persisting unlimited redundant keys,
		// while returning the authoritative current room as an idempotent success.
		if _, err := tx.Exec(r.Context(), `DELETE FROM idempotency_keys WHERE scope=$1 AND key=$2`, scope, idempotencyKey); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
			return
		}
		body, err := json.Marshal(map[string]any{"data": record.response(userID, now)})
		if err != nil || tx.Commit(r.Context()) != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
			return
		}
		writeRawJSON(w, http.StatusOK, body)
		return
	}
	if code, message, retryAfter := record.checkInRejection(now); code != "" {
		if retryAfter > 0 {
			seconds := max(int64(1), int64((retryAfter+time.Second-1)/time.Second))
			w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
		}
		writeError(w, http.StatusConflict, code, message)
		return
	}

	_, err = tx.Exec(r.Context(), `INSERT INTO match_check_ins
		(match_id,entry_id,checked_in_by,match_version,idempotency_key,checked_in_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, matchID, record.CurrentEntryID, userID, record.Version, idempotencyKey, now)
	if err != nil {
		writeError(w, http.StatusConflict, "check_in_conflict", "The match check-in changed. Refresh the match and try again.")
		return
	}
	nextState := "ready"
	if record.opponentCheckedInAt() != nil {
		nextState = "in_progress"
	}
	tag, updateErr := tx.Exec(r.Context(), `UPDATE matches
		SET state=$3,version=version+1,updated_at=now()
		WHERE id=$1 AND state='ready' AND version=$2`, matchID, record.Version, nextState)
	if updateErr != nil || tag.RowsAffected() != 1 {
		writeError(w, http.StatusConflict, "match_version_conflict", "The match changed. Refresh it and try again.")
		return
	}

	updated, err := loadMatchRecord(r.Context(), tx, userID, matchID, true)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the checked-in match.")
		return
	}
	room := updated.response(userID, now)
	body, err := json.Marshal(map[string]any{"data": room})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Unable to complete check-in.")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE idempotency_keys
		SET response_status=$3,response_body=$4::jsonb WHERE scope=$1 AND key=$2`,
		scope, idempotencyKey, http.StatusOK, string(body)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"matchId": matchID, "entryId": record.CurrentEntryID, "userId": userID,
		"checkedInAt": now, "matchVersion": room.Version,
	})
	if _, err = tx.Exec(r.Context(), `INSERT INTO audit_events
		(actor_user_id,action,subject_type,subject_id,request_id,after_state)
		VALUES ($1,'match.check_in','match',$2,$3,$4::jsonb)`,
		userID, matchID, r.Header.Get("X-Request-ID"), string(payload)); err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO outbox_events
			(aggregate_type,aggregate_id,event_type,payload)
			VALUES ('match',$1,'match.participant_checked_in',$2::jsonb)`, matchID, string(payload))
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check in.")
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) parseMatchPageRequest(w http.ResponseWriter, r *http.Request) (string, int, *matchCursor, bool) {
	state := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("state")))
	if state == "" {
		state = "active"
	}
	if state != "active" && state != "history" {
		writeError(w, http.StatusBadRequest, "invalid_match_state", "State must be active or history.")
		return "", 0, nil, false
	}
	limit := defaultMatchPageSize
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumMatchPageSize {
			writeError(w, http.StatusBadRequest, "invalid_limit", "Limit must be between 1 and 50.")
			return "", 0, nil, false
		}
		limit = parsed
	}
	var cursor *matchCursor
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		decoded, err := s.decodeMatchCursor(raw, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "The match cursor is invalid or expired.")
			return "", 0, nil, false
		}
		cursor = &decoded
	}
	return state, limit, cursor, true
}

func (s *Server) queryMatchPage(ctx context.Context, pool *pgxpool.Pool, userID, state string, limit int, cursor *matchCursor) ([]matchRecord, error) {
	if pool == nil {
		return nil, errors.New("database pool is unavailable")
	}
	stateClause := "m.state IN ('pending','ready','in_progress','awaiting_confirmation','disputed')"
	comparison := ">"
	direction := "ASC"
	if state == "history" {
		stateClause = "m.state IN ('completed','forfeit','cancelled')"
		comparison = "<"
		direction = "DESC"
	}
	var cursorAt any
	var cursorID any
	if cursor != nil {
		cursorAt = time.Unix(0, cursor.SortAt).UTC()
		cursorID = cursor.ID
	}
	query := matchSelectColumns + `
		WHERE ` + stateClause + `
		AND ($2::timestamptz IS NULL OR
			(COALESCE(m.completed_at,m.scheduled_at,m.created_at),m.id) ` + comparison + ` ($2::timestamptz,$3::uuid))
		ORDER BY COALESCE(m.completed_at,m.scheduled_at,m.created_at) ` + direction + `,m.id ` + direction + ` LIMIT $4`
	return scanMatchRecords(ctx, pool, query, userID, cursorAt, cursorID, limit)
}

func loadMatchRecord(ctx context.Context, queryer matchQueryer, userID, matchID string, forUpdate bool) (matchRecord, error) {
	query := matchSelectColumns + ` WHERE m.id=$2::uuid`
	if forUpdate {
		query += ` FOR UPDATE OF m`
	}
	records, err := scanMatchRecords(ctx, queryer, query, userID, matchID)
	if err != nil {
		return matchRecord{}, err
	}
	if len(records) == 0 {
		return matchRecord{}, pgx.ErrNoRows
	}
	return records[0], nil
}

func scanMatchRecords(ctx context.Context, queryer matchQueryer, query string, args ...any) ([]matchRecord, error) {
	rows, err := queryer.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]matchRecord, 0)
	for rows.Next() {
		var record matchRecord
		if err := rows.Scan(
			&record.ID, &record.CompetitionID, &record.CompetitionName, &record.GameID, &record.GameName,
			&record.StageID, &record.StageName, &record.StageFormat, &record.BestOf, &record.StageConfig, &record.RulesSnapshot,
			&record.Bracket, &record.RoundNumber, &record.MatchNumber, &record.State, &record.ScheduledAt,
			&record.CheckInOpensAt, &record.CheckInClosesAt, &record.ResultDueAt, &record.CompletedAt, &record.Version,
			&record.SortAt, &record.CurrentEntryID, &record.CurrentSide,
			&record.HomePlayerID, &record.HomeHandle, &record.HomeDisplayName, &record.HomeCheckedInAt,
			&record.AwayPlayerID, &record.AwayHandle, &record.AwayDisplayName, &record.AwayCheckedInAt,
			&record.SubmissionID, &record.SubmittedBy, &record.SubmissionState,
			&record.SubmissionHomeScore, &record.SubmissionAwayScore, &record.SubmissionTiebreakType,
			&record.SubmissionHomeTiebreakScore, &record.SubmissionAwayTiebreakScore,
			&record.SubmissionEvidence, &record.SubmissionCreatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (record matchRecord) response(userID string, now time.Time) matchRoomResponse {
	opensAt, closesAt := record.checkInWindow()
	lifecycle, actions := record.presentation(userID, now, opensAt, closesAt)
	settings := resolveMatchSettings(record.GameID, record.StageFormat, record.RulesSnapshot, record.StageConfig)
	roundName := fmt.Sprintf("Round %d", record.RoundNumber)
	if record.StageFormat == "round_robin" {
		roundName = fmt.Sprintf("Matchday %d", record.RoundNumber)
	}
	room := matchRoomResponse{
		ID: record.ID, Code: fmt.Sprintf("R%02d-M%02d", record.RoundNumber, record.MatchNumber),
		CompetitionID: record.CompetitionID, CompetitionName: record.CompetitionName,
		GameID: record.GameID, GameName: record.GameName, StageID: record.StageID, StageName: record.StageName,
		RoundName: roundName, RoundNumber: record.RoundNumber, MatchNumber: record.MatchNumber,
		Bracket: record.Bracket, BestOf: record.BestOf, DrawAllowed: settings.DrawAllowed,
		ScheduledAt: utcTime(record.ScheduledAt), CheckInOpensAt: utcTime(opensAt), CheckInClosesAt: utcTime(closesAt),
		ResultDueAt: utcTime(record.ResultDueAt), CompletedAt: utcTime(record.CompletedAt),
		State: record.State, Lifecycle: lifecycle, Version: record.Version,
		CurrentPlayerID: userID, CurrentPlayerSide: record.CurrentSide,
		Home:           participantResponse(record.HomePlayerID, record.HomeHandle, record.HomeDisplayName, "home", record.HomeCheckedInAt),
		Away:           participantResponse(record.AwayPlayerID, record.AwayHandle, record.AwayDisplayName, "away", record.AwayCheckedInAt),
		AllowedActions: actions, FriendMatchInstructions: settings.Instructions,
	}
	if record.SubmissionID != nil && record.SubmittedBy != nil && record.SubmissionState != nil &&
		record.SubmissionHomeScore != nil && record.SubmissionAwayScore != nil && record.SubmissionCreatedAt != nil {
		pending := &matchPendingResult{
			ID: *record.SubmissionID, SubmittedBy: *record.SubmittedBy,
			HomeScore: *record.SubmissionHomeScore, AwayScore: *record.SubmissionAwayScore,
			Status: *record.SubmissionState, SubmittedAt: record.SubmissionCreatedAt.UTC(), EvidenceIDs: []string{},
		}
		_ = json.Unmarshal(record.SubmissionEvidence, &pending.EvidenceIDs)
		if record.SubmissionTiebreakType != nil && record.SubmissionHomeTiebreakScore != nil && record.SubmissionAwayTiebreakScore != nil {
			pending.Tiebreak = &tiebreakScoreInput{
				Type:      *record.SubmissionTiebreakType,
				HomeScore: *record.SubmissionHomeTiebreakScore,
				AwayScore: *record.SubmissionAwayTiebreakScore,
			}
		}
		room.PendingResult = pending
	}
	return room
}

func summarizeMatch(room matchRoomResponse) matchSummaryResponse {
	return matchSummaryResponse{
		ID: room.ID, Code: room.Code, CompetitionID: room.CompetitionID, CompetitionName: room.CompetitionName,
		GameID: room.GameID, GameName: room.GameName, RoundName: room.RoundName,
		ScheduledAt: room.ScheduledAt, CheckInClosesAt: room.CheckInClosesAt,
		State: room.State, Lifecycle: room.Lifecycle, Version: room.Version,
		CurrentPlayerSide: room.CurrentPlayerSide, Home: room.Home, Away: room.Away,
		AllowedActions: room.AllowedActions,
	}
}

func participantResponse(playerID, handle, displayName *string, side string, checkedInAt *time.Time) *matchParticipantResponse {
	if playerID == nil || handle == nil || displayName == nil {
		return nil
	}
	return &matchParticipantResponse{
		PlayerID: *playerID, Handle: *handle, DisplayName: *displayName,
		Initials: playerInitials(*displayName), Side: side, CheckedInAt: utcTime(checkedInAt),
	}
}

func playerInitials(name string) string {
	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "?"
	}
	initials := []rune(strings.ToUpper(parts[0]))[:1]
	if len(parts) > 1 {
		initials = append(initials, []rune(strings.ToUpper(parts[len(parts)-1]))[0])
	}
	return string(initials)
}

func (record matchRecord) checkInWindow() (*time.Time, *time.Time) {
	opensAt := record.CheckInOpensAt
	closesAt := record.CheckInClosesAt
	if (opensAt == nil || closesAt == nil) && record.ScheduledAt != nil {
		open := record.ScheduledAt.Add(-15 * time.Minute)
		closeAt := record.ScheduledAt.Add(10 * time.Minute)
		opensAt, closesAt = &open, &closeAt
	}
	return opensAt, closesAt
}

func (record matchRecord) presentation(userID string, now time.Time, opensAt, closesAt *time.Time) (string, []string) {
	actions := make([]string, 0, 2)
	switch record.State {
	case "pending":
		return "assigned", actions
	case "ready":
		if record.currentCheckedInAt() != nil {
			return "checked_in", actions
		}
		if record.HomePlayerID != nil && record.AwayPlayerID != nil && opensAt != nil && closesAt != nil &&
			!now.Before(*opensAt) && !now.After(*closesAt) && (record.ResultDueAt == nil || now.Before(*record.ResultDueAt)) {
			return "ready_for_check_in", append(actions, "check_in")
		}
		return "assigned", actions
	case "in_progress":
		if record.HomeCheckedInAt != nil && record.AwayCheckedInAt != nil &&
			(record.ResultDueAt == nil || now.Before(*record.ResultDueAt)) && record.SubmissionID == nil {
			actions = append(actions, "submit_result")
		}
		return "checked_in", actions
	case "awaiting_confirmation":
		if record.SubmissionState != nil && *record.SubmissionState == "pending_confirmation" &&
			record.SubmittedBy != nil && *record.SubmittedBy != userID {
			return "opponent_action_required", append(actions, "confirm_result", "dispute_result")
		}
		return "awaiting_opponent", actions
	case "disputed":
		return "under_review", actions
	case "forfeit":
		return "forfeited", actions
	case "completed", "cancelled":
		return "completed", actions
	default:
		return "assigned", actions
	}
}

func (record matchRecord) currentCheckedInAt() *time.Time {
	if record.CurrentSide == "home" {
		return record.HomeCheckedInAt
	}
	return record.AwayCheckedInAt
}

func (record matchRecord) opponentCheckedInAt() *time.Time {
	if record.CurrentSide == "home" {
		return record.AwayCheckedInAt
	}
	return record.HomeCheckedInAt
}

func (record matchRecord) checkInRejection(now time.Time) (string, string, time.Duration) {
	if record.State != "ready" {
		return "match_not_ready", "This match is not accepting check-ins.", 0
	}
	if record.HomePlayerID == nil || record.AwayPlayerID == nil {
		return "opponent_not_assigned", "Both sides must be assigned before check-in.", 0
	}
	opensAt, closesAt := record.checkInWindow()
	if opensAt == nil || closesAt == nil {
		return "check_in_not_scheduled", "The organizer has not scheduled match check-in.", 0
	}
	if now.Before(*opensAt) {
		return "check_in_not_open", "Match check-in is not open yet.", opensAt.Sub(now)
	}
	if now.After(*closesAt) || (record.ResultDueAt != nil && !now.Before(*record.ResultDueAt)) {
		return "check_in_closed", "The match check-in window has closed.", 0
	}
	return "", "", 0
}

type matchSettings struct {
	DrawAllowed  bool
	Instructions []friendMatchInstruction
}

type matchRuleOverrides struct {
	DrawAllowed             *bool                    `json:"drawAllowed"`
	FriendMatchInstructions []friendMatchInstruction `json:"friendMatchInstructions"`
}

func resolveMatchSettings(gameID, stageFormat string, competitionRules, stageConfig []byte) matchSettings {
	settings := matchSettings{DrawAllowed: stageFormat == "round_robin", Instructions: defaultFriendMatchInstructions(gameID)}
	apply := func(raw []byte) {
		if len(raw) == 0 {
			return
		}
		var override matchRuleOverrides
		if json.Unmarshal(raw, &override) != nil {
			return
		}
		if override.DrawAllowed != nil {
			settings.DrawAllowed = *override.DrawAllowed
		}
		if validFriendMatchInstructions(override.FriendMatchInstructions) {
			settings.Instructions = override.FriendMatchInstructions
		}
	}
	apply(competitionRules)
	apply(stageConfig)
	return settings
}

func validFriendMatchInstructions(instructions []friendMatchInstruction) bool {
	if len(instructions) == 0 || len(instructions) > 12 {
		return false
	}
	for _, instruction := range instructions {
		if strings.TrimSpace(instruction.Title) == "" || strings.TrimSpace(instruction.Detail) == "" ||
			len(instruction.Title) > 80 || len(instruction.Detail) > 300 {
			return false
		}
	}
	return true
}

func defaultFriendMatchInstructions(gameID string) []friendMatchInstruction {
	if gameID == "efootball-mobile" {
		return []friendMatchInstruction{
			{Title: "Wait for both players", Detail: "Start only after both sides show as checked in on Gamics."},
			{Title: "Create a Friend Match", Detail: "Use the tournament settings and confirm both in-game names before kickoff."},
			{Title: "Finish the match", Detail: "Play the complete match and do not leave the final result screen."},
			{Title: "Keep the result screen", Detail: "Keep both in-game names and the final score visible for result evidence."},
		}
	}
	return []friendMatchInstruction{{Title: "Follow the rules", Detail: "Use the competition rules shown by the organizer."}}
}

func (s *Server) encodeMatchCursor(state string, record matchRecord) string {
	payload, _ := json.Marshal(matchCursor{Version: matchCursorVersion, State: state, SortAt: record.SortAt.UnixNano(), ID: record.ID})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	signature := hmac.New(sha256.New, []byte(s.config.AccessTokenSecret))
	_, _ = signature.Write([]byte(encoded))
	return encoded + "." + base64.RawURLEncoding.EncodeToString(signature.Sum(nil))
}

func (s *Server) decodeMatchCursor(raw, state string) (matchCursor, error) {
	if len(raw) > 1024 {
		return matchCursor{}, errors.New("cursor is too long")
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return matchCursor{}, errors.New("malformed cursor")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return matchCursor{}, err
	}
	signature := hmac.New(sha256.New, []byte(s.config.AccessTokenSecret))
	_, _ = signature.Write([]byte(parts[0]))
	if !hmac.Equal(provided, signature.Sum(nil)) {
		return matchCursor{}, errors.New("invalid cursor signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return matchCursor{}, err
	}
	var cursor matchCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return matchCursor{}, err
	}
	if cursor.Version != matchCursorVersion || cursor.State != state || cursor.SortAt <= 0 || !canonicalUUIDPattern.MatchString(cursor.ID) {
		return matchCursor{}, errors.New("invalid cursor values")
	}
	return cursor, nil
}

func validIdempotencyKey(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}
	return true
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	result := value.UTC()
	return &result
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
