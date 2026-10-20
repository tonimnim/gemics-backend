BEGIN;

-- Match chat: one thread per match between its two players, so they can agree
-- a time and share a room code. A chat is deleted 7 days after its match is
-- final, or 30 days after a result review or chat report of it is settled
-- (docs/match-chat.md).

-- match_chats

CREATE TABLE match_chats (
    match_id uuid NOT NULL,
    last_seq bigint DEFAULT 0 NOT NULL,
    room_code text,
    room_code_by uuid,
    room_code_at timestamp with time zone,
    agreed_start_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_chats_last_seq_check CHECK (((last_seq >= 0) AND (last_seq <= 500))),
    CONSTRAINT match_chats_room_code_check CHECK (((room_code IS NULL) OR ((length(room_code) >= 1) AND (length(room_code) <= 32)))),
    CONSTRAINT match_chats_room_code_pair_chk CHECK ((((room_code IS NULL) = (room_code_by IS NULL)) AND ((room_code IS NULL) = (room_code_at IS NULL))))
);
ALTER TABLE ONLY match_chats
    ADD CONSTRAINT match_chats_pkey PRIMARY KEY (match_id);
ALTER TABLE ONLY match_chats
    ADD CONSTRAINT match_chats_match_id_fkey FOREIGN KEY (match_id) REFERENCES matches(id) ON DELETE CASCADE;
ALTER TABLE ONLY match_chats
    ADD CONSTRAINT match_chats_room_code_by_fkey FOREIGN KEY (room_code_by) REFERENCES users(id);

-- match_chat_messages: seq is gapless per chat, taken under the chat row's
-- lock, so a reader polling after its last seq never skips a message.

CREATE TABLE match_chat_messages (
    match_id uuid NOT NULL,
    seq bigint NOT NULL,
    sender_user_id uuid NOT NULL,
    client_message_id text NOT NULL,
    kind text NOT NULL,
    body text DEFAULT ''::text NOT NULL,
    quick_reply text,
    proposed_start_at timestamp with time zone,
    reply_to_seq bigint,
    accepted boolean,
    contact_hidden boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT clock_timestamp() NOT NULL,
    CONSTRAINT match_chat_messages_body_check CHECK ((length(body) <= 500)),
    CONSTRAINT match_chat_messages_client_message_id_check CHECK (((length(client_message_id) >= 1) AND (length(client_message_id) <= 64))),
    CONSTRAINT match_chat_messages_kind_check CHECK ((kind = ANY (ARRAY['text'::text, 'quick_reply'::text, 'room_code'::text, 'time_proposal'::text, 'time_answer'::text]))),
    CONSTRAINT match_chat_messages_quick_reply_check CHECK (((quick_reply IS NULL) OR (quick_reply = ANY (ARRAY['ready_now'::text, 'five_minutes'::text, 'ten_minutes'::text, 'friend_request_sent'::text, 'room_created'::text, 'good_game'::text])))),
    CONSTRAINT match_chat_messages_seq_check CHECK ((seq > 0)),
    CONSTRAINT match_chat_messages_shape_chk CHECK ((((kind = 'text'::text) AND (length(body) >= 1) AND (quick_reply IS NULL) AND (proposed_start_at IS NULL) AND (reply_to_seq IS NULL) AND (accepted IS NULL)) OR ((kind = 'quick_reply'::text) AND (body = ''::text) AND (quick_reply IS NOT NULL) AND (proposed_start_at IS NULL) AND (reply_to_seq IS NULL) AND (accepted IS NULL)) OR ((kind = 'room_code'::text) AND (length(body) >= 1) AND (length(body) <= 32) AND (quick_reply IS NULL) AND (proposed_start_at IS NULL) AND (reply_to_seq IS NULL) AND (accepted IS NULL)) OR ((kind = 'time_proposal'::text) AND (body = ''::text) AND (quick_reply IS NULL) AND (proposed_start_at IS NOT NULL) AND (reply_to_seq IS NULL) AND (accepted IS NULL)) OR ((kind = 'time_answer'::text) AND (body = ''::text) AND (quick_reply IS NULL) AND (proposed_start_at IS NULL) AND (reply_to_seq IS NOT NULL) AND (accepted IS NOT NULL))))
);
ALTER TABLE ONLY match_chat_messages
    ADD CONSTRAINT match_chat_messages_pkey PRIMARY KEY (match_id, seq);
ALTER TABLE ONLY match_chat_messages
    ADD CONSTRAINT match_chat_messages_client_uidx UNIQUE (match_id, sender_user_id, client_message_id);
