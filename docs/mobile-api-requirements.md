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
- `POST /v1/auth/logout` revokes the current session.
- `GET /v1/me/sessions` and `DELETE /v1/me/sessions/{id}` manage other devices.

Phone OTP is intentionally not part of onboarding. A phone number is requested only
when a player chooses M-Pesa.

### 2. Onboarding, legal consent, profile, and avatar

- `GET /v1/me` returns the player and onboarding status.
- `GET /v1/legal/documents/current` returns the exact current terms/privacy versions.
- `POST /v1/me/legal-acceptances` records those exact versions; `GET` lists accepted
  versions.
- `PATCH /v1/me` updates personal details. It does not accept legal-consent booleans.
- `PUT /v1/me/profile` creates or updates handle, bio, discoverability, and profile
  preferences.
- `POST /v1/me/avatar/uploads`, direct object upload, then
  `POST /v1/me/avatar/uploads/{id}/complete` uploads an avatar.
- `PATCH /v1/me/avatar` selects the completed avatar; `GET /v1/me/avatar` returns
  signed private access for the owner.
- `GET/PATCH /v1/me/notification-preferences` persists competition, match, and
  marketing preferences.

### 3. Device notifications and account security

- `POST /v1/me/push-tokens` registers or rotates an Expo device token.
- `DELETE /v1/me/push-tokens/{id}` revokes it.
- `GET /v1/me/notifications` is cursor paginated.
- `GET /v1/me/notifications/unread-count` returns the badge count.
- `POST /v1/me/notifications/{id}/read` and
  `POST /v1/me/notifications/read-all` update the inbox.
- `GET/POST/DELETE /v1/me/account-deletion` requests, inspects, or cancels deletion;
  `POST /v1/me/account-deletion/execute` executes an eligible request after the
  cooling-off period.

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
poll `GET /v1/payments/{id}` until a terminal state. Never call the provider callback
from the app. Payment and registration success must come from the backend, not from an
STK screen or client timer.

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
