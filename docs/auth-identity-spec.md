# Player identity: passwordless signup, Konami-ID recovery, avatars

Produced by a 4-agent design pass (schema/index, auth flows, abuse/privacy, synthesis) and
reviewed against the source. The four load-bearing claims about existing behaviour were
independently verified: the createGameAccount 409 oracle, HashOTP email binding, verifyOTP
auto-creating users, and the PresignGet 1-15 minute clamp.

Status: **superseded, never implemented.** On 2026-10-07 the product decision changed:
players register with only a username, Konami ID and password and sign in with the Konami
ID and password; email and phone are added after registration. See
`docs/mobile-api-requirements.md` and `services/api/openapi/openapi.yaml` for the
implemented contract. Kept for its reasoning: the Konami ID stays on `game_accounts`
behind a normalized unique index, phones are country-neutral, and one account per phone.

---
# Gamics 000010 — Player Identity Implementation Spec

Merged from three scouting reports. Source read: `auth_handlers.go`, `account_handlers.go`, `request_security.go`, `result_handlers.go`, `server.go`, `helpers.go`, `internal/auth/tokens.go`, `internal/username/generate.go`, `internal/storage/s3.go`, `internal/database/cluster.go`, `internal/config/config.go`, migrations 000001/000002/000004/000006/000007.

---

## 0. Disagreements resolved

| # | Question | Scout positions | Taken | Why |
|---|---|---|---|---|
| 1 | Where does the Konami ID live? | S1: stays on `game_accounts`. S2: same. S3: same. | **`game_accounts.publisher_player_id`** | Unanimous and correct. It is per-game; `game_accounts` already carries `verification_status DEFAULT 'unverified'`, which is the only honest thing we know about it. `game_accounts_id_user_uidx` already anchors two FK pairs to that row. |
| 2 | How is the ID made recoverable? | S1: `GENERATED ALWAYS AS ... STORED` column + partial unique index. S2: no normalization, exact match on the existing index. S3: non-unique expression index `(game_id, upper(...))`, multi-row match treated as a miss. | **Neither — a UNIQUE expression index on the normalized value, no new column.** | S1 is right that a byte-exact index over a human-typed string is not a recovery key, but its generated column forces a full table rewrite under ACCESS EXCLUSIVE, and the runner (`cluster.go:177-182`) wraps each file in one tx so `CONCURRENTLY` is impossible. S3 avoids the rewrite but its non-unique index lets case/separator variants coexist, so its own mitigation is "silently refuse to recover those players". A unique **expression** index gets S1's single-row guarantee at S3's cost (SHARE lock, no rewrite). S2 is wrong: a player retyping `1234-5678` as `12345678` is locked out and can also mint a second account that dodges the constraint. |
| 3 | Konami ID shape CHECK in the DB | S2: `^[0-9]{6,20}$` NOT VALID. S1: length bound only. | **S1 — length ≤ 64 only.** | A NOT VALID check still rejects *new* rows. S2's own risk note says the digit range is an assumption; Konami publishes no API. Getting it wrong rejects real players at signup with no appeal and needs a migration to widen. Shape lives in Go where it is a config change. **Founder decision flagged.** |
| 4 | Does recovery get its own tables/endpoints? | S3: new `/v1/auth/recovery/*`, `account_recovery_challenges` with decoy rows, challenge-id-keyed verify. S2: Konami ID as an alternate identifier on the existing `/v1/auth/otp/*`. S1: same as S2 plus a forensic column. | **S2's shape + S3's uniformity discipline. Reject the decoy table.** | `HashOTP(secret, email, code)` (`tokens.go:105`) binds the code to the email either way, so a second table buys only decoy rows. Decoys exist solely to make the *Redis-down* DB-fallback counter see misses — while `createGameAccount` returns `409 game_account_exists` for free, unlimited, today (`account_handlers.go:209`). Building a decoy table while that door stands open is misprioritized. Close the 409 oracle instead (§4.4). |
| 5 | How does a Konami-recovering player *verify*? | S1: unchanged email-keyed verify. S2: `konamiId` also accepted at verify, re-resolved. S3: opaque `challengeId`. | **S2.** | S1 is unshippable: the player recovering by Konami ID is precisely the player who does not remember their email, but S1's verify still demands it. S3 solves it with a whole parallel table. S2 re-resolves the same lookup — free, and it fails closed if the binding moved between request and verify. |
| 6 | Does `verifyOTP` keep auto-creating users? | S2: no — signup ticket. S1/S3: silent. | **S2 — remove it, add `signup_tickets`.** | Signup must collect phone + Konami ID + name; an unknown email can no longer silently become a nameless account. The ticket keeps `verifyOTP` single-purpose and gives the client a two-screen flow. Considered and rejected: a stateless signed ticket (no revocation, no supersede) and stuffing the signup payload into verify (mixes concerns). **This is a breaking client change — see §6.** |
| 7 | Avatar storage table | S1: new `avatar_uploads`. S2: widen `evidence_uploads.bound_kind` to `player_avatar`. | **S1's table, S2's serving route.** | The brief says reuse the *presign pattern*, and we do — byte for byte. But evidence is immutable, retained, dispute-bound, and `getEvidenceAccess` joins `bound_id` to `result_submissions` (`result_handlers.go:286`); an avatar is mutable, single-current, and must be purgeable. Reusing the table also means DROPping an auto-named inline CHECK (`000006:15`) on a live DB, which both S2 and S3 flagged as risky. Two lifecycles, two tables. |
| 8 | Avatar serving | S1: `PresignGet` per row, cache TTL must be < presign TTL. S2: `GET /v1/players/{id}/avatar` → 302. | **S2.** | `PresignGet` clamps TTL to 1–15 min (`s3.go:132`) and there is a `ResponseCache` (`server.go:74`). A stable path is cacheable; the presign happens at fetch. Also removes any join from list queries. |
| 9 | `users.phone_e164` CHECK | S1: country-neutral digits. S2: strict `^254[17][0-9]{8}$`. | **S1.** | S2's rule on `users` blocks every non-Kenyan signup forever and needs a migration to widen. The strict Safaricom rule already lives on `payment_intents.phone_e164` (`000003:16`) where a payout actually depends on it. Go still calls `normalizeKenyanPhone` at signup, so behaviour is identical today and widening is a Go change. |
| 10 | `in_game_name` at signup | S1: `DEFAULT ''`. S2: default it to the player's real name. | **S1.** | `in_game_name` is trigram-searched in public player search (`player_queries.go:200-207`). Writing the player's real name there publishes it. S1's blank-name hazard is real but is contained by the handle loop failing closed (§3) plus the COALESCE hardening in §6. |
| 11 | `game_account_claims` queue | S3: ship table + 2 endpoints. | **Reject.** | S3's own finding: there is no platform-staff role (`internal/organizer/roles.go` is org-scoped only), so nothing can adjudicate a claim. A queue nobody can drain is dead schema. Operator SQL + `audit_events` until a platform-role concept lands. |
| 12 | `avatar_uploads_user_created_idx` | S1: ship it for "history and moderation". | **Reject.** | No endpoint, no moderation role, no query. Well-indexed means every index carries a query. |

---

## 1. Migration 000010

Runner constraints (`cluster.go:177-182`): the file is stripped of `BEGIN;`/`COMMIT;` and executed as **one multi-statement `Exec` inside one transaction**. So: no `CONCURRENTLY`, no rewrite that matters, and 000001–000009 can never be edited (checksum-pinned, `cluster.go:149-182`). Nothing below rewrites a table — the only heavy op is one `CREATE INDEX` on `game_accounts` under SHARE lock.

