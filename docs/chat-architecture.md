# Real-time chat architecture: the decision

Produced by a 13-agent design pass: four architectures, two adversarial lenses each,
then a synthesis. Its load-bearing claims were independently verified against the source
and the live database:

- maxPositiveSessionCacheTTL = 5s (request_security.go:23) - confirmed
- DATABASE_WRITE_MAX_CONNS defaults to 8 (config.go:192) - confirmed
- matches_id_competition_uidx exists, competitions_id_org_uidx does not - confirmed
- architecture.md excludes raw chat from analytics products, and holds Redis
  non-authoritative - confirmed
- now() returns TRANSACTION START time, proven live: after a 2s sleep inside a
  transaction now() was unchanged while clock_timestamp() had advanced. This is the
  bug that makes a timestamp-based resume cursor silently skip threads.

Status: **decided, not implemented.** No chat code or migration exists yet.

> **Staff access retargeted (2026-09-28).** Result verification v2 retired the
> organizer-run adjudication this design assumed: organizers never decide results,
> and contested matches go to the Gamics result review queue, decided by platform
> reviewers and admins (`docs/result-verification.md`). The organizer-gated
> transcript route and the organization `chat.read` permission this design first
> proposed are obsolete. Sections 3, 4, 8, 9, 10 and 12 below describe the platform
> reviewer version: match chat is readable only by a Gamics reviewer or admin
> working a queued review of that match, never by organizers.

---
# Gamics Real-Time Chat — Architecture Decision

**Decision: WebSocket + Redis Pub/Sub fan-out, PostgreSQL-durable, pair-keyed conversations.**

Everything below was checked against `C:/Users/BEST/gamics.io/services/api`. Line-level claims are verified, not quoted from the proposals.

---

## 1. Why this one won

All four candidates converged on the same PostgreSQL core — pair-keyed conversations, a gapless per-conversation `seq`, unread as counter arithmetic. That part was never contested. The decision is the transport, and three of the four transports have a disqualifying property that the judges found and I re-verified.

**LISTEN/NOTIFY is out — it couples chat load to platform-wide write availability.** The judge's finding is correct and it is the worst failure mode in the set: NOTIFY is delivered at COMMIT, the queue is bounded, and a wedged listener backend stalls the tail. When the queue fills, `NOTIFY` starts *failing* — and every transaction that emits a notification fails with it. That is not "chat is down", that is M-Pesa callbacks and score reports failing because a container paused. It also forecloses PgBouncer transaction pooling, which `docs/production-architecture.md` explicitly holds open, and it puts the fan-out serialization point on the single primary at roughly 5k msg/s. We would be trading a $0 line item for a correlated outage across the whole platform. No.

**The managed vendor is out — it bills for idle users against a product with no ARPU, and the doorbell makes it *less* resilient, not more.** Its own defence is "vendor loss degrades to polling our own API". I read the auth path: `requireAuth` (auth_handlers.go:280) → `sessionActive` (request_security.go:33), and `maxPositiveSessionCacheTTL = 5 * time.Second` (request_security.go:23) hard-caps the positive Redis cache at five seconds regardless of `AUTH_SESSION_CACHE_TTL`. Any authenticated request more than 5s after the last one for that session falls through to `s.db.Writer` with an `EXISTS` over `refresh_sessions JOIN users`, through `DATABASE_WRITE_MAX_CONNS` = 8 per pod (config.go:192). So the doorbell's degraded mode converts N foregrounded users into pollers, and each poll is a writer query. Vendor blip → database saturation → sends fail too. Full-payload-over-vendor loses delivery during a vendor outage; doorbell loses the database. Add $2,500–4,000/month at 20k concurrent against a schema that caps fees at `fee_purpose='administration'`, plus routing Kenyan users' private-message metadata through a foreign processor under the DPA 2019. Rejected on three independent grounds.

**SSE is out — it is the same design with a worse write path and a broken storm control.** Its own document concedes it cannot recover author-intent ordering: two back-to-back POSTs can commit inverted, and the fix (one in-flight POST per conversation) blocks the next send under packet loss, which is the normal condition in this market. It pays 400–500 bytes of headers per send against ~20 bytes of WebSocket frame. And its headline storm control — a server-randomised `retry:` directive — is an `EventSource` protocol field that the same document says the primary Flutter client does not use. Its most-cited advantage (no new dependency) is real but small; its resume cursor does not even fit `publicCursor`'s 2048-byte cap.

**WebSocket wins because it is the only transport whose failure mode is confined to chat.** Redis pub/sub loss costs latency and is repaired by the seq watermark. Redis being unreachable entirely degrades chat to REST polling — the same fallback SSE offers — without ever having made Redis authoritative, which is exactly the boundary `docs/architecture.md:123-128` draws. It gives author-order for free, one connection, the smallest frames, and no per-connection bill. It scored highest on the mobile lens (6.5) and tied on correctness (7).

It is **not** adopted as written. The judges found real defects and I am taking their fixes as part of the decision, not as follow-ups.

---

## 2. What I grafted in, and from where

| Taken | From | Why |
|---|---|---|
| **Per-user monotonic `inbox_seq`** | Judge fix on both SSE and NOTIFY | The single most important change. All four candidates keyed cross-conversation resume on a timestamp. `now()` is transaction-*start* time, so a send that begins at T1 and commits at T3 writes `last_activity_at = T1`; a client whose cursor already passed T2 never sees that thread again. Replacing it with a dense per-user counter makes "provably repair every gap" true across conversations, not just within one. |
| **`GET /v1/chat/sync` conversation-level delta** | SSE proposal | One bounded request returns `{conversationId, lastSeq, unread, preview}` for changed threads. A player with 300 conversations and 3 active resumes on ~300 bytes, not 300 messages. Cold start, gap fill and a six-hour suspension take the same code path. |
| **`redacted_at` / `redacted_by` tombstoning** | LISTEN/NOTIFY proposal | Reconciles "never deleted" with an erasure obligation under Kenya's DPA 2019. NULLs the body, deletes the S3 object, keeps the row and the `seq`, so gaplessness and the dispute transcript survive. |
| **Permanent `client_message_id` instead of `idempotency_keys`** | LISTEN/NOTIFY proposal | Correct reasoning: `idempotency_keys` stores whole response bodies and expires at 24h; a handset suspended in a pocket retries days later. A unique index is smaller, permanent, and returns the original `seq`. |
| **Frozen report window (`window_from_seq` / `window_to_seq`)** | LISTEN/NOTIFY proposal | Staff read the range a player named at report time. Complaint-driven, never browsable. |
| **Withhold `chat.read` from `RoleAnalyst`, citing the architecture doc** | SSE proposal | `docs/architecture.md:99-100` lists raw chat among things that are not analytics products. Granting the analytics role message access would contradict the architecture in one line of Go. Sharpest single line in any of the four. |
| **`competitions_id_org_uidx` + composite FK on the match link** | Vendor proposal | The denormalised `organization_id` on the link is the staff authorization scope. Without the composite FK it is an unverified claim about tenancy. `matches_id_competition_uidx` already exists (000009:47); the competitions half does not. |
| **`expect_seq` on the gap-fill fetch** | Vendor proposal | Lets the server detect a short read against a lagging replica instead of the client silently concluding it is caught up. |
| **`blocked_at` on the participant row** | Vendor proposal | 1:1 DMs across 1M accounts without a block list is a harassment and mobile-money-scam vector. |

