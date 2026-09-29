# Mobile API handoff

The authoritative contract is `services/api/openapi/openapi.yaml` (OpenAPI 0.7.0).
Generate types from that file and validate runtime responses. Do not invent routes or
infer authorization from UI labels. All authenticated requests use the access token;
refresh tokens are rotated by the API and stored only in secure device storage.

## Base configuration

```text
EXPO_PUBLIC_API_URL=http://10.0.2.2:8080  # Android emulator
EXPO_PUBLIC_USE_FIXTURES=false
```

Use the computer's LAN address instead of `10.0.2.2` on a physical phone. Production
must use HTTPS. API-relative media references, including `avatarUrl`, are resolved
against `EXPO_PUBLIC_API_URL`. The public avatar route responds with a short-lived
`307` redirect; the client may let its image component follow that redirect.

## Player journey

### 1. Email sign-in and durable sessions

- `POST /v1/auth/otp/request` with an email address.
- `POST /v1/auth/otp/verify` creates or signs in the player and returns access and
  refresh tokens.
- `POST /v1/auth/refresh` rotates the refresh token. Serialize refresh attempts and
  replace the stored token atomically.
- `POST /v1/auth/logout` revokes the current session and every push installation
  registered with it. Call `DELETE /v1/me/push-tokens/{id}` for this device first, then
  log out.
- `GET /v1/me/sessions` and `DELETE /v1/me/sessions/{id}` manage other devices.
  Revoking a session also revokes its push installations, so a lost phone signed out
  from another device stops receiving the player's pushes.

Phone OTP is intentionally not part of onboarding. A phone number is requested only
when a player chooses M-Pesa.

### 2. Onboarding, legal consent, profile, and avatar

- `GET /v1/me` returns the player and `onboarding`: `personalDetails`, `displayName`,
  `profile`, `gameAccount` and `complete`. Show the next unfinished step. Until
  `complete` is true, `GET /v1/competitions/{id}/eligibility` reports a blocking issue
  (`profile_incomplete` while the display name is pending) and free registration and
  paid entry are refused with `409` for the same reason: `error` is
  `profile_incomplete` on free registration, and paid entry answers
  `competition_ineligible` with `issue.code` `profile_incomplete`. Route both to the
  unfinished step. `409 onboarding_required` is the fallback when onboarding changed
  after that check or `gameAccountId` is not one of the player's accounts.
- A new account starts with a generated handle such as `Swift_Falcon_4821` and a
  private profile, so `onboarding.profile` is already true. That handle is also the
  display name until the player chooses one. While `onboarding.displayName` is false,
  show the display-name screen and save the answer with `PATCH /v1/me`
  (`displayName`, 2-80 characters). Opponents, match rooms and public profiles show
  `displayName`; it is never taken from the email address, so never prefill the screen
  with the email.
- `GET /v1/legal/documents/current` returns the exact current terms/privacy versions.
- `POST /v1/me/legal-acceptances` records those exact versions; `GET` lists accepted
  versions.
- `PATCH /v1/me` updates personal details and the display name. It does not accept
  legal-consent booleans.
- `PUT /v1/me/profile` creates or updates handle, bio, discoverability, and profile
  preferences. While the display name is still pending it follows the handle.
- `POST /v1/me/avatar/uploads`, direct object upload, then
  `POST /v1/me/avatar/uploads/{id}/complete` uploads an avatar.
- `PATCH /v1/me/avatar` selects the completed avatar; `GET /v1/me/avatar` returns
  signed private access for the owner.
- `GET/PATCH /v1/me/notification-preferences` persists competition, match, result,
  and marketing preferences. The catalogue in section 3 names the preference that
  controls each push.

### 3. Device notifications and account security

