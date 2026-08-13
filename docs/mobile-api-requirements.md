# Mobile API requirements

This is the handoff contract for the mobile implementation model. Routes marked
**implemented** are in `services/api/openapi/openapi.yaml`; routes marked **planned**
must stay behind typed repository interfaces and demo adapters until their Go handler
and OpenAPI operation exist. The app must not silently call invented endpoints.

## Available now

- `POST /v1/auth/otp/request`
- `POST /v1/auth/otp/verify`
- `POST /v1/auth/refresh`
- `POST /v1/auth/logout`
- `GET/PATCH /v1/me`
- `PUT /v1/me/profile`
- `GET/POST/PATCH /v1/me/game-accounts`
- `GET /v1/games`
- `POST /v1/payments/mpesa/stk-push`
- `GET /v1/payments/{id}`

The Daraja callback route is provider-only and must never be present in mobile code.

## Player discovery and rankings — planned

### `GET /v1/rankings`

Query:

```text
gameId=efootball-mobile
scope=country|global
country=KE
limit=20                         # maximum 50
cursor=<opaque-server-cursor>
```

Response rows are deliberately compact:

```json
{
  "data": [{
    "rank": 24,
    "playerId": "uuid",
    "handle": "brian.mainaa",
    "displayName": "Brian Maina",
    "avatarUrl": "https://short-lived-or-public-cdn-url",
    "countryCode": "KE",
    "rating": 1842,
    "matchesPlayed": 41,
    "rankMovement": 3
  }],
  "page": { "nextCursor": "opaque-or-null", "hasMore": true },
  "snapshotAt": "2026-08-09T12:00:00Z"
}
```

Use keyset/opaque cursor pagination over a versioned leaderboard snapshot; never use
unbounded offset pagination. Repeated pages must stay on the same snapshot so rating
updates do not duplicate/skip players. Cache top pages briefly and serve immutable
snapshot rows. A background ranking projection computes global/country position and
movement rather than running a full-table window sort for every phone request.

### `GET /v1/players`

Search discoverable players with `q`, `gameId`, `country`, `limit` and `cursor`.
Search matches normalized handle, display name and connected in-game name. PostgreSQL
trigram indexes are sufficient at launch; move the public projection to a search
index only after measured pressure. The response uses the same compact player row as
rankings and never includes email, phone, birth date or payment identifiers.

### Public profile/history

- `GET /v1/players/{playerId}` returns avatar, handle/name, bio, country, ratings,
  global/country rank, win/draw/loss totals and consent-safe public game accounts.
- `GET /v1/players/{playerId}/matches?limit=20&cursor=...` returns confirmed public
  match history, opponent summary, score, outcome and competition.
- `GET /v1/players/{playerId}/competitions?limit=20&cursor=...` returns tournament
  history, placement, format and status.

All three honor `discoverable`; private/suspended/deleted profiles return `404` to
avoid account enumeration. Avatar URLs are CDN URLs, never object-store credentials.

## Match room and result submission — planned

### Assignment and check-in

- `GET /v1/me/matches?state=active|history&limit=20&cursor=...`
- `GET /v1/matches/{matchId}`
- `POST /v1/matches/{matchId}/check-ins` with `Idempotency-Key`

Match detail includes the player's side, opponent public summary, stage/round,
best-of, deadline, check-in state, eFootball Friend Match instructions and allowed
actions. The server derives allowed actions; the app must not infer authorization
from labels.

### Evidence upload

1. `POST /v1/evidence/uploads` with media type, byte size and SHA-256 checksum.
2. API returns a short-lived upload URL, required headers, opaque object key and
   expiry.
3. App uploads the selected screenshot directly, showing progress.
4. `POST /v1/evidence/uploads/{id}/complete` lets the API verify object size/checksum.

Only completed opaque evidence IDs are accepted in a result. Provider URLs and object
keys are not analytics data and are never public.

### Submit result

`POST /v1/matches/{matchId}/result-submissions` with `Idempotency-Key`:

```json
{
  "homeScore": 3,
  "awayScore": 1,
  "games": [{ "homeScore": 3, "awayScore": 1 }],
  "evidenceIds": ["uuid"],
  "declarationAccepted": true
}
```

The server checks participant identity, match version/state, score policy, evidence
ownership/completion and deadline. It returns a submission in
`pending_confirmation`; the submitter then sees an awaiting-opponent state.

### Opponent decision

- `POST /v1/result-submissions/{id}/confirmations` body
  `{ "decision": "confirm" }`, with `Idempotency-Key`.
- The same route accepts `{ "decision": "dispute", "reasonCode": "score_mismatch",
  "note": "...", "evidenceIds": ["uuid"] }`.

A confirmation transaction locks the submission/match, finalizes the result, updates
ratings/progression and writes audit/outbox events once. A dispute opens the referee
queue and freezes advancement. The response always returns the resulting match and
submission states so the app can update without a replica read.

## Typed mobile capability boundary

The mobile API layer exposes capability flags such as:

```ts
type BackendCapabilities = {
  playerDiscovery: 'demo' | 'available';
  rankings: 'demo' | 'available';
  matchOperations: 'demo' | 'available';
  evidenceUploads: 'unavailable' | 'available';
  resultDecisions: 'demo' | 'available';
};
```

Production builds must fail or visibly disable a feature whose capability is not
`available`; they must never report a demo submission as accepted by Gamics. Every
collection uses bounded cursor pagination, cancellation, retry classification and
loading/empty/offline/error states.