---

## 3. Match vs friendly — resolved

**A conversation is an unordered pair of users. A match is a link, not the key.**

```
chat_conversations (user_low_id, user_high_id)  UNIQUE, CHECK (low < high)
chat_match_links   (match_id PK) → conversation_id, from_seq, to_seq, organization_id
```

This is definitive, and here is what it buys:

- **A friendly is the default case, not a special case.** Zero match links. No nullable `match_id` on the conversation, no second code path, no "friendly conversations" table.
- **Two players who meet in three tournaments have one thread and three links.** That is what the founder described when he said conversations outlive the match.
- **Group chat becomes structurally impossible.** There are exactly two uuid columns and they must differ. Adding a third participant requires a migration, not a code review. The founder said never; this enforces never.
- **`from_seq` is the staff scope.** Captured as `conversations.last_seq` at link time. A Gamics reviewer reads the window that match opened — not two years of a friendship, not another tournament.

**Consequences I am accepting, stated up front:**

1. **A reviewer cannot see a message sent before the link.** "Let's play our round-2 game tomorrow at 8", agreed a day early, is invisible to the Gamics reviewer deciding that match's review. Widening the window re-exposes private history. There is no setting of this dial that is right in both cases; I chose privacy.
2. **The gapless counter forecloses group chat permanently.** Not a column addition — a rewrite of identity, sequencing and unread. This is the assumption most worth challenging *before* the migration runs.
3. **Team games degrade to captain-to-captain.** `competition_entries.captain_user_id` is NOT NULL and always present; `entry_members` can hold several people and a 1:1 chat between two squads is undefined. For eFootball 1v1 this is exact. For a future team title the squad has no chat. That is my call, not the founder's.

Nothing in the schema names eFootball. `chat_conversations` carries no `game_id` — the thread belongs to two people, not a title. Game context lives on the match link, reachable through `competitions`.

---

## 4. Migration 000011 — expand-only DDL

> Numbering assumes `000010` stays unclaimed (the working tree has uncommitted organizer work but no new migration). Renumber if it lands first.

Expand-only rules honoured: every object is new; the one existing-table change (widening a CHECK) adds the looser constraint `NOT VALID`, validates it, and only then drops the narrower one — dropping a *narrower* constraint never breaks a reader.