- `POST /v1/me/push-tokens` registers or rotates an Expo device token and binds it to
  the session the request is signed in with. Register after every sign-in and on each
  app launch: an installation stops receiving pushes when its session ends (logout,
  remote session revoke, account deletion), and installations registered before
  sessions were linked were revoked by the server. Keep the returned `id`; the call is
  idempotent for the same `deviceId` and token, so repeating it returns the same `id`.
  A `401 invalid_session` means the session already ended: sign in again.
- `DELETE /v1/me/push-tokens/{id}` revokes it. Call it before `POST /v1/auth/logout`.
- `GET /v1/me/notifications` is cursor paginated.
- `GET /v1/me/notifications/unread-count` returns the badge count.
- `POST /v1/me/notifications/{id}/read` and
  `POST /v1/me/notifications/read-all` update the inbox.
- `GET/POST/DELETE /v1/me/account-deletion` requests, inspects, or cancels deletion;
  `POST /v1/me/account-deletion/execute` executes an eligible request after the
  cooling-off period.

#### Notification catalogue

Every inbox item comes from one platform event, and `data.kind` is that event's type.
Route and render on `kind` and the ids in `data`; never parse the title or body.
`data` holds identifiers only, never a score.

- Every event below is stored in the inbox. Rows whose delivery is push are also sent
  through Expo, but only while the named preference is on
  (`PATCH /v1/me/notification-preferences`) and only to installations registered with
  `POST /v1/me/push-tokens` whose session is still signed in. The push has the same
  title and body as the inbox item.
- The Expo push `data` is the item's `data` plus `notificationId`, plus `actionUrl`
  when it is not null (`PushNotificationData` in OpenAPI).
- When a push is tapped, call `POST /v1/me/notifications/{notificationId}/read`, then
  open the screen listed for its `kind`, using the ids in `data`.
- `actionUrl` is an in-app route hint, not an API path. It has no `/v1` prefix and must
  never be requested from `EXPO_PUBLIC_API_URL`. It is null when the item has no screen
  of its own; open the inbox item instead.
- Items stored before `kind` existed may lack it, or carry one of the legacy values
  `draw_generated`, `payment_succeeded`, `payment_review` or `payment_failed`. Route
  those, and any `kind` the app does not know yet, by `category` and the ids in `data`,
  and fall back to the inbox.