ALTER TABLE ONLY match_chat_messages
    ADD CONSTRAINT match_chat_messages_match_id_fkey FOREIGN KEY (match_id) REFERENCES match_chats(match_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_chat_messages
    ADD CONSTRAINT match_chat_messages_reply_fkey FOREIGN KEY (match_id, reply_to_seq) REFERENCES match_chat_messages(match_id, seq) ON DELETE CASCADE;
ALTER TABLE ONLY match_chat_messages
    ADD CONSTRAINT match_chat_messages_sender_user_id_fkey FOREIGN KEY (sender_user_id) REFERENCES users(id);

-- match_chat_reads: each player's read position, and when they were last sent
-- a push for the chat, which spaces the pushes out.

CREATE TABLE match_chat_reads (
    match_id uuid NOT NULL,
    user_id uuid NOT NULL,
    read_seq bigint DEFAULT 0 NOT NULL,
    notified_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_chat_reads_read_seq_check CHECK ((read_seq >= 0))
);
ALTER TABLE ONLY match_chat_reads
    ADD CONSTRAINT match_chat_reads_pkey PRIMARY KEY (match_id, user_id);
ALTER TABLE ONLY match_chat_reads
    ADD CONSTRAINT match_chat_reads_match_id_fkey FOREIGN KEY (match_id) REFERENCES match_chats(match_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_chat_reads
    ADD CONSTRAINT match_chat_reads_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);

-- match_chat_reports: a player reports one of their opponent's messages; staff
-- dismiss the report or act on it.

CREATE TABLE match_chat_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    match_id uuid NOT NULL,
    message_seq bigint NOT NULL,
    reporter_user_id uuid NOT NULL,
    reported_user_id uuid NOT NULL,
    reason text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'open'::text NOT NULL,
    resolved_by uuid,
    resolution_note text DEFAULT ''::text NOT NULL,
    resolved_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_chat_reports_note_check CHECK ((length(note) <= 500)),
    CONSTRAINT match_chat_reports_reason_check CHECK ((reason = ANY (ARRAY['abuse'::text, 'harassment'::text, 'cheating'::text, 'spam'::text, 'other'::text]))),
    CONSTRAINT match_chat_reports_resolution_chk CHECK ((((status = 'open'::text) AND (resolved_by IS NULL) AND (resolved_at IS NULL)) OR ((status <> 'open'::text) AND (resolved_by IS NOT NULL) AND (resolved_at IS NOT NULL)))),
    CONSTRAINT match_chat_reports_resolution_note_check CHECK ((length(resolution_note) <= 500)),
    CONSTRAINT match_chat_reports_status_check CHECK ((status = ANY (ARRAY['open'::text, 'dismissed'::text, 'actioned'::text]))),
    CONSTRAINT match_chat_reports_users_chk CHECK ((reporter_user_id <> reported_user_id))
);
ALTER TABLE ONLY match_chat_reports
    ADD CONSTRAINT match_chat_reports_pkey PRIMARY KEY (id);
ALTER TABLE ONLY match_chat_reports
    ADD CONSTRAINT match_chat_reports_reporter_uidx UNIQUE (match_id, message_seq, reporter_user_id);
ALTER TABLE ONLY match_chat_reports
    ADD CONSTRAINT match_chat_reports_message_fkey FOREIGN KEY (match_id, message_seq) REFERENCES match_chat_messages(match_id, seq) ON DELETE CASCADE;
ALTER TABLE ONLY match_chat_reports
    ADD CONSTRAINT match_chat_reports_reporter_user_id_fkey FOREIGN KEY (reporter_user_id) REFERENCES users(id);
ALTER TABLE ONLY match_chat_reports
    ADD CONSTRAINT match_chat_reports_reported_user_id_fkey FOREIGN KEY (reported_user_id) REFERENCES users(id);
ALTER TABLE ONLY match_chat_reports
    ADD CONSTRAINT match_chat_reports_resolved_by_fkey FOREIGN KEY (resolved_by) REFERENCES users(id);
CREATE INDEX match_chat_reports_open_idx ON match_chat_reports USING btree (created_at, id) WHERE (status = 'open'::text);
CREATE INDEX match_chat_reports_resolved_idx ON match_chat_reports USING btree (resolved_at DESC, id DESC) WHERE (status <> 'open'::text);
CREATE INDEX match_chat_reports_match_idx ON match_chat_reports USING btree (match_id);

COMMIT;