```sql
-- migrations/000011_chat.up.sql
BEGIN;

-- ============================================================================
-- 0. Prerequisite for the composite FK in section 5. 000004 gave
--    competition_stages and competition_entries composite targets; competitions
--    never got one, and the staff-authorization FK below depends on it.
-- ============================================================================
CREATE UNIQUE INDEX competitions_id_org_uidx ON competitions (id, organization_id);

-- ============================================================================
-- 1. Per-user monotonic cursor.
--    This is what makes cross-conversation gap repair PROVABLE. Every send
--    bumps both participants' cursors inside the send transaction, so
--    "everything that changed for me since inbox_seq N" is a dense range, not
--    a timestamp comparison. now() is transaction-START time in PostgreSQL, so
--    any timestamp cursor can permanently skip a late-committing row.
-- ============================================================================
CREATE TABLE chat_user_cursors (
    user_id   uuid PRIMARY KEY REFERENCES users(id),
    inbox_seq bigint NOT NULL DEFAULT 0 CHECK (inbox_seq >= 0)
) WITH (fillfactor = 70);
-- fillfactor is CORRECT here and only here on the hot path: inbox_seq is the
-- only column that changes and it is not indexed (the PK is user_id alone), so
-- these updates are genuinely HOT. Mirrors the matches fillfactor 85 precedent
-- in 000009_bracket_graph.up.sql:42.

-- ============================================================================
-- 2. The conversation. The PAIR is the identity.
-- ============================================================================
CREATE TABLE chat_conversations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_low_id  uuid NOT NULL REFERENCES users(id),
    user_high_id uuid NOT NULL REFERENCES users(id),
    origin text NOT NULL CHECK (origin IN ('match','friendly')),
    origin_match_id uuid REFERENCES matches(id),
    last_seq bigint NOT NULL DEFAULT 0 CHECK (last_seq >= 0),
    last_message_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chat_conversations_pair_order_chk
        CHECK (user_low_id < user_high_id),
    CONSTRAINT chat_conversations_origin_chk
        CHECK ((origin = 'match') = (origin_match_id IS NOT NULL))
) WITH (fillfactor = 70);
-- fillfactor CORRECT: last_seq and last_message_at change on every message and
-- neither is indexed, so these updates stay HOT.

-- SERVES: the only way a conversation is ever opened.
--   SELECT id FROM chat_conversations
--    WHERE user_low_id=$1 AND user_high_id=$2;   -- caller sorts the two uuids
-- Also ENFORCES one thread per pair and, with the CHECK above, makes a third
-- participant unrepresentable.
CREATE UNIQUE INDEX chat_conversations_pair_uidx
    ON chat_conversations (user_low_id, user_high_id);

-- ============================================================================
-- 3. Participants. Unread lives here as arithmetic on ONE row.
-- ============================================================================
CREATE TABLE chat_participants (
    conversation_id uuid NOT NULL REFERENCES chat_conversations(id),
    user_id         uuid NOT NULL REFERENCES users(id),
    peer_user_id    uuid NOT NULL REFERENCES users(id),
    inbox_seq         bigint NOT NULL DEFAULT 0 CHECK (inbox_seq >= 0),
    sent_ordinal      bigint NOT NULL DEFAULT 0 CHECK (sent_ordinal >= 0),
    peer_sent_ordinal bigint NOT NULL DEFAULT 0 CHECK (peer_sent_ordinal >= 0),
    peer_ordinal_read bigint NOT NULL DEFAULT 0 CHECK (peer_ordinal_read >= 0),
    last_read_seq     bigint NOT NULL DEFAULT 0 CHECK (last_read_seq >= 0),
    last_activity_at  timestamptz NOT NULL DEFAULT now(),  -- DISPLAY ONLY
    muted_until timestamptz,
    archived_at timestamptz,
    blocked_at  timestamptz,
    disclosure_shown_at timestamptz,
    PRIMARY KEY (conversation_id, user_id),
    CONSTRAINT chat_participants_read_le_sent_chk
        CHECK (peer_ordinal_read <= peer_sent_ordinal),
    CONSTRAINT chat_participants_distinct_peer_chk
        CHECK (user_id <> peer_user_id)
);
-- NO fillfactor here, DELIBERATELY. inbox_seq is an index KEY below, so every
-- send changes an indexed column and HOT is impossible by construction. Setting
-- fillfactor=70 (as three of the four proposals did) buys nothing but 30% more
-- pages and more buffer churn on the hottest small table in the system.

-- SERVES THREE QUERIES WITH ONE STRUCTURE — this is the consolidation that
-- lets us ship exactly one secondary index on this table:
--  (a) foreground resume delta, provably complete because inbox_seq is dense:
--      SELECT conversation_id, peer_user_id, peer_sent_ordinal, peer_ordinal_read
--        FROM chat_participants
--       WHERE user_id=$1 AND inbox_seq > $2
--       ORDER BY inbox_seq LIMIT 200;
--  (b) the inbox screen, keyset-paged, index-only (archived_at filtered from
--      the INCLUDE payload, not from a partial predicate — see note):
--      ... WHERE user_id=$1 AND archived_at IS NULL AND inbox_seq < $2
--          ORDER BY inbox_seq DESC LIMIT 20;
--  (c) the app badge, no COUNT(*) anywhere:
--      SELECT COALESCE(SUM(peer_sent_ordinal - peer_ordinal_read),0)
--        FROM chat_participants
--       WHERE user_id=$1 AND peer_sent_ordinal > peer_ordinal_read;
CREATE INDEX chat_participants_inbox_idx
    ON chat_participants (user_id, inbox_seq DESC)
    INCLUDE (conversation_id, peer_user_id, peer_sent_ordinal,
             peer_ordinal_read, last_read_seq, archived_at, muted_until);
-- NOT partial on archived_at: sync correctness requires every conversation be
-- reachable from this index. An archived thread that receives a message must
-- still appear in the delta. Archiving is a display filter, never a
-- correctness predicate.

-- ============================================================================
-- 4. Messages. HASH-partitioned on conversation_id.
--    Every hot query filters conversation_id, so pruning is exact at plan time
--    and a read touches one partition. RANGE-by-created_at is wrong here:
--    its payoff is DROP PARTITION, which this product forbids, and its cost is
--    that paging one thread fans across every month the pair has talked.
--    32 partitions: the count is permanent, extra partitions cost only planning
--    time (negligible when every query prunes to one), so err high.
-- ============================================================================
CREATE TABLE chat_messages (
    conversation_id uuid   NOT NULL REFERENCES chat_conversations(id),
    seq             bigint NOT NULL CHECK (seq > 0),
    sender_user_id  uuid   REFERENCES users(id),        -- NULL for kind='system'
    sender_ordinal       bigint NOT NULL CHECK (sender_ordinal >= 0),
    -- The peer's ordinal high-water at this moment. Costs 8 bytes and REMOVES
    -- an entire per-message index: without it, mark-read needs
    -- (conversation_id, sender_user_id, seq DESC) to find "the peer's last
    -- message at or before S".
    counterparty_ordinal bigint NOT NULL CHECK (counterparty_ordinal >= 0),
    client_message_id uuid NOT NULL DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('text','image','system')),
    body text NOT NULL DEFAULT '' CHECK (length(body) <= 4000),
    evidence_id uuid REFERENCES evidence_uploads(id),
    -- Context only, NO foreign key: the thread outlives the match and must
    -- survive a draw being regenerated, exactly as progression_events does.
    match_id uuid,
    moderation_state text NOT NULL DEFAULT 'visible'
        CHECK (moderation_state IN ('visible','hidden')),
    redacted_at timestamptz,
    redacted_by uuid REFERENCES users(id),
    -- NO DEFAULT now(). now() is transaction-start time and the seq-assigning
    -- lock is taken several statements later, so now() can INVERT against seq.
    -- The handler supplies GREATEST(clock_timestamp(),
    -- chat_conversations.last_message_at + interval '1 microsecond') under the
    -- conversation row lock, which makes created_at monotone with seq.
    created_at timestamptz NOT NULL,
    PRIMARY KEY (conversation_id, seq),
    CONSTRAINT chat_messages_payload_chk CHECK (
        redacted_at IS NOT NULL
     OR (kind='text'   AND body <> '' AND evidence_id IS NULL     AND sender_user_id IS NOT NULL)
     OR (kind='image'  AND body =  '' AND evidence_id IS NOT NULL AND sender_user_id IS NOT NULL)
     OR (kind='system' AND body <> '' AND evidence_id IS NULL     AND sender_user_id IS NULL)),
    CONSTRAINT chat_messages_redaction_chk
        CHECK ((redacted_at IS NULL) = (redacted_by IS NULL))
) PARTITION BY HASH (conversation_id);

DO $$
BEGIN
    FOR shard IN 0..31 LOOP
        EXECUTE format(
            'CREATE TABLE chat_messages_p%s PARTITION OF chat_messages '
         || 'FOR VALUES WITH (MODULUS 32, REMAINDER %s)',
            lpad(shard::text, 2, '0'), shard);
    END LOOP;
END $$;

-- The PRIMARY KEY above serves THREE hot queries with one structure, each
-- pruning to a single partition. A btree scans backwards, so the DESC page
-- needs no second index:
--   history:     WHERE conversation_id=$1 AND seq < $2 ORDER BY seq DESC LIMIT 30
--   gap repair:  WHERE conversation_id=$1 AND seq BETWEEN $2 AND $3 ORDER BY seq
--   mark-read:   WHERE conversation_id=$1 AND seq = $2       (point lookup)

-- SERVES: the retry-dedupe probe inside the send transaction, run under the
-- conversation row lock BEFORE the counter is bumped.
--   SELECT seq, created_at FROM chat_messages
--    WHERE conversation_id=$1 AND sender_user_id=$2 AND client_message_id=$3;
-- Contains the partition key, so it is legal on a hash-partitioned table.
-- This is what makes a resend over a dropped Safaricom connection return the
-- original message instead of creating a duplicate.
CREATE UNIQUE INDEX chat_messages_client_uidx
    ON chat_messages (conversation_id, sender_user_id, client_message_id);

-- ============================================================================
-- 5. The match link: product feature AND authorization object.
-- ============================================================================
CREATE TABLE chat_match_links (
    match_id        uuid PRIMARY KEY REFERENCES matches(id),
    conversation_id uuid NOT NULL REFERENCES chat_conversations(id),
    competition_id  uuid NOT NULL,
    organization_id uuid NOT NULL,
    from_seq bigint NOT NULL CHECK (from_seq >= 0),
    to_seq   bigint,
    linked_at timestamptz NOT NULL DEFAULT now(),
    closed_at timestamptz,
    CONSTRAINT chat_match_links_window_chk
        CHECK (to_seq IS NULL OR to_seq >= from_seq),
    -- Composite FKs make the denormalised tenant columns VERIFIED rather than
    -- asserted. Uses matches_id_competition_uidx (000009:47) and the
    -- competitions_id_org_uidx created in section 0.
    CONSTRAINT chat_match_links_match_competition_fk
        FOREIGN KEY (match_id, competition_id)
        REFERENCES matches (id, competition_id),
    CONSTRAINT chat_match_links_competition_org_fk
        FOREIGN KEY (competition_id, organization_id)
        REFERENCES competitions (id, organization_id)
);
-- match_id as PRIMARY KEY serves the staff entry point (a reviewer always
-- arrives holding a review, and so a match id) AND enforces one conversation per match, so a
-- retried bracket-advance cannot fork a second thread for one fixture.

-- SERVES: the thread header ("which matches has this conversation covered")
-- and the reverse authorization check, given a conversation and a seq:
--   SELECT match_id, organization_id FROM chat_match_links
--    WHERE conversation_id=$1 AND from_seq <= $2
--    ORDER BY from_seq DESC LIMIT 1;
CREATE INDEX chat_match_links_conversation_idx
    ON chat_match_links (conversation_id, from_seq DESC);

-- ============================================================================
-- 6. Blocks and reports.
-- ============================================================================
CREATE TABLE chat_blocks (
    blocker_user_id uuid NOT NULL REFERENCES users(id),
    blocked_user_id uuid NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (blocker_user_id, blocked_user_id),
    CONSTRAINT chat_blocks_distinct_chk CHECK (blocker_user_id <> blocked_user_id)
);
-- PK SERVES the pre-send gate, one seek added to the send path:
--   SELECT 1 FROM chat_blocks WHERE blocker_user_id=$peer AND blocked_user_id=$me;
-- The alternative is 1,000,000 registered players DMing each other unbounded.

CREATE TABLE chat_reports (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    conversation_id uuid NOT NULL REFERENCES chat_conversations(id),
    reported_by_user_id uuid NOT NULL REFERENCES users(id),
    reported_user_id    uuid NOT NULL REFERENCES users(id),
    -- The window is FROZEN at report time. Staff read exactly this range.
    window_from_seq bigint NOT NULL CHECK (window_from_seq > 0),
    window_to_seq   bigint NOT NULL CHECK (window_to_seq   > 0),
    reason_code text NOT NULL
        CHECK (reason_code IN ('harassment','scam','impersonation','sexual_content','spam','other')),
    note text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    status text NOT NULL DEFAULT 'open'
        CHECK (status IN ('open','under_review','actioned','dismissed')),
    assigned_to uuid REFERENCES users(id),
    created_at  timestamptz NOT NULL DEFAULT now(),
    resolved_at timestamptz,
    CONSTRAINT chat_reports_window_chk CHECK (window_to_seq >= window_from_seq)
);
-- SERVES the moderation queue. Deliberately a first-in-first-out partial index
-- like match_result_reviews_queue_idx (000020), so staff tooling has one queue idiom.
CREATE INDEX chat_reports_queue_idx
    ON chat_reports (status, created_at, id)
    WHERE status IN ('open','under_review');

-- ============================================================================
-- 7. Push tokens. There is no device-token table anywhere in this repository
--    today (only refresh_sessions.device_name), and the socket is
--    foreground-only, so without this the product is silent whenever the app
--    is closed — which on a Transsion or MIUI handset is most of the day.
-- ============================================================================
CREATE TABLE device_push_tokens (
    token text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    platform text NOT NULL CHECK (platform IN ('android','ios','web')),
    app_version text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz
);
-- SERVES the worker's dispatch lookup:
--   SELECT token, platform FROM device_push_tokens
--    WHERE user_id=$1 AND revoked_at IS NULL;
-- Partial so tokens retired by an FCM UNREGISTERED response leave the index.
CREATE INDEX device_push_tokens_user_idx
    ON device_push_tokens (user_id) WHERE revoked_at IS NULL;

-- ============================================================================
-- 8. Push debounce on the EXISTING outbox. Without it, peak chat writes one
--    outbox row per message into a table shared with payments and results.
--    aggregate_id is plain text (000001:249), so the key carries the RECIPIENT
--    dimension — keying on conversation alone lets the two directions of a 1:1
--    thread overwrite each other and silently drop one player's notification.
-- ============================================================================
CREATE UNIQUE INDEX outbox_chat_pending_uidx
    ON outbox_events (aggregate_id)
    WHERE processed_at IS NULL AND event_type = 'chat.message';
-- Send transaction upserts with aggregate_id = conversation_id || ':' || recipient:
--   ON CONFLICT (aggregate_id)
--     WHERE processed_at IS NULL AND event_type='chat.message'
--   DO UPDATE SET payload = outbox_events.payload || EXCLUDED.payload
--                        || jsonb_build_object('unreadFromSeq',
--                             LEAST((outbox_events.payload->>'unreadFromSeq')::bigint,
--                                   (EXCLUDED.payload->>'unreadFromSeq')::bigint));
-- LEAST, not overwrite: unreadFromSeq must mark the START of the unread run.
-- occurred_at is NOT touched on conflict — it is the tiebreak column of
-- outbox_delivery_idx and rewriting it starves the oldest event.

-- ============================================================================
-- 9. Reuse the evidence pipeline. Expand-then-contract on the CHECK: add the
--    looser constraint NOT VALID, validate, then drop the narrower one.
--    Dropping a narrower constraint never breaks a reader.
--    The bound_kind CHECK in 000006:15 is an unnamed COLUMN check, so
--    PostgreSQL generated its name. Resolve it rather than guessing.
-- ============================================================================
ALTER TABLE evidence_uploads
    ADD CONSTRAINT evidence_uploads_bound_kind_v2_chk
        CHECK (bound_kind IN ('result_submission','result_dispute','dispute_case',
                              'game_account_verification','match_result_report','chat_message')) NOT VALID,
    -- image/webp: 25-35% smaller at equal quality. The single cheapest
    -- metered-data win available, and the only place in this migration that
    -- touches a shared table's value domain.
    ADD CONSTRAINT evidence_uploads_media_type_v2_chk
        CHECK (media_type IN ('image/jpeg','image/png','image/heic','image/heif','image/webp')) NOT VALID;

ALTER TABLE evidence_uploads VALIDATE CONSTRAINT evidence_uploads_bound_kind_v2_chk;
ALTER TABLE evidence_uploads VALIDATE CONSTRAINT evidence_uploads_media_type_v2_chk;

DO $$
DECLARE old_name text;
BEGIN
    FOR old_name IN
        SELECT conname FROM pg_constraint
         WHERE conrelid = 'evidence_uploads'::regclass
           AND contype  = 'c'
           AND conname IN ('evidence_uploads_bound_kind_check',
                           'evidence_uploads_media_type_check')
    LOOP
        EXECUTE format('ALTER TABLE evidence_uploads DROP CONSTRAINT %I', old_name);
    END LOOP;
END $$;

-- NO new index and NO new column on evidence_uploads. For a chat image,
-- bound_id is the CONVERSATION id, not a message id — (conversation_id, seq) is
-- the message identity and bound_id is a single uuid column. Binding to the
-- conversation is also the correct authorization anchor: "may this user see
-- this image" is answered by conversation membership. The existing
-- evidence_uploads_bound_idx (bound_kind, bound_id) WHERE bound_id IS NOT NULL
-- (000006:29-30) serves it unchanged.

COMMIT;
```