| Kind | Category | Delivery | Preference | Title | actionUrl | Data keys | App screen |
|---|---|---|---|---|---|---|---|
| `competition.check_in` | `competition` | push | `competitionPush` | Competition check-in is open | `/competitions/{competitionId}` | `kind`, `competitionId` | Competition detail |
| `competition.running` | `competition` | push | `competitionPush` | Competition started | `/competitions/{competitionId}` | `kind`, `competitionId` | Competition detail |
| `competition.cancelled` | `competition` | push | `competitionPush` | Competition cancelled | `/competitions/{competitionId}` | `kind`, `competitionId` | Competition detail |
| `competition.completed` | `competition` | push | `competitionPush` | Competition complete | `/competitions/{competitionId}` | `kind`, `competitionId` | Competition detail |
| `competition.draw_generated` | `competition` | push | `competitionPush` | Tournament draw is ready | `/competitions/{competitionId}/bracket` | `kind`, `competitionId` | Competition bracket |
| `match.ready` | `match` | push | `matchPush` | Your match is ready | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `match.forfeited` | `match` | push | `matchPush` | Match decided by forfeit | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `match.cancelled` | `match` | push | `matchPush` | Match cancelled | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `match.participant_checked_in` | `match` | push | `matchPush` | Opponent checked in | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `match.result_confirmed` | `result` | push | `resultPush` | Result confirmed | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `result.report_received` | `result` | push | `resultPush` | Your opponent reported the score | `/matches/{matchId}` | `kind`, `matchId` | Match room, score report |
| `result.report_reminder` | `result` | push | `resultPush` | Report your score now | `/matches/{matchId}` | `kind`, `matchId` | Match room, score report |
| `result.mismatch` | `result` | push | `resultPush` | Scores don't match | `/matches/{matchId}` | `kind`, `matchId` | Match room, final score |
| `result.under_review` | `result` | push | `resultPush` | Result under review | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `result.review_decided` | `result` | push | `resultPush` | Review complete | `/matches/{matchId}` | `kind`, `matchId` | Match room |
| `competition.entry_removed` | `result` | push | `resultPush` | Removed from tournament | `/matches/{matchId}` | `kind`, `competitionId`, `matchId` | Match room of the match that removed the entry |
| `player.strike_recorded` | `account` | push | `resultPush` | Conduct strike recorded | null | `kind`, `strikeId` | Inbox item |
| `player.strike_revoked` | `account` | inbox only | none | Conduct strike removed | null | `kind`, `strikeId` | Inbox item |
| `payment.succeeded` | `payment` | inbox only | none | Payment received | `/payments/{paymentId}` | `kind`, `paymentId` | Payment status (`GET /v1/payments/{paymentId}`); registered only when `registrationStatus` is `registered` |
| `payment.reconciliation_review_required` | `payment` | inbox only | none | Payment needs review | `/payments/{paymentId}` | `kind`, `paymentId` | Payment status (`GET /v1/payments/{paymentId}`) |
| `payment.review_marked_failed` | `payment` | inbox only | none | Payment not completed | `/payments/{paymentId}` | `kind`, `paymentId` | Payment status (`GET /v1/payments/{paymentId}`) |
| `payment.refund_requested` | `payment` | inbox only | none | Refund requested | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `payment.refund_approved` | `payment` | inbox only | none | Refund approved | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `payment.refund_rejected` | `payment` | inbox only | none | Refund rejected | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `payment.refund_succeeded` | `payment` | inbox only | none | Refund completed | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `payment.refund_failed` | `payment` | inbox only | none | Refund needs attention | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `payment.refund_manual_review` | `payment` | inbox only | none | Refund under review | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `payment.refund_processing` | `payment` | inbox only | none | Refund processing | `/refunds/{refundId}` | `kind`, `refundId`, `status` | Refunds list (`GET /v1/me/refunds`) |
| `game_account.verification_approved` | `account` | inbox only | none | Game account verification approved | `/game-accounts/{gameAccountId}` | `kind`, `verificationRequestId`, `status`, `gameAccountId` | Game account verification (`GET /v1/me/game-accounts/{gameAccountId}/verification`) |
| `game_account.verification_rejected` | `account` | inbox only | none | Game account verification rejected | `/game-accounts/{gameAccountId}` | `kind`, `verificationRequestId`, `status`, `gameAccountId` | Game account verification (`GET /v1/me/game-accounts/{gameAccountId}/verification`) |

Refund and game-account `status` is the state the event reports (`requested`,
`approved`, `rejected`, `succeeded`, `failed`, `manual_review`, `processing`); read the
current state from the API. Who receives each event:

- Competition events go to the captain, starters and substitutes of every entry that is
  still registered, checked in or accepted, or whose withdrawal is pending. The draw goes
  to every entry in the draw.
- Match and result events go to both entries' rosters, except entries that withdrew or
  were removed. `match.participant_checked_in` skips the player who checked in.
  `result.report_received` and `result.report_reminder` go only to the entry that still
  owes its report, and are dropped once it reported, was removed or its window closed.
- `competition.entry_removed` goes to the removed entry's roster.
- Strike, payment, refund and game-account events go only to the player they concern.

### 4. Game accounts and verification

- `GET/POST /v1/me/game-accounts` lists or adds an eFootball Mobile account.
- `PATCH /v1/me/game-accounts/{id}` edits it.
- `POST /v1/me/game-accounts/{id}/verification-requests` starts evidence review.
- `GET /v1/me/game-accounts/{id}/verification` returns status.
- `DELETE /v1/me/game-accounts/{id}/verification-requests/{requestId}` withdraws a request.

`publisherVerified` is true only for a publisher API verification. A Gamics manual
evidence review has `verificationMethod=manual_evidence` and must not be presented as
Konami verification.

