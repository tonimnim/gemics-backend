# Prompt for the Gamics mobile AI developer

You are building the production Gamics player application as a separate Expo
SDK 57 / React Native project for Android and iOS. Gamics is an esports
competition platform launching in Kenya with eFootball Mobile. It is not a
betting product: players compete by skill, there are no wagers, and player entry
fees must not be represented as a pooled prize fund.

Use the supplied Gamics artwork as the visual reference: editorial sports-poster
energy, ink black, warm paper, electric acid yellow, royal blue and orange;
condensed oversized headlines; sharp bracket/grid motifs; Kenyan identity;
strong contrast and purposeful motion. Do not copy Konami/eFootball branding or
imply publisher affiliation. Build accessible native UI, not a website wrapped
in a WebView.

## Product scope

Build these player journeys:

1. Email OTP onboarding, profile creation, age/country eligibility and
   consent choices.
2. Add an eFootball Mobile game account and public player handle.
3. Discover/search competitions; view format, schedule, rules, capacity,
   organizer, entry type and prize source.
4. Register or withdraw, complete any eligibility step and see registration status.
   For a paid event, choose the connected game account and M-Pesa number, generate a
   new Idempotency-Key once, start STK Push, then poll the returned payment ID until
   it is `succeeded`, `failed` or `review`. Reuse the same key after network retries.
5. Receive match reminders, check in, view opponent and Friend Match instructions.
6. Report the score blind (score and declaration only, no screenshot) without ever
   seeing what the opponent reported.
7. When the reports don't match, submit one final score with one to three
   screenshots before the response deadline, then follow the match through Gamics
   review if the scores still differ.
8. Follow bracket progress, match history, notifications, ranking and public player card.
9. Handle loading, empty, offline, retry, expired-session and API-error states.

Use Expo Router, strict TypeScript, TanStack Query for server state, React Hook
Form plus Zod for forms, Expo SecureStore for refresh/session secrets, Expo
Notifications, Expo ImagePicker/Camera, and an upload client that supports
short-lived signed object-storage URLs. Keep domain features separate from UI
components and keep all HTTP calls behind a typed API client.

## API access

Read the authoritative OpenAPI 0.7.0 contract in
`services/api/openapi/openapi.yaml` and the integration sequence in
`docs/mobile-api-requirements.md`. The backend now covers email OTP and rotating
sessions; onboarding/legal/profile/avatar; game accounts and verification; discovery,
eligibility, registration and M-Pesa; matches, blind score reports and evidence; rankings,
histories, notifications and account lifecycle. Generate types from OpenAPI and keep
runtime Zod validation. Do not call the provider callback route from the app and do
not invent fields or endpoints.

Route every push tap and inbox item by `data.kind` and the ids in `data`, using the
notification catalogue in `docs/mobile-api-requirements.md`. Mark the tapped item read
with its `notificationId`. `actionUrl` is an in-app route hint, never an API path.
Register the Expo push token after every sign-in and on each app launch (it is bound
to the session), and delete it with `DELETE /v1/me/push-tokens/{id}` before logout.

Set a capability to unavailable only when its required deployment service is not
configured (for example Daraja, Expo or object storage). A local UI action must never
pretend that a fixture accepted money, evidence or a match result.

Use `EXPO_PUBLIC_API_URL`:

- Android emulator: `http://10.0.2.2:8080`
- iOS simulator: `http://127.0.0.1:8080`
- physical device: `http://<backend-computer-lan-ip>:8080`
- production: `https://api.gamics.io`

Never embed database credentials, payment secrets or object-storage secrets in
the app. Production API calls require HTTPS. Send the access token as a Bearer
token, rotate it using a refresh token stored in SecureStore, attach
`X-Request-ID`, and send `Idempotency-Key` on retryable mutations.

Implement typed repositories for auth/session, current profile/legal/avatar, game
accounts, competitions/eligibility/registrations/brackets, payments/refunds, match
check-in, blind score reports and final score reports, signed evidence, push/inbox,
rankings/search and public player histories.

## Result workflow

Each entry reports its score blind: score only, no screenshot, and the app never
shows or asks about the opponent's claim. Equal reports confirm the result at once.
If the opponent has not reported, their report window (10 minutes by default) is
running; the room shows only that the opponent has or has not reported. Different
reports open a response window in which each side may send one final score with one
to three ready screenshots; if the scores still differ, Gamics staff review the match.
Silence has a cost: a player who does not report, or does not respond after a
mismatch, before the deadline is removed from the tournament, and if nobody reports
before the result deadline both entries are removed. Make every deadline visible and
drive actions only from `lifecycle` and `allowedActions`; `awaiting_resolution`
means a deadline has passed and the server is settling the match.

Cloudflare R2 is the selected private object store; the client uses only API-issued
URLs and never needs Cloudflare credentials or a hard-coded bucket URL.
Declare checksum, media type and size to the API, upload directly to private object
storage through the returned short-lived signed URL, complete the upload, then poll
`GET /v1/evidence/uploads/{id}` with backoff until `ready=true`. Completion queues
asynchronous verification. Evidence is JPEG or PNG screenshots only; video is not
accepted. Convert HEIC/HEIF to JPEG before hashing; send the exact signed length,
type, SHA256 and `If-None-Match: *` headers. Attach only the opaque ready evidence IDs
to the final score report. Start uploading as soon as the mismatch opens so the
screenshots are ready before the response deadline.
Never send or persist an object
key in the mobile client. Compress
images responsibly, strip unnecessary metadata, show upload progress and allow
retry. Never place raw evidence in analytics or public player profiles.

## Delivery quality

Create an environment example, README and automated checks. Add unit tests for
score validation and state transitions, integration tests for the typed client,
and end-to-end coverage for register -> check-in -> report -> (mismatch -> final score).
Run TypeScript, lint, Expo Doctor and Android/iOS build validation. Return a list
of screens, architecture decisions, API assumptions, test results and any deployment
service that must be configured before fixtures can be disabled.