### Indexes explicitly REJECTED

The founder asked for heavy indexing. Heavy indexing means every index earns its write; it does not mean many indexes. `chat_messages` gets exactly two, and `chat_participants` gets exactly one.

| Rejected | Why |
|---|---|
| `chat_participants_unread_idx (user_id) WHERE peer_sent_ordinal > peer_ordinal_read` | Cargo cult. `chat_participants_inbox_idx` is already keyed on `user_id` first and already `INCLUDE`s both counters, so the badge is **already** an index-only scan over that user's own entries. A second index on the hottest small table costs a write on every message and every read receipt, and its partial predicate *flips* on both — constant insert/delete churn plus autovacuum load — to serve a marginal scan reduction. Three of the four proposals included it. |
| `chat_messages_author_idx (sender_user_id, created_at DESC)` | Costs a write on every message across all 32 partitions to serve a moderation query run dozens of times a day, and because it is a local index the planner runs 32 scans and merges. Moderators enter through a report that already names `(conversation_id, window_from_seq, window_to_seq)` and reach the transcript by primary key. The winning proposal's own author said they would ship without it. Agreed. |
| `chat_messages_evidence_uidx UNIQUE (conversation_id, evidence_id)` | Enforces "the same image cannot be posted twice in one thread" — a requirement nobody stated. The reverse `evidence → message` lookup is unnecessary once `bound_id` points at the conversation, because download authorization resolves through `chat_participants`. |
| `chat_messages_public_uidx UNIQUE (conversation_id, id)` | A third unique index on the hottest insert path to give messages a uuid identity nobody needs. Message identity **is** `(conversation_id, seq)`, and every public reference already carries `conversationId`. Rejected along with the `evidence_uploads.bound_conversation_id` column and composite FK that one proposal needed to support it. |
| GIN / `pg_trgm` on `chat_messages.body` | The single most expensive index available: pending-list flush amplification on the highest-write table, and it would build exactly the searchable corpus `docs/architecture.md:99-100` says must not exist. Search is deferred; if it ships it ships scoped to reported conversations. |
| Any index on `chat_messages.created_at` | Every message read is anchored on a conversation, and within a conversation `seq` order **is** time order by construction (`created_at` is now monotone with `seq`). Pure write cost. |
| Any index on `chat_participants.last_activity_at` | It is now a display column only. Removing it from every correctness path — sync, inbox ordering, resume — is the point of `inbox_seq`. |
| `fillfactor = 70` on `chat_participants` | HOT requires that no indexed column changes. `inbox_seq` is an index key. HOT is impossible by construction; the fillfactor buys 30% more pages and nothing else. Kept on `chat_conversations` and `chat_user_cursors`, where the changing columns genuinely are unindexed. |