### 5. Discovery, rankings, and public player profiles

- `GET /v1/games`
- `GET /v1/rankings` with game, country/global scope, bounded limit, and opaque cursor.
- `GET /v1/players` with search text, game, country, and opaque cursor.
- `GET /v1/players/{playerId}`
- `GET /v1/players/{playerId}/avatar`
- `GET /v1/players/{playerId}/matches`
- `GET /v1/players/{playerId}/competitions`

All collections use bounded keyset cursors. A hidden, suspended, or deleted player is
returned as `404`; the app must not try to distinguish those states.

### 6. Competition entry

- `GET /v1/competitions` and `GET /v1/competitions/{id}` discover competitions.
- `GET /v1/competitions/{id}/eligibility` is the preflight source of truth for age,
  country, ranking, game account, capacity, existing registration, payment action, and
  the conduct-strike ban (`conduct_suspended`).
- `GET /v1/competitions/{id}/bracket` returns typed stages, rounds, slots, matches,
  standings, and progression.
- `POST /v1/competitions/{id}/registrations` enters a free competition.
- `DELETE /v1/competitions/{id}/registrations/me` withdraws a free entry.
- `GET /v1/me/registrations` lists the player's entries.

For paid entry, call `POST /v1/payments/mpesa/stk-push` with an `Idempotency-Key`, then
poll `GET /v1/payments/{id}` until `status` is `succeeded`, `failed` or `review`. Never
call the provider callback from the app. Payment and registration success must come from
the backend, not from an STK screen or client timer.

`status=succeeded` only means M-Pesa confirmed the money. Show the outcome from
`registrationStatus` on the same payment:

| `registrationStatus` | Show |
|---|---|
| `pending` | Payment still being confirmed; keep polling (for `status=review`, see below) |
| `registered` | Registered |
| `refund_pending` | Payment received but no place; a full refund is on its way (`refund` has its status and reason) |
| `refunded` | Refunded (`refund.completedAt`) |
| `removed` | The entry was removed from the competition |
| `not_registered` | Not registered, for example after a failed payment |

A payment that completes after its place is gone (the competition was cancelled, the
draw was made, registration ended, the places filled or the player reached the conduct
strike limit) reads `succeeded` with `registrationStatus=refund_pending`; never show it
as a registration.

`status=review` (`registrationStatus=pending`) means Gamics is checking the outcome with
M-Pesa. Show "Payment under review. You will not be charged twice", stop fast polling,
and read the payment again when the player reopens the screen or a `payment.*`
notification arrives. Never start another payment for that competition.

If the app restarts during a payment, `GET /v1/competitions/{id}/eligibility` returns
the player's latest `paymentId`. Whenever `paymentId` is not null, read
`GET /v1/payments/{paymentId}` and act on its `status` and `registrationStatus`,
whatever `requiredAction` says. `requiredAction` is `poll_payment` while that payment is
unfinished, even when registration has since closed or filled: the payment can still
register the player or be refunded.

- `GET /v1/me/payments` lists durable payment/receipt history.
- `GET /v1/me/refunds` lists refund state.
- `POST /v1/competitions/{id}/registrations/me/withdrawal-requests` requests a paid
  withdrawal/refund.

### 7. Match room, evidence, and result verification

- `GET /v1/me/matches?state=active|history`
- `GET /v1/matches/{matchId}` returns `lifecycle`, `allowedActions`,
  `verificationPolicy` (report window, reminder lead, response window, screenshot
  rules), `resultVerification`, `result`, `completionReason`, and both check-in
  states.
- `POST /v1/matches/{matchId}/check-ins` uses an `Idempotency-Key`.

Drive the screen from `lifecycle` and `allowedActions`; never infer an action from
the state or a local timer.

