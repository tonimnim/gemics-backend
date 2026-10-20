package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// Match chat (docs/match-chat.md) is one thread per match between the players
// of its two entries, for agreeing a time and sharing the room code. Posting is
// open from when both sides are known until the match is final; after that the
// chat is read-only until the retention worker deletes it.

const (
	matchChatMaxMessages = 500
	matchChatMaxText     = 500
	matchChatPageLimit   = 100
	// matchChatSendLimit is messages per player per minute.
	matchChatSendLimit = 20
	// A player who polled the chat this recently is reading it, so gets no push.
	matchChatPresenceTTL = 15 * time.Second
	// matchChatPushSpacing is the least time between two pushes for one chat.
	matchChatPushSpacing = 2 * time.Minute
	// matchChatProposalReach bounds a proposed time for a match with no result
	// deadline yet.
	matchChatProposalReach = 14 * 24 * time.Hour
)

var (
	matchChatQuickReplies    = []string{"ready_now", "five_minutes", "ten_minutes", "friend_request_sent", "room_created", "good_game"}
	matchChatReportReasons   = []string{"abuse", "harassment", "cheating", "spam", "other"}
	matchChatClientIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	matchChatRoomCodePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9 -]{0,30}[A-Za-z0-9])?$`)
	// matchChatContactPatterns find emails, links and phone numbers, in that
	// order, so an email's domain is not also read as a link.
	matchChatContactPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9-]+(?:\.[a-z0-9-]+)*\.[a-z]{2,}`),
		regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`),
		regexp.MustCompile(`(?i)\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.(?:com|net|org|io|me|co|ke|app|link|ly|gg|tv|info|biz|xyz|site|online)\b(?:/\S*)?`),
		regexp.MustCompile(`\+?\d(?:[\s().-]*\d){8,}`),
	}
)

type matchChatQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type matchChatAccess struct {
	State       string
	ResultDueAt *time.Time
	HomeEntryID *string
	AwayEntryID *string
	Side        string
}

// open reports whether players can post: both sides are known and the match
// is not final.
func (access matchChatAccess) open() bool {
	return matchChatOpen(access.State, access.HomeEntryID, access.AwayEntryID)
}

func (access matchChatAccess) opponentEntryID() string {
	if access.Side == "home" {
		return valueOrEmpty(access.AwayEntryID)
	}
	return valueOrEmpty(access.HomeEntryID)
}

func matchChatOpen(state string, homeEntryID, awayEntryID *string) bool {
	return homeEntryID != nil && awayEntryID != nil && !slices.Contains([]string{"completed", "forfeit", "cancelled"}, state)
}

// loadMatchChatAccess answers pgx.ErrNoRows unless userID plays in the match,
// as the match room does.
func loadMatchChatAccess(ctx context.Context, queryer matchChatQueryer, userID, matchID string) (matchChatAccess, error) {
	var access matchChatAccess
	err := queryer.QueryRow(ctx, `SELECT m.state,m.result_due_at,m.home_entry_id::text,m.away_entry_id::text,
		CASE WHEN mine.entry_id=m.home_entry_id THEN 'home' ELSE 'away' END
		FROM matches m
		JOIN entry_members mine ON mine.user_id=$1 AND mine.roster_role IN ('starter','substitute')
			AND (mine.entry_id=m.home_entry_id OR mine.entry_id=m.away_entry_id)
		WHERE m.id=$2::uuid LIMIT 1`, userID, matchID).
		Scan(&access.State, &access.ResultDueAt, &access.HomeEntryID, &access.AwayEntryID, &access.Side)
	return access, err
}

type matchChatMessageView struct {
	Seq             int64      `json:"seq"`
	SenderID        string     `json:"senderId"`
	Sender          string     `json:"sender"`
	Mine            bool       `json:"mine"`
	Kind            string     `json:"kind"`
	Text            *string    `json:"text"`
	QuickReply      *string    `json:"quickReply"`
	RoomCode        *string    `json:"roomCode"`
	ProposedStartAt *time.Time `json:"proposedStartAt"`
	ReplyToSeq      *int64     `json:"replyToSeq"`
	Accepted        *bool      `json:"accepted"`
	ContactHidden   bool       `json:"contactHidden"`
	SentAt          time.Time  `json:"sentAt"`
}

type matchChatRoomCodeView struct {
	Code  string    `json:"code"`
	SetBy string    `json:"setBy"`
	SetAt time.Time `json:"setAt"`
}

type matchChatView struct {
	MatchID         string                 `json:"matchId"`
	Open            bool                   `json:"open"`
	RoomCode        *matchChatRoomCodeView `json:"roomCode"`
	AgreedStartAt   *time.Time             `json:"agreedStartAt"`
	PlayBy          *time.Time             `json:"playBy"`
	LastSeq         int64                  `json:"lastSeq"`
	ReadSeq         int64                  `json:"readSeq"`
	OpponentReadSeq int64                  `json:"opponentReadSeq"`
	Messages        []matchChatMessageView `json:"messages"`
	HasMore         bool                   `json:"hasMore"`
}

// matchChatMessageColumns selects a message with the side its sender plays
// for. Every query using it binds the match as $1.
const matchChatMessageColumns = `SELECT message.seq,message.sender_user_id::text,
		CASE WHEN EXISTS (SELECT 1 FROM matches side_match
			JOIN entry_members side_member ON side_member.entry_id=side_match.home_entry_id
			WHERE side_match.id=message.match_id AND side_member.user_id=message.sender_user_id)
		THEN 'home' ELSE 'away' END,
		message.kind,message.body,message.quick_reply,message.proposed_start_at,message.reply_to_seq,
		message.accepted,message.contact_hidden,message.created_at
	FROM match_chat_messages message`

func scanMatchChatMessages(rows pgx.Rows, viewerID string) ([]matchChatMessageView, error) {
	defer rows.Close()
	messages := []matchChatMessageView{}
	for rows.Next() {
		var message matchChatMessageView
		var body string
		if err := rows.Scan(&message.Seq, &message.SenderID, &message.Sender, &message.Kind, &body,
			&message.QuickReply, &message.ProposedStartAt, &message.ReplyToSeq, &message.Accepted,
			&message.ContactHidden, &message.SentAt); err != nil {
			return nil, err
		}
		message.Mine = message.SenderID == viewerID
		message.ProposedStartAt = utcTime(message.ProposedStartAt)
		message.SentAt = message.SentAt.UTC()
		switch message.Kind {
		case "text":
			message.Text = &body
		case "room_code":
			message.RoomCode = &body
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func loadMatchChatMessage(ctx context.Context, queryer matchChatQueryer, matchID string, seq int64, viewerID string) (matchChatMessageView, error) {
	rows, err := queryer.Query(ctx, matchChatMessageColumns+` WHERE message.match_id=$1 AND message.seq=$2`, matchID, seq)
	if err != nil {
		return matchChatMessageView{}, err
	}
	messages, err := scanMatchChatMessages(rows, viewerID)
	if err == nil && len(messages) == 0 {
		err = pgx.ErrNoRows
	}
	if err != nil {
		return matchChatMessageView{}, err
	}
	return messages[0], nil
}

func loadMatchChat(ctx context.Context, queryer matchChatQueryer, userID, matchID string, after int64, limit int) (matchChatView, error) {
	access, err := loadMatchChatAccess(ctx, queryer, userID, matchID)
	if err != nil {
		return matchChatView{}, err
	}
	view := matchChatView{MatchID: matchID, Open: access.open(), PlayBy: utcTime(access.ResultDueAt),
		Messages: []matchChatMessageView{}}
	var code, codeBy *string
	var codeAt *time.Time
	err = queryer.QueryRow(ctx, `SELECT chat.last_seq,chat.room_code,
		CASE WHEN chat.room_code_by IS NULL THEN NULL
			WHEN EXISTS (SELECT 1 FROM matches side_match
				JOIN entry_members side_member ON side_member.entry_id=side_match.home_entry_id
				WHERE side_match.id=chat.match_id AND side_member.user_id=chat.room_code_by) THEN 'home'
			ELSE 'away' END,
		chat.room_code_at,chat.agreed_start_at,
		COALESCE((SELECT viewer.read_seq FROM match_chat_reads viewer
			WHERE viewer.match_id=chat.match_id AND viewer.user_id=$2),0),
		COALESCE((SELECT max(other.read_seq) FROM match_chat_reads other
			WHERE other.match_id=chat.match_id AND other.user_id<>$2),0)
		FROM match_chats chat WHERE chat.match_id=$1`, matchID, userID).
		Scan(&view.LastSeq, &code, &codeBy, &codeAt, &view.AgreedStartAt, &view.ReadSeq, &view.OpponentReadSeq)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return matchChatView{}, err
	}
	view.AgreedStartAt = utcTime(view.AgreedStartAt)
	if code != nil && codeBy != nil && codeAt != nil {
		view.RoomCode = &matchChatRoomCodeView{Code: *code, SetBy: *codeBy, SetAt: codeAt.UTC()}
	}
	rows, err := queryer.Query(ctx, matchChatMessageColumns+` WHERE message.match_id=$1 AND message.seq>$2
		ORDER BY message.seq LIMIT $3`, matchID, after, limit+1)
	if err != nil {
		return matchChatView{}, err
	}
	if view.Messages, err = scanMatchChatMessages(rows, userID); err != nil {
		return matchChatView{}, err
	}
	if len(view.Messages) > limit {
		view.Messages, view.HasMore = view.Messages[:limit], true
	}
	return view, nil
}

func (s *Server) getMatchChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	query := r.URL.Query()
	after, limit := int64(0), 50
	if raw := query.Get("after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid_after", "after must be a message seq, 0 or more.")
			return
		}
		after = parsed
	}
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > matchChatPageLimit {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 100.")
			return
		}
		limit = parsed
	}
	userID := identityFromContext(r.Context()).UserID
	pool := s.selfReader(r.Context(), userID)
	view, err := loadMatchChat(r.Context(), pool, userID, matchID, after, limit)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) && pool != s.db.Writer {
		view, err = loadMatchChat(r.Context(), s.db.Writer, userID, matchID, after, limit)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the match chat.")
		return
	}
	s.markMatchChatSeen(r.Context(), matchID, userID)
	writeJSON(w, http.StatusOK, map[string]any{"data": view})
}

type matchChatMessageInput struct {
	ClientMessageID string     `json:"clientMessageId"`
	Kind            string     `json:"kind"`
	Text            string     `json:"text"`
	QuickReply      string     `json:"quickReply"`
	RoomCode        string     `json:"roomCode"`
	ProposedStartAt *time.Time `json:"proposedStartAt"`
	ReplyToSeq      int64      `json:"replyToSeq"`
	Accepted        *bool      `json:"accepted"`
}

// matchChatDraft is a validated message before it takes a seq.
type matchChatDraft struct {
	Kind            string
	Body            string
	QuickReply      *string
	ProposedStartAt *time.Time
	ReplyToSeq      *int64
	Accepted        *bool
	ContactHidden   bool
}

// draft checks the message has exactly the fields of its kind, cleans its text
// and hides contact details in it.
func (input matchChatMessageInput) draft() (matchChatDraft, string) {
	if !matchChatClientIDPattern.MatchString(input.ClientMessageID) {
		return matchChatDraft{}, "clientMessageId must be 1-64 letters, digits, dashes or underscores."
	}
	text, quick, code := input.Text != "", input.QuickReply != "", input.RoomCode != ""
	proposal, answer := input.ProposedStartAt != nil, input.ReplyToSeq != 0 || input.Accepted != nil
	only := func(field bool) bool {
		count := 0
		for _, set := range []bool{text, quick, code, proposal, answer} {
			if set {
				count++
			}
		}
		return field && count == 1
	}
	draft := matchChatDraft{Kind: input.Kind}
	switch input.Kind {
	case "text":
		cleaned, ok := cleanChatText(input.Text)
		if !only(text) || !ok {
			return matchChatDraft{}, "A text message needs text of 1-500 characters and nothing else."
		}
		draft.Body, draft.ContactHidden = hideContactDetails(cleaned)
		if runes := []rune(draft.Body); len(runes) > matchChatMaxText {
			draft.Body = string(runes[:matchChatMaxText])
		}
	case "quick_reply":
		if !only(quick) || !slices.Contains(matchChatQuickReplies, input.QuickReply) {
			return matchChatDraft{}, "Choose a quick reply: " + strings.Join(matchChatQuickReplies, ", ") + "."
		}
		draft.QuickReply = &input.QuickReply
	case "room_code":
		roomCode := strings.TrimSpace(input.RoomCode)
		if !only(code) || !matchChatRoomCodePattern.MatchString(roomCode) {
			return matchChatDraft{}, "A room code is 1-32 letters, digits, spaces or dashes."
		}
		draft.Body = roomCode
	case "time_proposal":
		if !only(proposal) {
			return matchChatDraft{}, "A time proposal needs proposedStartAt and nothing else."
		}
		proposed := input.ProposedStartAt.UTC().Truncate(time.Minute)
		draft.ProposedStartAt = &proposed
	case "time_answer":
		if !only(answer) || input.ReplyToSeq < 1 || input.Accepted == nil {
			return matchChatDraft{}, "A time answer needs replyToSeq and accepted, and nothing else."
		}
		draft.ReplyToSeq, draft.Accepted = &input.ReplyToSeq, input.Accepted
	default:
		return matchChatDraft{}, "kind must be text, quick_reply, room_code, time_proposal or time_answer."
	}
	return draft, ""
}

// cleanChatText trims the text, turns tabs into spaces, drops other control
// characters and keeps at most two line breaks in a row. It fails on empty or
// over-long text.
func cleanChatText(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	var builder strings.Builder
	breaks := 0
	for _, r := range strings.ReplaceAll(raw, "\r\n", "\n") {
		switch {
		case r == '\n':
			breaks++
			if breaks > 2 {
				continue
			}
		case r == '\t':
			r, breaks = ' ', 0
		case unicode.IsControl(r):
			continue
		default:
			breaks = 0
		}
		builder.WriteRune(r)
	}
	text := strings.TrimSpace(builder.String())
	return text, text != "" && utf8.RuneCountInString(text) <= matchChatMaxText
}

// hideContactDetails replaces emails, links and phone numbers with a marker,
// so players arrange the match here rather than moving off Ozone. Room codes
// have their own message kind, which this never touches.
func hideContactDetails(text string) (string, bool) {
	hidden := false
	for _, pattern := range matchChatContactPatterns {
		text = pattern.ReplaceAllStringFunc(text, func(string) string {
			hidden = true
			return "[hidden]"
		})
	}
	return text, hidden
}

func (s *Server) postMatchChatMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input matchChatMessageInput
	if !decodeJSON(w, r, &input) {
		return
	}
	draft, problem := input.draft()
	if problem != "" {
		writeError(w, http.StatusBadRequest, "invalid_chat_message", problem)
		return
	}
	userID := identityFromContext(r.Context()).UserID
	if !s.allowMatchChatSend(r.Context(), userID) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "chat_rate_limited", "Too many messages. Wait a minute and try again.")
		return
	}
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to send the message.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	unavailable := func() {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to send the message.")
	}
	access, err := loadMatchChatAccess(r.Context(), tx, userID, matchID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	if err != nil {
		unavailable()
		return
	}
	// The chat row's lock orders every write to the chat, which keeps seq
	// gapless and in commit order.
	var lastSeq int64
	if _, err = tx.Exec(r.Context(), `INSERT INTO match_chats(match_id) VALUES ($1) ON CONFLICT DO NOTHING`, matchID); err == nil {
		err = tx.QueryRow(r.Context(), `SELECT last_seq FROM match_chats WHERE match_id=$1 FOR UPDATE`, matchID).Scan(&lastSeq)
	}
	if err != nil {
		unavailable()
		return
	}
	var replayed int64
	err = tx.QueryRow(r.Context(), `SELECT seq FROM match_chat_messages
		WHERE match_id=$1 AND sender_user_id=$2 AND client_message_id=$3`, matchID, userID, input.ClientMessageID).Scan(&replayed)
	if err == nil {
		message, loadErr := loadMatchChatMessage(r.Context(), tx, matchID, replayed, userID)
		if loadErr != nil {
			unavailable()
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": message})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		unavailable()
		return
	}
	if !access.open() {
		writeError(w, http.StatusConflict, "chat_closed", "The match is over, so its chat is read-only.")
		return
	}
	if lastSeq >= matchChatMaxMessages {
		writeError(w, http.StatusConflict, "chat_full", "This chat has reached its 500 message limit.")
		return
	}
	now := time.Now().UTC()
	var agreedStartAt *time.Time
	switch draft.Kind {
	case "time_proposal":
		latest := now.Add(matchChatProposalReach)
		if access.ResultDueAt != nil {
			latest = *access.ResultDueAt
		}
		if !draft.ProposedStartAt.After(now) || draft.ProposedStartAt.After(latest) {
			writeError(w, http.StatusBadRequest, "invalid_proposed_time",
				"Propose a time in the future and before the match must be played (playBy).")
			return
		}
	case "time_answer":
		code, message, start := answerableMatchChatProposal(r.Context(), tx, matchID, userID, *draft.ReplyToSeq, now)
		if code == "database_unavailable" {
			unavailable()
			return
		}
		if code != "" {
			writeError(w, http.StatusConflict, code, message)
			return
		}
		if *draft.Accepted {
			agreedStartAt = start
		}
	}
	seq := lastSeq + 1
	if _, err = tx.Exec(r.Context(), `INSERT INTO match_chat_messages
		(match_id,seq,sender_user_id,client_message_id,kind,body,quick_reply,proposed_start_at,reply_to_seq,accepted,contact_hidden)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, matchID, seq, userID, input.ClientMessageID, draft.Kind, draft.Body,
		draft.QuickReply, draft.ProposedStartAt, draft.ReplyToSeq, draft.Accepted, draft.ContactHidden); err != nil {
		unavailable()
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE match_chats SET last_seq=$2,
		room_code=CASE WHEN $3 THEN $4 ELSE room_code END,
		room_code_by=CASE WHEN $3 THEN $5::uuid ELSE room_code_by END,
		room_code_at=CASE WHEN $3 THEN clock_timestamp() ELSE room_code_at END,
		agreed_start_at=COALESCE($6,agreed_start_at)
		WHERE match_id=$1`, matchID, seq, draft.Kind == "room_code", draft.Body, userID, agreedStartAt); err != nil {
		unavailable()
		return
	}
	// Sending a message reads everything before it.
	if _, err = tx.Exec(r.Context(), `INSERT INTO match_chat_reads(match_id,user_id,read_seq) VALUES ($1,$2,$3)
		ON CONFLICT (match_id,user_id) DO UPDATE SET read_seq=GREATEST(match_chat_reads.read_seq,EXCLUDED.read_seq),
		updated_at=now()`, matchID, userID, seq); err != nil {
		unavailable()
		return
	}
	if err = s.notifyMatchChatOpponents(r.Context(), tx, matchID, userID, access.opponentEntryID()); err != nil {
		unavailable()
		return
	}
	message, err := loadMatchChatMessage(r.Context(), tx, matchID, seq, userID)
	if err != nil || tx.Commit(r.Context()) != nil {
		unavailable()
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": message})
}

// answerableMatchChatProposal checks that replyTo is the chat's latest time
// proposal, made by someone else and not answered yet, and returns its time.
// A refusal comes back as an error code and message.
func answerableMatchChatProposal(ctx context.Context, tx pgx.Tx, matchID, userID string, replyTo int64,
	now time.Time) (string, string, *time.Time) {
	var seq int64
	var proposer string
	var start time.Time
	var answered bool
	err := tx.QueryRow(ctx, `SELECT proposal.seq,proposal.sender_user_id::text,proposal.proposed_start_at,
		EXISTS (SELECT 1 FROM match_chat_messages answer
			WHERE answer.match_id=proposal.match_id AND answer.reply_to_seq=proposal.seq)
		FROM match_chat_messages proposal WHERE proposal.match_id=$1 AND proposal.kind='time_proposal'
		ORDER BY proposal.seq DESC LIMIT 1`, matchID).Scan(&seq, &proposer, &start, &answered)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || (err == nil && seq != replyTo):
		return "proposal_not_current", "Answer the latest time proposal.", nil
	case err != nil:
		return "database_unavailable", "", nil
	case proposer == userID:
		return "own_proposal", "Your opponent answers your proposal.", nil
	case answered:
		return "proposal_answered", "This proposal has already been answered.", nil
	case !start.After(now):
		return "proposal_expired", "That time has passed. Propose a new one.", nil
	}
	start = start.UTC()
	return "", "", &start
}

func (s *Server) allowMatchChatSend(ctx context.Context, userID string) bool {
	limitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	allowed, err := redisFixedWindow(limitCtx, s.redis, "chat:send:{"+userID+"}", matchChatSendLimit, time.Minute)
	// Chat stays usable when Redis is down; the 500 message cap still bounds a chat.
	return err != nil || allowed
}

func matchChatPresenceKey(matchID, userID string) string {
	return "chat:seen:" + matchID + ":" + userID
}

// markMatchChatSeen records that the player is reading the chat, which holds
// back pushes for it while they are.
func (s *Server) markMatchChatSeen(ctx context.Context, matchID, userID string) {
	if s.redis == nil {
		return
	}
	markCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	_ = s.redis.Set(markCtx, matchChatPresenceKey(matchID, userID), "1", matchChatPresenceTTL).Err()
}

func (s *Server) matchChatSeen(ctx context.Context, matchID, userID string) bool {
	if s.redis == nil {
		return false
	}
	seenCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	count, err := s.redis.Exists(seenCtx, matchChatPresenceKey(matchID, userID)).Result()
	return err == nil && count > 0
}

// notifyMatchChatOpponents queues a push for each opponent who is not reading
// the chat and has not had one for it in the last two minutes.
func (s *Server) notifyMatchChatOpponents(ctx context.Context, tx pgx.Tx, matchID, senderID, opponentEntryID string) error {
	if opponentEntryID == "" {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT member.user_id::text FROM entry_members member
		JOIN users player ON player.id=member.user_id AND player.status='active'
		WHERE member.entry_id=$1 AND member.roster_role IN ('starter','substitute') AND member.user_id<>$2`,
		opponentEntryID, senderID)
	if err != nil {
		return err
	}
	opponents, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	var handle string
	if len(opponents) > 0 {
		if err = tx.QueryRow(ctx, `SELECT COALESCE(profile.handle,player.display_name) FROM users player
			LEFT JOIN player_profiles profile ON profile.user_id=player.id WHERE player.id=$1`, senderID).Scan(&handle); err != nil {
			return err
		}
	}
	for _, opponentID := range opponents {
		if s.matchChatSeen(ctx, matchID, opponentID) {
			continue
		}
		var due bool
		err = tx.QueryRow(ctx, `INSERT INTO match_chat_reads(match_id,user_id,notified_at) VALUES ($1,$2,now())
			ON CONFLICT (match_id,user_id) DO UPDATE SET notified_at=now(),updated_at=now()
			WHERE match_chat_reads.notified_at IS NULL OR match_chat_reads.notified_at<now()-$3::interval
			RETURNING true`, matchID, opponentID, matchChatPushSpacing.String()).Scan(&due)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO outbox_events(aggregate_type,aggregate_id,event_type,payload)
			VALUES ('match',$1::text,'match.chat_message',
			jsonb_build_object('userId',$2::text,'senderUserId',$3::text,'handle',$4::text))`,
			matchID, opponentID, senderID, handle); err != nil {
			return err
		}
	}
	return nil
}

type matchChatReadInput struct {
	Seq int64 `json:"seq"`
}

func (s *Server) readMatchChat(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input matchChatReadInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Seq < 0 {
		writeError(w, http.StatusBadRequest, "invalid_seq", "seq must be a message seq, 0 or more.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	_, err := loadMatchChatAccess(r.Context(), s.db.Writer, userID, matchID)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	// Reading clears the push spacing, so the next message notifies at once.
	if err == nil {
		_, err = s.db.Writer.Exec(r.Context(), `INSERT INTO match_chat_reads(match_id,user_id,read_seq)
			SELECT chat.match_id,$2::uuid,LEAST($3::bigint,chat.last_seq) FROM match_chats chat WHERE chat.match_id=$1
			ON CONFLICT (match_id,user_id) DO UPDATE SET read_seq=GREATEST(match_chat_reads.read_seq,EXCLUDED.read_seq),
			notified_at=NULL,updated_at=now()`, matchID, userID, input.Seq)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to mark the chat read.")
		return
	}
	s.markMatchChatSeen(r.Context(), matchID, userID)
	w.WriteHeader(http.StatusNoContent)
}

type matchChatReportInput struct {
	Seq    int64  `json:"seq"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

func (s *Server) reportMatchChatMessage(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	matchID := r.PathValue("matchId")
	if !canonicalUUIDPattern.MatchString(matchID) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var input matchChatReportInput
	if !decodeJSON(w, r, &input) {
		return
	}
	note, _ := cleanChatText(input.Note)
	if input.Seq < 1 || !slices.Contains(matchChatReportReasons, input.Reason) || utf8.RuneCountInString(note) > 500 {
		writeError(w, http.StatusBadRequest, "invalid_chat_report",
			"Report a message by its seq, with a reason ("+strings.Join(matchChatReportReasons, ", ")+") and an optional note of up to 500 characters.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to report the message.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck
	if _, err = loadMatchChatAccess(r.Context(), tx, userID, matchID); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "match_not_found", "Match not found.")
		return
	}
	var senderID string
	if err == nil {
		err = tx.QueryRow(r.Context(), `SELECT sender_user_id::text FROM match_chat_messages WHERE match_id=$1 AND seq=$2`,
			matchID, input.Seq).Scan(&senderID)
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "chat_message_not_found", "Message not found.")
			return
		}
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to report the message.")
		return
	}
	if senderID == userID {
		writeError(w, http.StatusConflict, "own_chat_message", "You can only report your opponent's messages.")
		return
	}
	var report struct {
		ID        string    `json:"id"`
		Seq       int64     `json:"seq"`
		Reason    string    `json:"reason"`
		Status    string    `json:"status"`
		CreatedAt time.Time `json:"createdAt"`
	}
	err = tx.QueryRow(r.Context(), `INSERT INTO match_chat_reports(match_id,message_seq,reporter_user_id,reported_user_id,reason,note)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (match_id,message_seq,reporter_user_id) DO NOTHING
		RETURNING id::text,message_seq,reason,status,created_at`, matchID, input.Seq, userID, senderID, input.Reason, note).
		Scan(&report.ID, &report.Seq, &report.Reason, &report.Status, &report.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "chat_message_already_reported", "You have already reported this message.")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(actor_user_id,action,subject_type,subject_id,request_id,after_state)
			VALUES ($1::uuid,'match_chat.reported','match_chat_report',$2::text,$3,
			jsonb_build_object('matchId',$4::text,'seq',$5::bigint,'reason',$6::text))`,
			userID, report.ID, r.Header.Get("X-Request-ID"), matchID, input.Seq, input.Reason)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to report the message.")
		return
	}
	report.CreatedAt = report.CreatedAt.UTC()
	writeJSON(w, http.StatusCreated, map[string]any{"data": report})
}