---

## 5. The send transaction

Order is the design. Deadlock-free because both sends in a conversation take the conversation row lock first, and the two cursor locks are taken in ascending `user_id` order, giving a total order over all lock acquisitions.

```
BEGIN;
  -- 1. Serialize this pair and load the peer + block state.
  SELECT last_seq, last_message_at, user_low_id, user_high_id
    FROM chat_conversations WHERE id=$conv FOR UPDATE;

  -- 2. Retry probe. MUST be after the lock and before the bump: bumping first
  --    and letting the unique index reject the duplicate burns a sequence
  --    number on every retry, and on this network retries are the common case.
  SELECT seq, created_at FROM chat_messages
   WHERE conversation_id=$conv AND sender_user_id=$me AND client_message_id=$cid;
  -- found -> COMMIT and return that seq. The counter is never touched.

  -- 3. Allocate. NOT a SEQUENCE: nextval is non-transactional and leaves holes
  --    on rollback, and a hole is indistinguishable from a lost message.
  UPDATE chat_conversations
     SET last_seq = last_seq + 1,
         last_message_at = GREATEST(clock_timestamp(),
                                    last_message_at + interval '1 microsecond')
   WHERE id=$conv RETURNING last_seq, last_message_at;      -- $seq, $ts

  -- 4. Both user cursors, ascending user_id. Deterministic order = no deadlock.
  UPDATE chat_user_cursors SET inbox_seq = inbox_seq + 1
   WHERE user_id = $lowUser  RETURNING inbox_seq;
  UPDATE chat_user_cursors SET inbox_seq = inbox_seq + 1
   WHERE user_id = $highUser RETURNING inbox_seq;
  -- assert RowsAffected()==1 on BOTH.

  -- 5. Participants. Assert RowsAffected()==1 on BOTH — a zero-row UPDATE here
  --    commits a message that is invisible in every read path the recipient
  --    uses. Same guard as result_handlers.go:432.
  UPDATE chat_participants
     SET sent_ordinal = sent_ordinal + 1,
         inbox_seq = $myInboxSeq, last_activity_at = $ts
   WHERE conversation_id=$conv AND user_id=$me
   RETURNING sent_ordinal, peer_sent_ordinal;               -- $ord, $ctr

  UPDATE chat_participants
     SET peer_sent_ordinal = peer_sent_ordinal + 1,
         inbox_seq = $peerInboxSeq, last_activity_at = $ts
   WHERE conversation_id=$conv AND user_id=$peer;

  -- 6. The message.
  INSERT INTO chat_messages
    (conversation_id, seq, sender_user_id, sender_ordinal, counterparty_ordinal,
     client_message_id, kind, body, evidence_id, match_id, created_at)
  VALUES ($conv, $seq, $me, $ord, $ctr, $cid, $kind, $body, $ev, $match, $ts);

  -- 7. Images only. Hard precondition, copying result_handlers.go:430-431 and
  --    adding the ownership predicate that path is missing:
  UPDATE evidence_uploads
     SET bound_kind='chat_message', bound_id=$conv, bound_at=now(), updated_at=now()
   WHERE id=$ev AND bound_id IS NULL AND status='completed' AND owner_user_id=$me;
  -- abort unless RowsAffected()==1.

  -- 8. Debounced push.
  INSERT INTO outbox_events (aggregate_type, aggregate_id, event_type, payload)
  VALUES ('chat_conversation', $conv || ':' || $peer, 'chat.message', ...)
  ON CONFLICT ... DO UPDATE ...;
COMMIT;
-- THEN SPUBLISH. Never before.
```

Collapse steps 1–8 into a single `plpgsql` function or one CTE chain. As written across round trips, the conversation row lock is held across seven network hops; under the reconnect storm the design fears, that hold time multiplies against 8 writer connections per pod.

---

## 6. Unread without `COUNT(*)`

Three bigints on the reader's **own** participant row, all monotone:

```
unread(me, C) = peer_sent_ordinal - peer_ordinal_read     -- subtraction, ONE row
badge(me)     = SUM(peer_sent_ordinal - peer_ordinal_read)
                  FROM chat_participants
                 WHERE user_id=$1 AND peer_sent_ordinal > peer_ordinal_read
```

`badge` is an index-only scan over that user's own entries in `chat_participants_inbox_idx` — no heap fetch, no second index, no aggregate over `chat_messages`. Sending your own message never touches `peer_ordinal_read`, so unread cannot be destroyed by the recipient replying (the failure the SSE judge constructed).

Mark-read at seq `S` is a **single primary-key lookup**, no aggregate. Read the row at `(C, S)`; if the peer wrote it, `read_to = sender_ordinal`, else `read_to = counterparty_ordinal`. Then:

```sql
UPDATE chat_participants
   SET peer_ordinal_read = GREATEST(peer_ordinal_read, LEAST($read_to, peer_sent_ordinal)),
       last_read_seq     = GREATEST(last_read_seq, $S)
 WHERE conversation_id=$C AND user_id=$me;
```

`GREATEST` makes it monotone — idempotent under retry and safe when receipts arrive out of order, which they will. `LEAST` clamps against a buggy or hostile client, and `chat_participants_read_le_sent_chk` turns a violation into a `23514` in tests rather than a duplicate push in production. Both are required: a second device resuming after an hour reports a stale, lower cursor, and without `GREATEST` it drives the count backwards and resurrects read messages.

`counterparty_ordinal` on the message costs 8 bytes and removes an entire per-message index. That is a write saved on the hottest table in the system.

---

## 7. Gap repair after a long offline period

Three levels, and the crucial property is that **each is dense**, so "missing" is arithmetic rather than a heuristic.

**Level 1 — within a conversation.** `seq` is gapless per conversation. `seq != last + 1` means exactly one thing: a message is missing. The client requests `seq BETWEEN a AND b`.

**Level 2 — across conversations.** `inbox_seq` is a dense per-user counter. `POST /v1/chat/sync {inboxSeq, watermarks[]}` returns every conversation with `inbox_seq > N`, ordered by `inbox_seq`, bounded at 200 with `nextInboxSeq` for paging. **A thread the client has never heard of is enumerated.** This is the hole in all four original designs: they all keyed cross-conversation resume on the client's list of known conversations plus a timestamp, so a first-ever thread opened while the phone was suspended was returned by neither the watermark replay nor the delta.

**Level 3 — the client's persisted state.** The client persists per conversation `{contiguousSeq, pendingGaps[]}`, **not** `max(seq)`.

- `contiguousSeq` is the highest N such that the client holds every seq `1..N`.
- `pendingGaps` is written to local storage **at detection time, before the sync request is sent**, and cleared only when the range is fully materialised.

Without this, a gap detected mid-session but not repaired before the app is killed is erased on relaunch — the client's `hello` sends the max, seq 7 is missing forever, and no future message, resync or push will ever reveal it. That is permanent, undetectable loss and it was in the winning proposal as written.

**The last-message hole.** Gap detection only fires when a *later* message arrives. If the final message of a conversation is dropped, no successor exposes it. Two closures:

1. **In-band, ships day one, costs nothing.** The 45s server keepalive carries `{conversationId, lastSeq}` for the conversation the client currently has open, read from the pod's already-held counters. Detection drops from "next foreground" to ≤45s with no new binary and no extra data cost. The winning proposal declared this hole unclosable pending `cmd/worker`; it isn't.
2. **Out-of-band.** The outbox push after ~10s if `peer_ordinal_read < peer_sent_ordinal`. This is also what wakes a suspended Android app.

**Read routing.** Gap-fill and `sync` carry `expect_seq`. Serve from `db.Reader` behind the existing `ReaderLag` gate; if the reader's highest returned seq is below `expect_seq`, re-run on `db.Writer` through the existing `writerFallback` semaphore. A replica that has not applied seq N+1 returns fewer rows, and a client that reads that as "I'm caught up" has silently lost a message — the same class of bug `organizer_access.go` avoids by pinning membership to the writer. Pinning *everything* to the writer instead (as the winning proposal did) points the entire reconnect herd at 8 connections per pod.

**Admission control for repair must not live in Redis.** Gate `sync` with an **in-process token bucket per pod**. A Redis-pressure event is precisely what triggers bulk resync; throttling the repair through the subsystem whose failure caused it means denials become unrepaired gaps. Keep `redisFixedWindow` only as a per-user abuse ceiling, never as the gate that can deny a repair.

---

## 8. Go package shape

Follows the existing convention: pure domain logic in `internal/<domain>`, SQL and HTTP in `internal/httpapi`, infrastructure in its own package (`internal/storage`, `internal/database`).

```
internal/chat/            NEW  pure domain. No SQL, no HTTP, no Redis.
internal/realtime/        NEW  socket hub, Redis fan-out, frame codec, ticket auth.
internal/httpapi/chat_*.go        NEW  handlers + SQL, mirrors result_review_*.go
internal/httpapi/platform_access.go EDIT one platform permission
internal/httpapi/server.go        EDIT three blockers (below)
internal/config/config.go         EDIT REDIS_REALTIME_URL, CHAT_IMAGE_MAX_BYTES, FCM creds
cmd/worker/               NEW  first background binary in the repo
```

```go
package chat

// PairKey returns the canonical ordered pair. ok is false when a == b.
// The caller sorts before every conversation lookup; the schema CHECK is the
// backstop, not the mechanism.
func PairKey(a, b string) (low, high string, ok bool)

// Unread is the entire unread computation. There is no other one.
func Unread(peerSent, peerRead int64) int64

// Gap is a half-open-free inclusive range of missing sequence numbers.
type Gap struct{ From, To int64 }

// Watermark is exactly what a Flutter client persists per conversation.
// ContiguousSeq is the highest N for which every seq in 1..N is held locally —
// NOT the highest seq observed. PendingGaps is durable and survives app death.
type Watermark struct {
    ConversationID string
    ContiguousSeq  int64
    PendingGaps    []Gap
}

// Plan bounds a resume so a week-away client cannot trigger an unbounded
// backfill. Returns ResyncRequired when the delta exceeds the caps, at which
// point the client falls back to REST paging.
type ResumePlan struct {
    Fetch          []ConversationRange
    ResyncRequired bool
    NextInboxSeq   int64
}
type ConversationRange struct {
    ConversationID string
    Gap
}
func Plan(w []Watermark, server map[string]int64, maxConversations, maxMessages int) ResumePlan

// ReadTo resolves how far a mark-read at seq S advances the reader's ordinal,
// given the message row at (C, S). Pure, so it is unit-testable without a DB.
func ReadTo(senderIsPeer bool, senderOrdinal, counterpartyOrdinal int64) int64

const (
    MaxBody             = 4000
    MaxResumeConvs      = 50
    MaxResumeMessages   = 200
    KeepaliveInterval   = 45 * time.Second
)
```