| `lifecycle` | Meaning | Action |
|---|---|---|
| `report_required` | Your entry has not reported yet | `report_score` |
| `awaiting_opponent_report` | You reported; the opponent's report window is running | none |
| `mismatch_response_required` | The reports differ and your entry has not responded | `submit_final_score` |
| `awaiting_opponent_response` | You responded; the response window is still running | none |
| `awaiting_resolution` | A deadline passed and the server is settling the match | none |
| `under_review` | Gamics is reviewing the match | none |
| `forfeited` / `completed` | The match is over | none |

The room is blind. `resultVerification` holds only your entry's own reports
(`myReport`, `myFinalReport`), whether the opponent reported or responded, the
deadline of the current phase, the resolution, and `entryRemoved` when your entry
was removed from the tournament. It never contains the opponent's score. The
confirmed score appears in `result` once the match is completed.

Evidence flow (screenshots are needed only for a final score after a mismatch):

1. `POST /v1/evidence/uploads` declares a JPEG or PNG screenshot's size, media type
   and SHA-256. Video is not accepted.
2. Upload directly to the returned signed URL with the exact required headers.
3. `POST /v1/evidence/uploads/{id}/complete` durably queues verification and returns
   `processing`, `ready=false`.
4. Poll `GET /v1/evidence/uploads/{id}` with 2-10 second exponential backoff and
   jitter until `ready=true`. Stop on rejected/failed/expired; respect Retry-After
   for 429/503. Persist the evidence ID so polling can resume after an app restart.
5. `GET /v1/evidence/{id}` gives short-lived access to the uploader, and to Gamics
   reviewers and admins. Opponents and organizers never see another player's
   screenshots.

Screenshots must be JPEG or PNG, at most 10 MiB by default, 16 megapixels and
8192 pixels per dimension. Convert HEIC/HEIF on the device, then compute size and
SHA256 from the final uploaded bytes. PUT raw binary with the returned headers,
including `If-None-Match: *`. Content-Length is signed: a web Blob supplies it
automatically; native uploads must send a fixed-length body. A 412 on a retried
PUT can mean the first attempt succeeded: call completion and let verification
decide. Keep signed URLs and credentials out of logs/analytics. Cap uploads at
two per device. See [screenshot pipeline](screenshot-pipeline.md).

Report the score with `POST /v1/matches/{matchId}/score-reports` and an
`Idempotency-Key`: home and away score, a penalty tiebreak where a knockout match is
tied, optional game rows, and `declarationAccepted=true`. No screenshot is sent. When
the reports differ, send the one final score with
`POST /v1/matches/{matchId}/score-reports/final`: the same fields plus one to three
`evidenceIds` of ready screenshots. Both responses contain the updated blind `match`
room and the `report` just stored, so the app does not need an unsafe replica read.

Tell players plainly what silence costs: if they do not report within the report
window after the opponent's report, or do not send a final score within the response
window after a mismatch, their entry is removed from the tournament. If nobody reports
before `resultDueAt`, both entries are removed. The full policy is in
[result verification](result-verification.md). There is no dependency on a public
Konami results API.

## Organizer and staff clients

The same OpenAPI contract includes organization membership and permissions,
competition creation/transitions, typed draw generation and entries for organizers,
and, for Gamics platform staff only, the result review queue, conduct strikes,
game-account verification review, refund decisions, and payment review. Organizers
never decide match results. These routes are permission protected and must not be
exposed merely because a tab is visible.

## Client rules

- Generate request/response types from OpenAPI 0.7.0 and keep Zod validation at the
  network boundary.
- Use a fresh UUID `Idempotency-Key` for each user intent and reuse it only for retries
  of that exact payload.
- Treat `401` as one serialized refresh attempt; never retry mutations blindly.
- Honor `429` and `Retry-After`, show offline/empty/error states, and cancel abandoned
  list requests.
- Keep cursor values opaque and never build object-storage URLs from keys.
- Disable a capability visibly when its required production service is unavailable;
  never report fixture data as accepted by Gamics.