### Pre-flight — run against production first. All four must return 0.

```sql
-- 1. Normalized Konami IDs that would collide under the new unique index.
SELECT count(*) FROM (
  SELECT 1 FROM game_accounts WHERE publisher_player_id IS NOT NULL
  GROUP BY game_id, upper(regexp_replace(publisher_player_id,'[^A-Za-z0-9]+','','g'))
  HAVING count(*) > 1) c;

-- 2. Rows the new game_accounts CHECKs would reject.
SELECT count(*) FROM game_accounts
WHERE (publisher_player_id IS NOT NULL AND char_length(publisher_player_id) NOT BETWEEN 1 AND 64)
   OR char_length(in_game_name) > 80
   OR (platform <> '' AND platform !~ '^[a-z][a-z0-9_]{1,31}$');

-- 3. Rows the new users CHECKs would reject, and any phone that canonicalizes into a collision.
SELECT count(*) FROM users
WHERE char_length(display_name) NOT BETWEEN 1 AND 80
   OR (phone_e164 IS NOT NULL AND regexp_replace(phone_e164,'[^0-9]','','g') !~ '^[1-9][0-9]{7,14}$');

-- 4. Handles that predate the regex.
SELECT count(*) FROM player_profiles WHERE handle !~ '^[A-Za-z0-9][A-Za-z0-9_.]{2,23}$';
```

If (1) is non-zero the migration still succeeds — the dedup pre-pass releases the losers — but you want to know the count and notify those players first.

### `migrations/000010_player_identity.up.sql`

```sql
BEGIN;

-- =====================================================================
-- A. Konami ID: bound it, then give recovery a normalized lookup key.
-- =====================================================================

-- decodeJSON accepts a 64 KiB body (helpers.go:9) and createGameAccount only
-- TrimSpaces (account_handlers.go:199-201). A ~3 KB value today reaches the
-- btree in game_accounts_publisher_id_unique and the INSERT dies with
-- "index row size exceeds maximum" — a live 500, not a theoretical one.
ALTER TABLE game_accounts
    ADD CONSTRAINT game_accounts_publisher_id_length_chk
        CHECK (publisher_player_id IS NULL
               OR char_length(publisher_player_id) BETWEEN 1 AND 64) NOT VALID,
    ADD CONSTRAINT game_accounts_in_game_name_length_chk
        CHECK (char_length(in_game_name) <= 80) NOT VALID;

-- Signup collects a Konami ID but neither a platform nor an in-game name.
-- This schema already spells "not provided yet" as NOT NULL DEFAULT ''
-- (player_profiles.bio 000001:23, refresh_sessions.device_name 000002:26,
-- competitions.description 000001:88). SET DEFAULT is catalog-only: no
-- rewrite, and every Go scanner reading these into a plain string still works.
-- Making them nullable instead would break scanGameAccount, player_queries.go
-- and organizer_competition_handlers.go simultaneously.
-- The platform CHECK is a shape rule, not a membership list; membership lives
-- in games.supported_platforms, which a CHECK cannot reference.
ALTER TABLE game_accounts
    ALTER COLUMN platform     SET DEFAULT '',
    ALTER COLUMN in_game_name SET DEFAULT '',
    ADD CONSTRAINT game_accounts_platform_shape_chk
        CHECK (platform = '' OR platform ~ '^[a-z][a-z0-9_]{1,31}$') NOT VALID;

-- Deterministic pre-pass, the same rank-then-neutralize-then-index shape
-- migration 000004:72-79 used for duplicate OTP challenges. Two live rows that
-- differ only by case or separators collapse to one key and would abort the
-- unique index below. Keep the oldest, release the newer ones, record what was
-- released so support can restore it. Expected affected rows: 0.
INSERT INTO audit_events (actor_user_id, action, subject_type, subject_id, before_state)
SELECT ranked.user_id, 'game_account.publisher_id_released', 'game_account', ranked.id::text,
       jsonb_build_object('gameId', ranked.game_id,
                          'publisherPlayerId', ranked.publisher_player_id,
                          'reason', 'normalized Konami ID collided with an older account')
FROM (
    SELECT id, user_id, game_id, publisher_player_id,
           row_number() OVER (
               PARTITION BY game_id, upper(regexp_replace(publisher_player_id,'[^A-Za-z0-9]+','','g'))
               ORDER BY created_at, id) AS position
    FROM game_accounts WHERE publisher_player_id IS NOT NULL
) ranked
WHERE ranked.position > 1;

UPDATE game_accounts account
SET publisher_player_id = NULL,
    verification_status = 'unverified',
    verified_at         = NULL,
    updated_at          = now()
FROM (
    SELECT id,
           row_number() OVER (
               PARTITION BY game_id, upper(regexp_replace(publisher_player_id,'[^A-Za-z0-9]+','','g'))
               ORDER BY created_at, id) AS position
    FROM game_accounts WHERE publisher_player_id IS NOT NULL
) ranked
WHERE ranked.id = account.id AND ranked.position > 1;

-- THE recovery index. A player recovering an account retypes this ID from
-- memory on a phone keyboard: "1234-5678", "1234 5678" and "12345678" are one
-- account to them and must be one account to us. An expression index rather
-- than a stored generated column, because ADD COLUMN ... GENERATED ... STORED
-- rewrites the table under ACCESS EXCLUSIVE and the streaming replica
-- (compose.yaml:35) stalls behind it; CREATE INDEX takes only SHARE.
-- Both regexp_replace/4 and upper/1 are IMMUTABLE, so this is index-legal.
CREATE UNIQUE INDEX game_accounts_publisher_key_uidx
    ON game_accounts (game_id, upper(regexp_replace(publisher_player_id,'[^A-Za-z0-9]+','','g')))
    WHERE publisher_player_id IS NOT NULL;

-- =====================================================================
-- B. users: name bound, phone canonicalized. No new name column.
-- =====================================================================

-- Floor is 1, not 2, because rows auto-created by the old verifyOTP path via
-- provisionalDisplayName (auth_handlers.go:320-329) can hold a 1-character
-- name for an email like a@b.com. VALIDATE must not abort on them. Signup
-- enforces >= 2 in Go; that path is deleted in this release.
ALTER TABLE users
    ADD CONSTRAINT users_display_name_length_chk
        CHECK (char_length(display_name) BETWEEN 1 AND 80) NOT VALID;

-- Phone is stored only: never verified, never a credential, no SMS provider.
-- The shape rule is therefore country-neutral digits. The strict Safaricom
-- rule stays on payment_intents.phone_e164 (000003:16), the one place a payout
-- depends on it. Both backfills expect 0 rows: no Go path has ever written
-- users.phone_e164 (the only INSERT INTO users is auth_handlers.go:148,
-- email + display_name).
UPDATE users SET phone_e164 = regexp_replace(phone_e164,'[^0-9]','','g'), updated_at = now()
WHERE phone_e164 IS NOT NULL AND phone_e164 <> regexp_replace(phone_e164,'[^0-9]','','g');

UPDATE users SET phone_e164 = '254' || substring(phone_e164 from 2), updated_at = now()
WHERE phone_e164 LIKE '0%' AND country_code = 'KE';

ALTER TABLE users
    ADD CONSTRAINT users_phone_digits_chk
        CHECK (phone_e164 IS NULL OR phone_e164 ~ '^[1-9][0-9]{7,14}$') NOT VALID;

-- =====================================================================
-- C. Handle: the unique index already exists, the shape rule does not.
-- =====================================================================

-- handlePattern (account_handlers.go:15) and username.Pattern
-- (generate.go:25) are two byte-identical Go copies of this regex. Signup
-- mints handles on a third path now, so the rule belongs in the table.
ALTER TABLE player_profiles
    ADD CONSTRAINT player_profiles_handle_shape_chk
        CHECK (handle ~ '^[A-Za-z0-9][A-Za-z0-9_.]{2,23}$') NOT VALID;

-- =====================================================================
-- D. Profile picture. avatar_object_key (000001:24, dead since day one)
--    becomes the current-avatar pointer; this table is the upload protocol
--    copied from evidence_uploads, plus the purge queue evidence never needs.
-- =====================================================================

CREATE TABLE avatar_uploads (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    storage_provider text NOT NULL DEFAULT 's3' CHECK (storage_provider = 's3'),
    object_key text NOT NULL UNIQUE,
    media_type text NOT NULL
        CHECK (media_type IN ('image/jpeg','image/png','image/heic','image/heif')),
    byte_size bigint NOT NULL CHECK (byte_size > 0 AND byte_size <= 10485760),
    checksum_sha256 bytea NOT NULL CHECK (octet_length(checksum_sha256) = 32),
    status text NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending','completed','failed','expired','superseded','purged')),
    upload_expires_at timestamptz NOT NULL,
    completed_at timestamptz,
    provider_etag text,
    purge_after timestamptz,
    purged_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (status <> 'completed'  OR completed_at IS NOT NULL),
    CHECK (status <> 'superseded' OR purge_after  IS NOT NULL),
    CHECK ((status = 'purged') = (purged_at IS NOT NULL))
);

-- evidence_uploads has no per-user cap (only evidence_uploads_owner_created_idx,
-- 000006:25), so a client can mint unbounded presigned PUTs. This caps it at
-- one and gives replace the same consume-then-insert shape as requestOTP
-- (auth_handlers.go:83-87).
CREATE UNIQUE INDEX avatar_uploads_one_pending_per_user_uidx
    ON avatar_uploads (user_id) WHERE status = 'pending';
CREATE INDEX avatar_uploads_pending_expiry_idx
    ON avatar_uploads (upload_expires_at, id) WHERE status = 'pending';
CREATE INDEX avatar_uploads_purge_idx
    ON avatar_uploads (purge_after, id) WHERE status = 'superseded';

-- The current avatar must be a real, completed, checksum-verified object
-- rather than an arbitrary string. avatar_object_key is 100% NULL today, so
-- this validates instantly.
ALTER TABLE player_profiles
    ADD CONSTRAINT player_profiles_avatar_object_fk
        FOREIGN KEY (avatar_object_key) REFERENCES avatar_uploads (object_key) NOT VALID;

-- Required BY the FK, not by any handler query: without it every DELETE from
-- avatar_uploads seq-scans player_profiles for the referential check. The
-- purge worker is specified to UPDATE status='purged' rather than DELETE, but
-- a retention job that ever hard-deletes must not be O(players) per row.
CREATE INDEX player_profiles_avatar_object_idx
    ON player_profiles (avatar_object_key) WHERE avatar_object_key IS NOT NULL;

-- =====================================================================
-- E. Recovery reuses email_otp_challenges unchanged. One forensic column.
-- =====================================================================

-- Recovery IS login: the code stays HMAC-bound to the resolved email
-- (HashOTP, tokens.go:105) and lands in the single active row guaranteed by
-- email_otp_one_active_per_email_uidx (000004:78). No purpose column is needed
-- to separate the flows and no new index. The one question the existing rows
-- cannot answer is "was this code triggered by someone who typed this player's
-- Konami ID", which is exactly what an abuse report asks. A constant default
-- is metadata-only on PG 17 — no rewrite.
ALTER TABLE email_otp_challenges
    ADD COLUMN request_channel text NOT NULL DEFAULT 'email'
        CHECK (request_channel IN ('email','konami_id'));

-- =====================================================================
-- F. Proof-of-email carried from OTP verification to signup.
-- =====================================================================

-- Same shape as email_otp_challenges: superseded rather than accumulated, one
-- live row per address, single-use, swept by expiry.
CREATE TABLE signup_tickets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email text NOT NULL,
    token_hash bytea NOT NULL UNIQUE CHECK (octet_length(token_hash) = 32),
    request_ip text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX signup_tickets_one_active_per_email_uidx
    ON signup_tickets (lower(email)) WHERE consumed_at IS NULL;
CREATE INDEX signup_tickets_expiry_idx
    ON signup_tickets (expires_at, id) WHERE consumed_at IS NULL;

-- =====================================================================
-- G. Validate.
-- =====================================================================
ALTER TABLE game_accounts   VALIDATE CONSTRAINT game_accounts_publisher_id_length_chk;
ALTER TABLE game_accounts   VALIDATE CONSTRAINT game_accounts_in_game_name_length_chk;
ALTER TABLE game_accounts   VALIDATE CONSTRAINT game_accounts_platform_shape_chk;
ALTER TABLE users           VALIDATE CONSTRAINT users_display_name_length_chk;
ALTER TABLE users           VALIDATE CONSTRAINT users_phone_digits_chk;
ALTER TABLE player_profiles VALIDATE CONSTRAINT player_profiles_handle_shape_chk;
ALTER TABLE player_profiles VALIDATE CONSTRAINT player_profiles_avatar_object_fk;

COMMIT;
```