```go
package realtime

// Hub owns this pod's sockets. It holds connections, never state: a pod can be
// killed at any time and the seq watermark repairs the client.
type Hub struct{ /* ... */ }

func NewHub(logger *slog.Logger, maxConns int) *Hub
func (h *Hub) Register(userID string, c *Conn) (release func())
func (h *Hub) Deliver(userID string, frame []byte) int   // fan-in from Subscriber
func (h *Hub) Len() int

// Drain closes every socket over the window with a per-connection jittered
// reconnectAfter carried in the bye frame. It does NOT rely on
// http.Server.Shutdown: Shutdown neither closes nor waits for hijacked
// connections, so without this the process exits killing sockets mid-flight
// with no bye frames — exactly the undirected spike the drain exists to prevent.
// Register it via http.Server.RegisterOnShutdown so it cannot be forgotten.
func (h *Hub) Drain(ctx context.Context, window time.Duration) error

type Conn struct{ /* ... */ }
func (c *Conn) Send(ctx context.Context, frame []byte) error
func (c *Conn) Close(code int, reason string) error

// Publisher/Subscriber isolate the fan-out so it can be swapped without
// touching the hub. SPUBLISH with a {u:<id>} hash tag, never PUBLISH: in Redis
// Cluster plain PUBLISH broadcasts to every node.
type Publisher interface {
    Publish(ctx context.Context, userID string, frame []byte) error
}
type Subscriber interface {
    // Run blocks. On subscriber-connection loss it reconnects and invokes
    // onResubscribe, which pushes resyncRequired to every socket on this pod —
    // Redis kills a subscriber that exceeds client-output-buffer-limit pubsub,
    // silently dropping every message for all of a pod's users until resubscribe.
    Run(ctx context.Context, deliver func(userID string, frame []byte), onResubscribe func()) error
}
func NewRedisFanout(client *redis.Client, namespace string, shards int) (Publisher, Subscriber)

// Ticket carries socket identity for clients that cannot set Authorization on
// a WebSocket handshake (browsers). It is a signed token from the existing
// gamicsauth.TokenManager under a domain-separated secret — NOT a Redis-only
// value. Redis holds a single-use jti marker; if Redis is down the ticket path
// still authenticates, and Redis is never the sole carrier of an identity claim.
type TicketIssuer struct{ /* ... */ }
func NewTicketIssuer(secret string, ttl time.Duration) *TicketIssuer
func (t *TicketIssuer) Issue(userID, sessionID string) (token, jti string, err error)
func (t *TicketIssuer) Parse(token string) (userID, sessionID, jti string, err error)
```

```go
package httpapi   // platform_access.go, +1 permission

// platformChatRead reads the message window a match opened, for a Gamics result
// review. Granted to reviewer and admin, the roles that already hold
// platformResultReviewManage. No organization role gets it: organizers never
// decide results, and docs/architecture.md states raw chat is not an analytics
// product.
const platformChatRead platformPermission = "chat.read"
```

### Three existing-code blockers — all verified, all silent at compile time

1. **`responseMetricsWriter` (server.go:239-262) implements only `WriteHeader` and `Write`.** No `Hijack`, and — critically — no `Unwrap() http.ResponseWriter`. `http.NewResponseController(w).Hijack()` walks `Unwrap` and returns `ErrNotSupported`. **The upgrade cannot happen at all.** Fix: add `func (w *responseMetricsWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }`.
2. **`s.middleware` wraps *every* request in `context.WithTimeout(r.Context(), s.config.RequestTimeout)` (server.go:233).** The socket's context is cancelled mid-life. `s.middleware(mux)` wraps the whole mux, so no route registration can opt out — the middleware itself must skip the wrap for the socket path, using the same prefix idiom as `sensitivePath`.
3. **`WriteTimeout` is applied globally (server.go:110-118).** Every socket dies at 30 seconds. Fix: clear both deadlines after hijack and manage them per frame via `ResponseController.SetWriteDeadline` — which requires fix 1.

Blockers 2 and 3 fail as a mysterious disconnect pattern in production, not as a test failure.

### Two more that will bite

- **`SHUTDOWN_TIMEOUT` defaults to 10s** (config.go:91) and `Run` hard-caps the drain at exactly that. A 90-second staggered drain is SIGKILLed at 30s (the Kubernetes grace default) with two-thirds of the pod's sockets still open, dying with no bye frame — *worse* than no drain. Set `SHUTDOWN_TIMEOUT=120s`, `terminationGracePeriodSeconds=150`, and a `preStop` sleep sized to LB deregistration. Constrain jitter window < shutdown timeout < grace period, and state all three together.
- **Do not re-run `sessionActive` on a per-socket timer.** `maxPositiveSessionCacheTTL = 5 * time.Second` (request_security.go:23) caps the positive cache regardless of `AUTH_SESSION_CACHE_TTL`, so a 60s liveness poll misses cache **100% of the time** and lands on `db.Writer` with a two-table join. At 100k sockets that is ~1,700 writer QPS of pure liveness polling through 8 connections per pod — larger than the entire chat write load. Replace polling with a `session:revoked:<sessionID>` pub/sub event published by the existing logout handler, plus one `sessionActive` call at handshake and one per `reauth` frame (once per 15-minute access token). That is O(logout events) instead of O(sockets / 60s). Put socket revocation traffic on `REDIS_REALTIME_URL`, not `s.redis`, or the third Redis achieves none of the isolation it was added for.

---

## 9. Staff access, and what players are told

**Reuse `platformRoute`. Add exactly one permission.**

```go
mux.Handle("GET /v1/admin/result-reviews/{id}/chat",
    s.platformRoute(platformChatRead, s.getResultReviewChatTranscript))
```

Mounting through `platformRoute` means the route cannot be registered without naming a permission, and the staff role resolves from `s.db.Writer`, never a replica, because replication lag on an authorization check is an authorization bug.

**Three gates the middleware cannot know about, all in SQL:**

1. The review must be `queued`, and the reviewer must pass `resultReviewConflictClause`: nobody reads the chat of a match they play in, captain in or organize. Staff cannot read chat for matches proceeding normally.
2. The conversation is the one `chat_match_links` holds for the review's match. The URL names only the review, never a conversation or a user.
3. `seq BETWEEN from_seq AND COALESCE(to_seq, chat_conversations.last_seq)`. A reviewer reads the window that match opened — nothing before it, nothing from another tournament, nothing from the friendlies that continued afterwards.

**The audit row is in the same transaction as the SELECT**, reusing the existing `audit_events` table and `audit_subject_idx (subject_type, subject_id, occurred_at DESC)` (000001:241). No new audit infrastructure, no new index:

```sql
BEGIN;
  INSERT INTO audit_events (organization_id, actor_user_id, action, subject_type,
                            subject_id, request_id, after_state)
  VALUES (NULL, $staff, 'chat.transcript.read', 'conversation', $convID, $reqID,
          jsonb_build_object('matchId',$m,'reviewId',$r,'fromSeq',$a,'toSeq',$b,'messageCount',$n));
  SELECT seq, sender_user_id, kind, body, evidence_id, created_at
    FROM chat_messages WHERE conversation_id=$1 AND seq BETWEEN $a AND $b ORDER BY seq;
COMMIT;
```

If the audit insert fails the transaction aborts and no transcript is returned. That is the point of one transaction rather than logging after the fact: **no transcript can leave the database unaudited.** `moderation_state='hidden'` rows are returned with a redacted body, never filtered out — filtering breaks `seq` density on the read path and stalls the client's contiguous watermark forever.

**The honest gap.** The review gate is match-scoped. A friendly has no match, therefore no review, therefore nothing for the gate to resolve against. **No organizer can read any chat, and no reviewer can read a friendly through this route, and that is correct.** But it means harassment in friendlies is unmoderatable until a report-gated platform route exists. `chat_reports` ships in v1 so the evidence is captured with a frozen window at report time; the report read route is v1.1 (see delivery order). Do not silently stretch the review route to cover it.

### What players are told, verbatim in the product