### `migrations/000010_player_identity.down.sql`

```sql
BEGIN;
DROP TABLE IF EXISTS signup_tickets;
ALTER TABLE email_otp_challenges DROP COLUMN IF EXISTS request_channel;
DROP INDEX IF EXISTS player_profiles_avatar_object_idx;
ALTER TABLE player_profiles
    DROP CONSTRAINT IF EXISTS player_profiles_avatar_object_fk,
    DROP CONSTRAINT IF EXISTS player_profiles_handle_shape_chk;
DROP TABLE IF EXISTS avatar_uploads;
ALTER TABLE users
    DROP CONSTRAINT IF EXISTS users_phone_digits_chk,
    DROP CONSTRAINT IF EXISTS users_display_name_length_chk;
DROP INDEX IF EXISTS game_accounts_publisher_key_uidx;
ALTER TABLE game_accounts
    DROP CONSTRAINT IF EXISTS game_accounts_platform_shape_chk,
    DROP CONSTRAINT IF EXISTS game_accounts_in_game_name_length_chk,
    DROP CONSTRAINT IF EXISTS game_accounts_publisher_id_length_chk,
    ALTER COLUMN in_game_name DROP DEFAULT,
    ALTER COLUMN platform     DROP DEFAULT;
COMMIT;
```
The down file does not restore `publisher_player_id` values released by the dedup pre-pass. Those are recoverable only from `audit_events.before_state`.

### Every index, justified or explicitly dropped

**New — each carries a named query:**

| Index | Query it serves |
|---|---|
| `game_accounts_publisher_key_uidx` | §4 recovery resolution, and the 409 on a variant duplicate at signup. Correctness, not performance: the existing `game_accounts_publisher_id_unique` indexes the raw string exactly as typed (`account_handlers.go:199-201` only TrimSpaces), so a retyped separator variant returns zero rows *and* can create a second account. |
| `avatar_uploads.object_key UNIQUE` (implicit) | Prevents key reuse (as `evidence_uploads.object_key UNIQUE`, `000006:7`) and is the target index `player_profiles_avatar_object_fk` requires. |
| `avatar_uploads_one_pending_per_user_uidx` | `UPDATE ... WHERE user_id=$1 AND status='pending'` in §5 create, plus the cap itself. |
| `avatar_uploads_pending_expiry_idx` | Reaper. Models `evidence_uploads_pending_expiry_idx` (`000006:27`). |
| `avatar_uploads_purge_idx` | Purge worker. No analogue in evidence_uploads because evidence is retained; superseded avatars are storage cost. |
| `player_profiles_avatar_object_idx` | The FK referential check on DELETE. Without it, deleting an avatar row seq-scans `player_profiles`. |
| `signup_tickets.token_hash UNIQUE` (implicit) | The only lookup path in §3(a), and the atomic single-use gate. |
| `signup_tickets_one_active_per_email_uidx` | Forces supersede-then-insert; stops ticket accumulation. Mirrors `email_otp_one_active_per_email_uidx` (`000004:78`). |
| `signup_tickets_expiry_idx` | Bounded sweeper scan. Mirrors `email_otp_challenges_expiry_idx` (`000004:107`). |

**Explicitly NOT created — already covered:**

- **Login by email.** `users_email_unique (lower(email)) WHERE email IS NOT NULL` (`000001:17`) serves `auth_handlers.go:145` verbatim. `email_otp_challenges_lookup_idx (lower(email), created_at DESC)` (`000002:17`) plus `email_otp_one_active_per_email_uidx` serve `auth_handlers.go:125-128`; the partial unique already reduces that lookup to one row, so adding `WHERE consumed_at IS NULL` to the lookup index would duplicate an existing index.
- **Konami recovery on `email_otp_challenges`.** Recovery resolves to an email and reuses the identical write path — no new access pattern. `email_otp_challenges_rate_idx (request_ip, created_at DESC)` (`000002:19`) already backs `allowOTPRate`'s DB fallback for both channels.
- **Handle uniqueness / retry loop.** `player_profiles_handle_unique (lower(handle))` (`000001:31`) is exactly the arbiter `ON CONFLICT ((lower(handle)))` infers. What was missing was a CHECK, not an index.
- **Phone lookup.** `users_phone_unique (phone_e164) WHERE phone_e164 IS NOT NULL` (`000001:18`) covers equality. After canonicalization the column is directly comparable to `normalizeKenyanPhone` output — no `lower()`/expression wrapper needed on digits.
- **`loadMe`'s eFootball game-account lookup.** `game_accounts_user_game_idx (user_id, game_id)` (`000001:79`).
- **`avatar_uploads (user_id, created_at DESC, id)`** — S1 proposed it for history/moderation. No endpoint, no role, no query. Dropped.
- **`game_accounts (game_id, upper(publisher_player_id))`** — S3's non-unique variant, superseded by the unique one above.

**Kept, not dropped:** `game_accounts_publisher_id_unique` (`000001:80`) is now logically redundant (identical raw values imply identical keys). Expand-only discipline keeps it here; drop it in a later contract migration, after `account_handlers.go:209` stops string-matching its name.

**Left alone:** `game_accounts_in_game_name_trgm_idx` (`000007:189`). Signup rows carry `in_game_name = ''` and a GIN trgm index derives zero keys from an empty string, so no posting entries and no bloat. Rebuilding it partial would cost an ACCESS EXCLUSIVE rebuild for nothing.

---

## 2. HTTP surface

New/changed routes in the `New()` table (`server.go:83-108`). `sensitivePath` (`server.go:264`) already covers `/v1/auth/` and `/v1/me`, so `Cache-Control: no-store, private` is inherited with no edit. `/v1/players/{id}/avatar` deliberately falls outside it.

```go
mux.HandleFunc("POST /v1/auth/signup", s.signup)
mux.Handle("POST /v1/me/avatar/uploads",              s.requireAuth(http.HandlerFunc(s.createAvatarUpload)))
mux.Handle("POST /v1/me/avatar/uploads/{id}/complete", s.requireAuth(http.HandlerFunc(s.completeAvatarUpload)))
mux.Handle("DELETE /v1/me/avatar",                     s.requireAuth(http.HandlerFunc(s.deleteAvatar)))
mux.HandleFunc("GET /v1/players/{id}/avatar", s.getPlayerAvatar)
```

### 2.1 `POST /v1/auth/otp/request` — CHANGED (same route, same 202 body)

```jsonc
{ "email": "brian@example.com" }        // or
{ "konamiId": "1234567890" }
```
Exactly one. Both or neither → `400 invalid_identifier`, `"Provide either your email address or your Konami ID."`

Response, byte-identical for a hit, a miss, and a post-resolution rate denial:
```json
{ "status": "accepted", "expiresInSeconds": 600 }
```
`429 rate_limited` with `Retry-After` only from the pre-resolution IP/Konami limiters, which reveal nothing.

### 2.2 `POST /v1/auth/otp/verify` — CHANGED (two possible 200 bodies)

```jsonc
{ "email": "brian@example.com", "code": "483920", "deviceName": "Pixel 7" }
{ "konamiId": "1234567890",     "code": "483920", "deviceName": "Pixel 7" }
```

Known player → **unchanged** `AuthSession`:
```json
{ "accessToken": "...", "refreshToken": "...", "tokenType": "Bearer",
  "expiresInSeconds": 900, "player": { "...loadMe..." } }
```

Unknown email → **new** 200 body:
```json
{ "status": "registration_required",
  "registrationToken": "8f3c…",
  "email": "brian@example.com",
  "expiresInSeconds": 900 }
```
Clients discriminate on the presence of `status`; `AuthSession` has no `status` field. An unknown **Konami ID** never reaches this branch — it returns the same `401 invalid_code` as a wrong code, so verify is not an enumerator either.

### 2.3 `POST /v1/auth/signup` — NEW, unauthenticated, gated by the ticket

```json
{ "registrationToken": "8f3c…",
  "name": "Brian Otieno",
  "phoneNumber": "0712345678",
  "konamiId": "1234567890",
  "platform": "android",
  "countryCode": "KE",
  "acceptTerms": true,
  "acceptPrivacy": true,
  "deviceName": "Pixel 7" }
```
Optional: `platform` (`""` when absent), `countryCode` (default `"KE"`), `deviceName`. **No `email` field** — the email comes from the ticket, so nobody can sign up under an address they did not prove. **No `handle` field** — the founder's model is derived-or-generated; renaming is `PUT /v1/me/profile`, which already exists. **No `discoverable` field** — stays at the column default `false` (`000001:25`), which `publish_leaderboard_snapshot` filters on (`000007:152`), so auto-provisioned profiles are private until the player opts in.

`201 Created` returns the same `AuthSession` shape via `s.writeSessionResponse`, with `player.onboarding.complete` already true.

Errors: `400 invalid_registration_token | invalid_name | invalid_phone | invalid_konami_id | invalid_platform | invalid_country | terms_required | privacy_required`; `401 registration_token_expired`; `409 account_exists | phone_taken | konami_id_taken`; `503 database_unavailable | handle_unavailable`.

### 2.4 Avatar (§5 for mechanics)

```
POST   /v1/me/avatar/uploads                → 201 { "data": { id, uploadUrl, requiredHeaders, expiresAt } }
POST   /v1/me/avatar/uploads/{id}/complete  → 200 { "data": { id, status, mediaType, byteSize, completedAt, avatarUrl } }
DELETE /v1/me/avatar                        → 204
GET    /v1/players/{id}/avatar              → 302 Location: <presigned GET>   (404 if none)
```

### 2.5 `GET /v1/me` (`loadMe`) — CHANGED

The Konami ID is the primary human-facing identifier, so `/v1/me` must return it — and must never return it without its verification status, so no client can render it as trusted.