End-to-end encryption and staff readability are mutually exclusive. The founder chose moderation and result review, so messages are readable server-side. That must be said out loud, not buried in a policy PDF:

- **A `kind='system'` message is the first message in every conversation:** *"Gamics staff can read the messages in this chat that relate to a tournament match, and only for that match, when Gamics reviews its result. Gamics staff can read a conversation you report. Tournament organizers cannot read your chats. Messages are stored and are not deleted."*
- `chat_participants.disclosure_shown_at` records that each player saw it.
- The privacy policy must state plainly: **this system has WhatsApp's latency goals and none of its privacy guarantees.** Nobody should later claim parity on privacy because the chat feels equally fast.
- Every staff read is answerable to the player: `audit_subject_idx` makes "who read my messages, when, and under which case number" one indexed query. Build that into the DSAR path from day one, not after the first request arrives.

---

## 10. Delivery order

The key sequencing decision: **chat over plain REST ships before the socket.** Real-time is an upgrade, not the MVP. Players need "here is my Friend Match code" working in week 3, not a perfect socket in week 10.

| # | Ships | Effort | What players feel |
|---|---|---|---|
| 1 | Unblock the server: `Unwrap()`, middleware path exemption, per-frame deadlines, `SHUTDOWN_TIMEOUT` + grace period | 0.5w | Nothing |
| 2 | **Migration 000011 + send / history / sync / read over plain HTTP.** No socket. Client polls every 5s while a conversation is visible | **2w** | **Chat works.** Text messages, one thread per opponent, friendlies, unread badges. The whole product loop |
| 3 | **`cmd/worker` + FCM + `device_push_tokens`.** Leased outbox drain (`FOR UPDATE SKIP LOCKED` on `outbox_delivery_idx`, bump `available_at`, dead-letter on `attempts`). `priority=high`, one per-user `collapse_key`, no message body in the payload | **2.5w** | **Notifications when the app is closed.** On a Transsion/MIUI handset this is worth more than the socket |
| 4 | **WebSocket + Redis SPUBLISH fan-out + hub + staggered drain + revocation channel** | **3w** | **Instant.** Sub-second delivery, typing-speed responsiveness |
| 5 | Images: reuse the presign / direct-PUT / SHA-256 flow verbatim, add `CHAT_IMAGE_MAX_BYTES` (2 MiB, vs the shared 25 MiB ceiling), client-side downscale to WebP, worker-generated ~15KB thumbnail | 1.5w | Screenshots, and they don't eat the bundle |
| 6 | Review transcript route + audit + `chat_reports` + block/report UI | 1.5w | Reviewers see match chat; reporting exists |
| 7 | **Load-test the reconnect storm.** Kill -9 a pod at full socket count, not just the graceful case | 1w | Nothing, until the day it saves the launch |
| 8 | v1.1: a report-gated `platformRoute` transcript read, for friendlies | 1w | Friendlies become moderatable |

**~13 weeks, and item 7 is not optional.** The winning proposal estimated 11 and did not contain push at all.

Item 3 before item 4 is deliberate and I will defend it: a socket that only works while the app is foregrounded, with no push, is a chat app that is silent for most of a Kenyan user's day. Shipping the socket first optimises the case that already works.

---

## 11. Top 3 risks

**1. Push is the product on these handsets, and it is the piece most likely to slip.**
The socket is foreground-only by design — correct, because Doze and the Transsion/MIUI battery managers will kill it anyway and fighting them with a foreground service burns battery and gets the app flagged. That makes FCM the *primary* delivery surface, not a fallback. There is no device-token table, no worker binary, no FCM config, no token-rotation or `UNREGISTERED` handling anywhere in the repo today. **Cost of getting it wrong:** the founder ships a chat that never notifies anyone, players miss match times, the product fails at its one job — and the diagnosis will land on the socket, which is working fine. Also get the mechanics right on the named handsets: data-only messages require app code to run, and a swiped-away app on HiOS/XOS does not run it. Use a notification+data hybrid so the tray renders without the process, and decide explicitly whether the banner carries a preview (which ships content to Google, contradicting the residency story) or just the sender handle.

**2. The auth cost of a long-lived socket lands on the writer, and it takes payments down with it.**
Verified: `maxPositiveSessionCacheTTL = 5 * time.Second` caps the positive session cache, so any periodic `sessionActive` re-check on a socket **always** misses Redis and hits `s.db.Writer` through 8 connections per pod. At 100k sockets a 60-second liveness poll is ~1,700 writer QPS of pure polling. **Cost of getting it wrong:** the failure is not "chat is slow" — the writer pool is the same one serving M-Pesa STK pushes, registrations and result submissions. Chat concurrency becomes a platform-wide outage, and it surfaces at 10k concurrent, not at the design point. The fix (revocation pub/sub instead of polling) is cheap; missing it is not.

**3. Pair-keyed conversations are a product assumption, not an engineering one.**
The gapless counter, the unread arithmetic, the sync cursor and every client cache are all built on "one thread per pair, forever". If the founder actually wants a thread per match — separate rooms, separate history, tidy per-fixture moderation — then `chat_conversations_pair_uidx` is exactly wrong. **Cost of getting it wrong:** this is not a column addition. It is a rewrite of the identity model, the sequencing model, the unread model and the client's local store, after messages exist that can never be deleted. **Confirm this before the migration runs.** It is a five-minute conversation now and a six-week rewrite later.

---

## 12. Do not build in v1

- **Typing indicators.** They multiply the fan-out rate by an order of magnitude for zero durable value and real battery cost. If they ever ship they go through Redis pub/sub only, lossy — the one place Redis-as-truth is acceptable, because losing "typing" costs nothing.
- **Presence / "last seen".** Same argument, plus it leaks online status between players who just contested a result against each other.
- **Per-message read receipts.** One read cursor per participant is enough. Multi-device delivered/read state is a distributed-systems project of its own.
- **Message search.** A GIN trigram index on the highest-write table in the product, building exactly the searchable corpus `docs/architecture.md:99-100` says must not exist.
- **Editing, deleting, reactions, replies, forwarding.** "Messages are never deleted" is a requirement; edit history is a second requirement nobody stated.
- **Group chat.** Structurally forbidden by the schema. That is the feature, not a limitation.
- **End-to-end encryption.** Mutually exclusive with the staff-read requirement. Do not half-build it.
- **`permessage-deflate`.** A 32KB zlib window per direction per connection; at 100k sockets that is tens of GB of RAM for compression state on 60–300 byte frames where deflate saves nothing. Short JSON keys and envelope-only above 4KB do the real work.
- **Protobuf / a binary wire format.** JSON is free on both ends and text bodies dominate the payload.
- **Sticky sessions / consistent-hash socket routing.** A pod holds sockets, not state. Sticky routing makes pods stateful and makes every deploy a rebalance.
- **Multi-region.** Cross-region pub/sub replication is not designed here at all. Single region, and say so.
- **A platform-staff *browse* endpoint.** Case-gated reads only. There must be no route that takes a conversation id or a user id without a report or a Gamics review attached. This is the object most likely to be loosened later by someone who just wants to debug something, and the blast radius of loosening it is every private conversation on Gamics.