```sql
SELECT u.id,u.email,u.phone_e164,u.display_name,u.country_code,
       to_char(u.birth_date,'YYYY-MM-DD'),u.status,u.terms_accepted_at,u.privacy_accepted_at,
       u.created_at,u.updated_at,
       p.handle,p.bio,p.avatar_object_key,p.discoverable,p.analytics_consent_at,p.scouting_consent_at,
       ga.publisher_player_id, ga.verification_status,
       EXISTS(SELECT 1 FROM game_accounts x WHERE x.user_id=u.id)
FROM users u
LEFT JOIN player_profiles p ON p.user_id=u.id
LEFT JOIN LATERAL (
    SELECT publisher_player_id, verification_status
    FROM game_accounts
    WHERE user_id=u.id AND game_id='efootball-mobile'
    ORDER BY created_at, id LIMIT 1
) ga ON true
WHERE u.id=$1
```
**`LEFT JOIN LATERAL … LIMIT 1`, not a plain LEFT JOIN.** `game_accounts_user_game_idx` is deliberately non-unique, so a player may hold two eFootball accounts; a plain join makes `QueryRow` pick one arbitrarily and silently. Neither scout caught this. The lateral is served by the same index.

New JSON keys: `phoneNumber` (unmasked — it is the owner's own record; `maskPhone` is for organizer-facing views), `konamiId`, `konamiIdVerified` (derived, `verification_status == "verified"`, therefore always `false` today), `avatarUrl` (`"/v1/players/<id>/avatar"` when `avatar_object_key IS NOT NULL`, else `null`). `onboarding` keeps its four booleans.

---

## 3. Signup transaction, exact statement order

**Pre-transaction, all pure, no DB, no open tx:**

1. `decodeJSON` into `signupRequest`. `DisallowUnknownFields` is on (`helpers.go:10`), so every field above must exist on the struct.
2. `name := strings.TrimSpace(input.Name)`; require `2 <= len(name) <= 80` → `400 invalid_name`. Matches `patchMe` (`account_handlers.go:87`).
3. `phone, ok := normalizeKenyanPhone(input.PhoneNumber)` — **reused verbatim** from `payment_handlers.go:723` → `400 invalid_phone`.
4. `konamiID, ok := normalizeKonamiID(input.KonamiID)` → `400 invalid_konami_id`. Stores the trimmed original; the normalized key is derived by the index.
5. `platform`: `""` when absent, else lowercase and require `android|ios` → `400 invalid_platform`.
6. `countryCode` upper-cased, `countryPattern` (`account_handlers.go:16`) → `400 invalid_country`.
7. `AcceptTerms && AcceptPrivacy` both true → `400 terms_required` / `privacy_required` (same codes `patchMe` uses).
8. **Draw handle candidates before opening the tx**, so no entropy read happens with a transaction held:

```go
const handleAttempts = 5

func handleCandidates(name string, entropy io.Reader) ([]string, error) {
    candidates := make([]string, 0, handleAttempts)
    derived, ok, err := username.FromName(name, entropy) // "Brian Otieno" -> "brian_4821"
    if err != nil {
        return nil, err                                  // broken entropy source
    }
    if ok {
        candidates = append(candidates, derived)
    }
    generated, err := username.Candidates(entropy, handleAttempts-len(candidates)) // Swift_Falcon_4821…
    if err != nil {
        return nil, err
    }
    return append(candidates, generated...), nil
}
```
`entropy` is `nil` in production, which makes both default to `crypto/rand.Reader` (`generate.go:70-72, 129-131`); pass a fixed `io.Reader` only in tests. `FromName` already keeps only the first token (`generate.go:132-136`), which is why **no `first_name` column exists** — it would be a second source of truth that `patchMe` desynchronizes on the first rename. `Candidates` is used rather than N `Generate` calls because the package offers batching for exactly this loop (`generate.go:93-94`).

**One transaction**, `s.db.Writer.Begin` + `defer tx.Rollback`, in this order:

**(a) Consume the ticket first.** Nothing else may run for a replayed or expired token:
```sql
UPDATE signup_tickets SET consumed_at=now()
WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now()
RETURNING email
```
`$1 = gamicsauth.HashRefreshToken(input.RegistrationToken)`. `pgx.ErrNoRows` → `401 registration_token_expired`. `token_hash UNIQUE` + `consumed_at IS NULL` makes this atomically single-use: two concurrent signups with one token, one wins. **The email returned here is the email used below — the client never supplies it.**

**(b) Advisory lock on that email**, the identical idiom from `requestOTP` (`auth_handlers.go:79`), so a concurrent flow for the same address serializes rather than racing the unique index:
```sql
SELECT pg_advisory_xact_lock(hashtextextended(lower($1),0))
```

**(c) `users`** — parent row, must precede both children (FKs):
```sql
INSERT INTO users (email, phone_e164, display_name, country_code, status,
                   terms_accepted_at, privacy_accepted_at)
VALUES ($1,$2,$3,$4,'active',now(),now())
RETURNING id
```
`display_name` is the full name; the handle is the public identity. On error use `isUniqueViolation(err)` (`organizer_handlers.go:556`) and switch on `pgErr.ConstraintName`: `users_email_unique` → `409 account_exists`, `users_phone_unique` → `409 phone_taken`. Do **not** use `strings.Contains(err.Error(), …)`.

**(d) `player_profiles` — the collision-retry loop.** No savepoints; the tx never enters a failed state:
```go
func claimHandle(ctx context.Context, tx pgx.Tx, userID string, candidates []string) (string, error) {
    for _, candidate := range candidates {
        var claimed string
        err := tx.QueryRow(ctx, `INSERT INTO player_profiles (user_id, handle)
            VALUES ($1,$2) ON CONFLICT ((lower(handle))) DO NOTHING RETURNING handle`,
            userID, candidate).Scan(&claimed)
        if errors.Is(err, pgx.ErrNoRows) {
            continue // lower(handle) already claimed
        }
        if err != nil {
            return "", err
        }
        return claimed, nil
    }
    return "", errHandleExhausted
}
```
Things an implementer must not get wrong:
- **`ON CONFLICT`, not a caught 23505.** Inside this transaction a raised 23505 poisons the tx and would force a SAVEPOINT per attempt. `ErrNoRows` from `RETURNING` is the retry signal.
- **Infer the handle index specifically** (`((lower(handle)))` — the inner parens are the documented form for an index *expression*), not bare `DO NOTHING`. Bare `DO NOTHING` would also swallow a `user_id` PK conflict and burn all five candidates before returning a misleading 503. The PK cannot conflict — step (c) just created that row — so inference is safe and precise.
- A concurrent inserter of the same handle blocks until it commits, then this returns zero rows. Correct, not a lost update.
- **Fail closed.** `errHandleExhausted` → `503 handle_unavailable`, roll the whole signup back. Do not degrade to a profile-less user: `COALESCE(profile.handle, account.in_game_name, player.display_name)` (`match_handlers.go:198,211`) does **not** skip `''`, so a signup row with `in_game_name=''` and no profile renders a blank name in the match room. With ~40M shapes (`generate.go:66-68`) this is effectively unreachable; the failure mode is a retryable 503, never a partial account.

**(e) `game_accounts`** — do **not** touch `verification_status`; let it default:
```sql
INSERT INTO game_accounts (user_id, game_id, platform, in_game_name, publisher_player_id)
VALUES ($1,'efootball-mobile',$2,'',$3)
```
`platform` is `''` when signup did not ask; `in_game_name` takes `''`. `verification_status` defaults to `'unverified'`, because the ID is unverifiable, forever. On `isUniqueViolation`, `ConstraintName` of **either** `game_accounts_publisher_key_uidx` **or** `game_accounts_publisher_id_unique` → `409 konami_id_taken`, `"That Konami ID is already connected to another Gamics account."`

**(f) `refresh_sessions`** — byte-identical to `verifyOTP`'s insert (`auth_handlers.go:165-167`), so no session can exist for a half-built account:
```go
refreshToken, refreshHash, _ := gamicsauth.NewRefreshToken()
sessionID := gamicsauth.RandomID()
```

**(g) `outbox_events`** in the same tx, house pattern: `('player', userID, 'player.registered', payload)`. See the §6 caveat: there is no consumer yet.

**(h) `tx.Commit`, then** `s.writeSessionResponse(w, r, userID, sessionID, refreshToken)` — after commit, exactly like `verifyOTP`, so `loadMe` reads committed rows and Redis is warmed only for a session that exists.

One line each on ordering: ticket first so replays cost nothing; advisory lock before any unique-index contention; parent before children; handle loop innermost because it is the only step that legitimately retries; session last so credentials can never be issued for an account whose later inserts failed.

---

## 4. Konami-ID recovery, end to end

### 4.0 The decision everything follows from

`HashOTP(secret, email, code)` binds the code to the **email** (`tokens.go:105-111`). So the Konami ID is never a challenge key — it is a lookup key that resolves to an email, resolved identically at request and verify. `email_otp_challenges` keeps its exact current shape. Free security property: if the Konami→email binding moves between request and verify, the HMAC stops matching and verification fails closed.

### 4.1 Resolution query (identical in both handlers)

```sql
SELECT player.id, player.email
FROM game_accounts account
JOIN users player ON player.id = account.user_id
WHERE account.game_id = 'efootball-mobile'
  AND account.publisher_player_id IS NOT NULL
  AND upper(regexp_replace(account.publisher_player_id,'[^A-Za-z0-9]+','','g')) = $1
  AND player.email IS NOT NULL
  AND player.status = 'active'
```
`$1` is the Go-normalized ID (same rule, uppercase, non-alphanumerics stripped). Index Scan on `game_accounts_publisher_key_uidx`, then `users_pkey`; at most one row by construction. **The explicit `publisher_player_id IS NOT NULL` is load-bearing** — without it the planner cannot prove the index's partial predicate and falls back to a seq scan, which is a silent performance regression, not a wrong answer. Assert the plan in a test.

Run on `s.db.Writer`, not `s.db.Reader`: an account created seconds earlier must be recoverable, and every other auth query in the file already uses the writer. `pgx.ErrNoRows` → miss, never an error to the caller.

### 4.2 `requestOTP`, exact new ordering

Replaces `auth_handlers.go:50-66`. Everything from `gamicsauth.GenerateOTP()` (line 67) onward is untouched.

1. `decodeJSON` into the widened `otpRequest`. Both identifiers or neither → `400 invalid_identifier`.
2. `started := time.Now()`; `ip := s.clientIP(r)`.
3. **`allowOTPRate(ctx,"ip",ip,cfg.OTPIPLimit)` first, before any DB work.** This is the only limiter that applies to an unknown Konami ID, so it must be charged before the early return — and charging it before resolution stops an attacker driving resolution queries past their IP budget. Denial → `429` + `Retry-After`; safe to return immediately, it depends only on pre-resolution inputs.
4. Email channel: `normalizeEmail` → `400 invalid_email` on failure. Then the existing path unchanged (email limit → 429, generate, hash, tx, advisory lock, consume, insert with `request_channel='email'`, commit, `SendOTP`, 202). **No behaviour change for email login.**
5. Konami channel: `normalizeKonamiID` → on an unusable shape, still fall through to the uniform 202 with the floor; do not 400 (shape is a weak oracle). Then `allowKonamiProbe(ctx, key)` (§4.4) — denial → `429`, pre-resolution, non-oracular.
6. Run §4.1. On **miss**: hold to the floor, write the uniform 202, return. Log at Info with the IP and a SHA-256 of the normalized ID — **never the raw ID**.
7. On **hit**: `allowOTPRate(ctx,"email",resolvedEmail,cfg.OTPEmailLimit)`. This is the key that stops someone who knows a victim's Konami ID from mailing them unlimited codes — the Konami door and the email door share one 5-per-15-minutes budget. **A denial here must NOT change the response:** suppress the send, hold the floor, return the same 202. Surfacing a 429 here would confirm the ID exists.
8. Otherwise the existing path verbatim, with `request_channel='konami_id'` on the insert and `HashOTP(secret, resolvedEmail, code)`.
9. Hold to the floor, **cancellably**, then write the uniform 202:
```go
select {
case <-time.After(time.Until(started.Add(s.config.KonamiRecoveryFloor))):
case <-r.Context().Done():
    return
}
```
A bare `time.Sleep` ignores `RequestTimeout` (the middleware wraps every request, `server.go:233`) and pins a goroutine per probe — that is itself a cheap DoS.

**Do not add a `"konami"` kind to `allowOTPRate`.** Its Postgres fallback maps any unrecognised kind to the `request_ip` column (`auth_handlers.go:339-343`), so a Konami ID compared against `request_ip` always counts 0 and always allows whenever Redis is down. That is why resolution happens before the email charge, and why the Konami probe counter is its own helper.

### 4.3 `verifyOTP`, exact changes

Lines 110-114 become:
```go
identifier, err := s.readOTPIdentifier(r.Context(), s.db.Writer, input.Email, input.KonamiID)
if err != nil || !identifier.Known || len(input.Code) != 6 || len(input.DeviceName) > 120 {
    writeError(w, http.StatusUnauthorized, "invalid_code", "The code is invalid or expired.")
    return
}
email := identifier.Email
```
Lines 115-143 (challenge lock, expiry, attempts, `HashOTP`, `VerifyOTP`, consume) are **unchanged** — already email-keyed; the resolved email slots straight in.

Lines 144-154 (the `users` lookup and auto-create) become: on `pgx.ErrNoRows`, mint a ticket instead of an account —
```go
token, tokenHash, _ := gamicsauth.NewRefreshToken()   // 32 random bytes + sha256, reused verbatim
// supersede-then-insert, mirroring requestOTP's challenge handling, so
// signup_tickets_one_active_per_email_uidx is always satisfied
UPDATE signup_tickets SET consumed_at=now() WHERE lower(email)=lower($1) AND consumed_at IS NULL
INSERT INTO signup_tickets(email,token_hash,request_ip,user_agent,expires_at)
VALUES ($1,$2,$3,$4,now()+$5::interval)
```
then commit and return the `registration_required` body. No extra advisory lock: two concurrent verifies for the same email cannot both get past the single `FOR UPDATE`'d, consumed-once challenge row.

Lines 155-172 (status check, refresh session, commit, `writeSessionResponse`) unchanged.

**Delete `provisionalDisplayName` (`auth_handlers.go:320-329`) and its tests.** Leaving it is leaving a second name source.

### 4.4 Rate limits

All keys go through `s.securityKey(...)` → `gamics:<env>:v1:security:…`; digest is `sha256(strings.ToLower(value))` hex, identical to `allowOTPRate`.

| Key | Limit | Default | Window | Redis-down behaviour |
|---|---|---|---|---|
| `rate:otp:ip:<d>` *(existing)* | `OTP_IP_LIMIT` | 20 | `OTP_REQUEST_WINDOW` 15m | DB fallback on `email_otp_challenges.request_ip`, fails closed on DB error |
| `rate:otp:email:<d>` *(existing, **shared**)* | `OTP_EMAIL_LIMIT` | 5 | 15m | DB fallback on `lower(email)` |
| `rate:konami:probe:<d>` *(new)* | `KONAMI_PROBE_LIMIT` | 5 | 15m | **Redis-only, fails OPEN** |

`allowKonamiProbe` calls `redisFixedWindow` directly and returns `true` when Redis is unavailable. Failing closed would break `POST /v1/me/game-accounts` during a Redis outage; the per-IP limiter (which does have a DB fallback) is the backstop. Log the degradation.

**Charge the same `rate:konami:probe` counter in `createGameAccount` and in `patchGameAccount`'s `PublisherPlayerID` branch, before the insert.** `409 game_account_exists` (`account_handlers.go:209-212`) is a free, unlimited registration oracle live in production today. Keep the 409 — the player needs to know why their entry was refused — but make probing through signup burn the identical budget as probing through recovery. **Hardening recovery while that door stays open moves the attacker one door over rather than shutting the house.**

### 4.5 What may never appear in a recovery response

Never, in any form: email, **masked** email, email domain, display name, handle, avatar, country, `verification_status`, account status, whether the ID exists, whether it is bound, or who holds it. A mask still confirms registration and hands an attacker the provider plus enough shape for a credible targeted phish against a player whose Konami ID is public in every WhatsApp bracket.

Must not vary between hit and miss: HTTP status (always 202), the JSON key set, `expiresInSeconds`, the presence of `Retry-After`, and observable latency (the floor). `writeJSON` marshals `map[string]any` through `encoding/json`, which sorts keys, so ordering is stable for free.

A **suspended** owner resolves as a miss, deliberately — no `403 account_unavailable` on this channel. `verifyOTP` can afford its 403 (`auth_handlers.go:155-158`) because email login is not an oracle at all.

The identifier is in the **body, never a query string**: `safeLogPath` (`server.go:271`) redacts only the M-Pesa callback path, so a Konami ID in a URL lands in access logs verbatim.

Client copy lives in the app: *"If that Konami ID is registered, we've emailed a code to the address on file."*

### 4.6 Squatting — the containing rule

`game_accounts_publisher_key_uidx` is first-write-wins and the ID is unverifiable, so a squatter can hold someone else's Konami ID. The rule that bounds it: **an unverified Konami ID is a lookup key, never an authenticator.** Recovery mails the code to the address on file, so a squatter holding your ID still cannot read your mail and cannot take your account. Residual harm is (a) the real owner cannot bind their ID and (b) the squatter's `in_game_name` shows in public search. Neither is compromise. **Never add `publisher_player_id` to a public projection** — it is correctly absent from `player_queries.go:311-312`.

The manual unbind is operator SQL plus one `audit_events` row (`action='game_account.publisher_id_released'`, matching the migration's own pre-pass). **The one hard rule for the runbook: a granted claim moves the `publisher_player_id` binding and nothing else — never sessions, never the email on file, never refresh tokens.** Without that rule the support channel becomes a social-engineering takeover path.

---

## 5. Avatar upload

Mirrors `createEvidenceUpload` / `completeEvidenceUpload` (`result_handlers.go:137-268`) statement for statement. Reuse `supportedEvidenceMediaTypes`, `storage.HexToBase64SHA256`, `storage.EqualChecksum`, `storageChecksumBase64`, `randomUUID`, `uuidPattern`, `truncate`. Every route 503s `avatar_storage_unavailable` when `s.evidenceStore == nil`, exactly as `result_handlers.go:141` does — `STORAGE_MODE` defaults to `"disabled"` (`config.go:244`), leaving the store nil.

**`POST /v1/me/avatar/uploads`** — body `{ "mediaType", "byteSize", "sha256" }`.
Validate media type against the map, `1 <= byteSize <= cfg.AvatarMaxBytes`, 64-hex checksum. Then:
```sql
UPDATE avatar_uploads SET status='expired', updated_at=now()
WHERE user_id=$1 AND status='pending';
INSERT INTO avatar_uploads (id,user_id,object_key,media_type,byte_size,checksum_sha256,upload_expires_at)
VALUES ($1,$2,$3,$4,$5,$6,$7);
```
Consume-then-insert is the `requestOTP` idiom (`auth_handlers.go:83-87`), and `avatar_uploads_one_pending_per_user_uidx` enforces it. `objectKey = path.Join("avatars", time.Now().UTC().Format("2006/01"), avatarID+extension)`. Presign with `s.evidenceStore.PresignPut(ctx, objectKey, mediaType, checksumBase64, s.config.StoragePresignTTL)`.

**`POST /v1/me/avatar/uploads/{id}/complete`** — `Stat()`, then reject unless `info.Size == record.ByteSize && storage.EqualChecksum(info.ChecksumSHA256, storageChecksumBase64(record.Checksum))`, same failure statuses as evidence (`409 avatar_upload_incomplete`, `410 avatar_upload_expired`, `422 avatar_verification_failed`). Then, in one tx, **in this order**:
```sql
UPDATE avatar_uploads SET status='superseded', purge_after=now()+interval '7 days', updated_at=now()
WHERE object_key = (SELECT avatar_object_key FROM player_profiles WHERE user_id=$1)
  AND status='completed';

UPDATE avatar_uploads SET status='completed', completed_at=now(), provider_etag=$2, updated_at=now()
WHERE id=$1 AND user_id=$3 AND status='pending';

UPDATE player_profiles SET avatar_object_key=$4, updated_at=now() WHERE user_id=$3;
```
Supersede the old row **before** repointing, so the pointer is never briefly dangling under the FK.

**`DELETE /v1/me/avatar`** — one tx: `UPDATE player_profiles SET avatar_object_key=NULL` first, **then** mark the old row `superseded` with `purge_after`. `player_profiles_avatar_object_fk` means a row that is still someone's current avatar cannot be deleted; clearing the pointer first is the FK-safe order.

**`GET /v1/players/{id}/avatar`** — unauthenticated. `SELECT p.avatar_object_key FROM player_profiles p JOIN users u ON u.id=p.user_id WHERE p.user_id=$1 AND u.status='active' AND p.avatar_object_key IS NOT NULL` (served by the `player_profiles` PK). 404 otherwise. Then `PresignGet(key, cfg.StoragePresignTTL)` → `302` with `Cache-Control: private, max-age=<presignTTL - 60s>`. **This is why the stable path exists:** `PresignGet` clamps TTL to 1–15 minutes (`s3.go:132`), and a `ResponseCache` (`server.go:74`) that cached a presigned URL directly in a list response would serve dead links. A stable path is cacheable; the signature is minted at fetch.

**Populate `avatarUrl` everywhere it is already promised** — `player_handlers.go:29`, `player_queries.go:51`, `player_history_queries.go:14`, `match_handlers.go:36`, and `openapi/player-discovery.paths.yaml:105,162` — as `"/v1/players/<id>/avatar"` when `avatar_object_key IS NOT NULL`, else `null`. No join, no extra column, no presign per row.

`player_profiles.updated_at` is the cache-busting timestamp; **no `avatar_updated_at` column is warranted.**

**Purge worker rule:** set `status='purged'` and `purged_at=now()` after deleting the object from S3. Never `DELETE` a row. If a retention job is ever added that does delete, `player_profiles_avatar_object_idx` is what keeps the FK check off a seq scan.

---

## 6. What NOT to build, and why

| Not built | Why |
|---|---|
| `account_recovery_challenges` + decoy rows + `/v1/auth/recovery/*` (S3) | Decoys exist only so the *Redis-down* DB fallback counts misses, while `createGameAccount`'s `409` is a free unlimited oracle today. Fix the free one (§4.4). A second challenge table also duplicates the verify path and gives an attacker one unauthenticated INSERT per probe on the primary. |
| `game_account_claims` + claim endpoints (S3) | No platform-staff role exists — every permission in `internal/organizer` is `{orgId}`-scoped (`organizer_access.go:34-73`). A queue nobody can drain is dead schema. Operator SQL + `audit_events` until a platform role lands. |
| Widening `evidence_uploads.bound_kind` (S2, S3) | Requires DROPping an auto-named inline CHECK (`000006:15`) on a live DB — both scouts flagged the name is only Postgres's guess. Avoided entirely by giving avatars their own table. |
| `publisher_player_key` stored generated column (S1) | Rewrites `game_accounts` under ACCESS EXCLUSIVE inside a single-transaction runner; the streaming replica stalls behind it. A unique expression index gives the same guarantee under SHARE. |
| `users.first_name` (S1 also rejects) | `username.FromName` already takes the first token. A second column desynchronizes on the first `patchMe` rename. |
| `publisher_player_id` promoted to `users` | It is per-game; `users` is game-agnostic. It would also lose `verification_status`, i.e. become a column that silently reads as trustworthy. |
| A `"konami"` kind in `allowOTPRate` | Its Postgres fallback silently maps unknown kinds to `request_ip`, counting 0 and allowing everything whenever Redis is down. |
| A second mail template / `mail.Sender` change | `SendOTP`'s subject *"Your Gamics sign-in code"* is honest for a passwordless flow where recovery **is** sign-in. |
| `avatar_updated_at`, `avatar_uploads (user_id, created_at DESC)`, any index on `player_profiles.avatar_object_key` beyond the FK-check one | No query. |
| A reserved-handle table | Generated handles cannot produce `admin`/`gamics`/`support` (the `Adjective_Noun_NNNN` shape), and `putProfile` already exists as the surface to gate later. Worth doing before public launch, not in 000010. |
| Dropping `game_accounts_publisher_id_unique` | Expand-only. Later contract migration, after `account_handlers.go:209` stops string-matching its name. |
| `UNIQUE (user_id, game_id)` on `game_accounts` | `game_accounts_user_game_idx` is deliberately non-unique, so one human can hold two Konami IDs and enter separate competitions under each. `entry_members_one_player_per_competition_uidx` (`000004:23`) only stops double-entry *within one* competition, so this is an open rating-farming path — but it can break live rows and it is a product decision. Founder call, own migration. |

### Small fixes to land in the same release (not schema)

- **`match_handlers.go:198,211`**: change `COALESCE(profile.handle, account.in_game_name, player.display_name)` to `COALESCE(NULLIF(profile.handle,''), NULLIF(account.in_game_name,''), player.display_name)`. COALESCE does not skip `''`, and `in_game_name` now has an `''` default. Two-word change, removes a whole class of blank-name bug. `competition_handlers.go:330` and `payment_handlers.go:142` need the same `NULLIF` on `in_game_name`.
- **`account_handlers.go:162, 209`**: replace `strings.Contains(err.Error(), …)` with `isUniqueViolation(err)` + `pgErr.ConstraintName`, and make :209 match **both** `game_accounts_publisher_id_unique` and `game_accounts_publisher_key_uidx`. Without this, a case/separator-variant duplicate returns a generic `400 invalid_game_account` instead of `409`.
- **Config additions** (`config.go` patterns, validated in the block at :309): `SIGNUP_TICKET_TTL` 15m (reject outside 1m–1h), `KONAMI_RECOVERY_FLOOR` 700ms (reject `>= RequestTimeout` or `>= WriteTimeout`), `KONAMI_PROBE_LIMIT` 5 (reject `< 1`), `AVATAR_MAX_BYTES` 5 MiB (reject outside 1 B–10 MiB, matching the table CHECK).
- **OpenAPI**: `/v1/auth/otp/request` and `/v1/auth/otp/verify` (`openapi.yaml:59-128`), `AuthSession`/`Player` (`:1814-1880`), plus a new `openapi/auth.paths.yaml` following the `organizer.paths.yaml` convention already in the tree.

---

## 7. Founder decisions — do not guess these

1. **Konami ID shape.** Spec leaves the DB at length ≤ 64 and puts the pattern in Go. What is the real range of eFootball IDs? S2 assumed `^[0-9]{6,20}$`; too narrow rejects real players at signup. Default until told otherwise: `^[A-Za-z0-9][A-Za-z0-9 _.-]{5,63}$` on input, normalized to alphanumeric-uppercase.
2. **`users_phone_unique` — one account per phone number.** Applied since `000001:18`. Right for M-Pesa payout fraud, wrong for shared family and cyber-café handsets: the second player to sign up is simply blocked with `409 phone_taken`. Relaxing it is a contract migration and payouts then need a different anti-fraud handle. Do not relax it silently.
3. **Does signup ask for platform (android/ios)?** Spec makes it optional and defaults `''`, so the game account is half-configured until the player patches it. One extra tap at signup avoids that.
4. **Is `GET /v1/players/{id}/avatar` public?** Spec says yes for any active player with an avatar, on the reasoning that `discoverable` gates search/indexing, not media the player chose to publish, and that gating it would blank out public brackets. Alternative: require auth, or require `discoverable = true`.
5. **`KONAMI_RECOVERY_FLOOR` is partial mitigation.** SMTP stays on the request path (`auth_handlers.go:92`), so a hit slower than the floor still leaks. Moving `SendOTP` to a detached goroutine would fix it but there is **no outbox consumer** (six writers, zero readers; `cmd/` holds only `api`), so a dropped mail has no retry. Decide: accept the residual timing leak, or fund a worker.
6. **Retention.** `email_otp_challenges`, `signup_tickets` and `avatar_uploads` all carry IPs/user agents and all have expiry indexes with **no sweeper process in the repo** — `email_otp_challenges_expiry_idx` (`000004:107`) has had none since it shipped. Cron/runbook item, not code.
7. **Breaking client change.** `verifyOTP` stops returning an `AuthSession` for unknown emails. The mobile app and `app/page.tsx` must branch on `status` before this ships, or first-time sign-in silently breaks. This is a coordinated release, not a drop-in.

---

## 8. Decisions taken

Recorded so §7 is not re-litigated during implementation.

| # | Question | Decision |
|---|---|---|
| 2 | `users_phone_unique`, one account per phone | **Keep the constraint.** It is the anti-fraud handle for M-Pesa payouts and is already applied since 000001. Accepted cost: a genuine second player on a shared handset or cyber-cafe phone is refused with `409 phone_taken` and needs manual support. Surface that error with a message a support agent can act on, not a generic conflict. |
| 7 | Breaking `verifyOTP` change | **Proceed now.** The player client is being rebuilt in Flutter, so the coordinated release costs nothing today and would be expensive once a released app depends on the old contract. |
| - | `409 game_account_exists` enumeration oracle | **Fix as part of this work.** It is a live hole today, independent of recovery: any authenticated caller can probe any Konami ID without limit (`account_handlers.go:213`). |

### Standing note: five features are blocked on one missing binary

`cmd/` contains only `api`. There is no worker process, and these all wait on it:

- moving `SendOTP` off the request path, which is what would close the §7.5 timing leak;
- sweeping `email_otp_challenges`, `signup_tickets` and `avatar_uploads` expiry (§7.6);
- draining `outbox_events`, which has writers and no readers at all;
- M-Pesa reconciliation, today reachable only if the player reopens the payment screen;
- bracket deadline forfeits, item 4 of the delivery order in algorithm-decision.md.

Treat the worker as shared infrastructure rather than as any one feature's task.
