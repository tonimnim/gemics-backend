# Prompt for the Tonits player app developer

You are building the production Tonits player application in Flutter for Android
and iOS, in its own repository. Tonits is an esports competition platform launching
in Kenya with eFootball Mobile. It is not a betting product: players compete by
skill, there are no wagers, and player entry fees must not be represented as a
pooled prize fund.

Match the Tonits look used by the staff dashboard and the website: light by
default, a soft grey canvas (#f4f5fa), white cards with large rounded corners, an
indigo accent (#5b5bd6) with lavender tints, Geist for text, halftone dot accents
and the halftone player artwork. Keep copy minimal: one clear headline and the
controls a screen needs. Do not copy Konami/eFootball branding or imply publisher
affiliation. Build native Flutter UI, not a website wrapped in a WebView.

## Product scope

Build these player journeys:

1. Registration with only a username, Konami ID and password (which also creates
   the eFootball Mobile game account), and sign-in with Konami ID and password.
2. Add an email (communication, password recovery) and a phone number (payments)
   later from the account screen, plus profile and consent choices.
3. Discover/search competitions; view format, schedule, rules, capacity,
   organizer, entry type and prize source. A cancelled competition still opens by id
   and must show its cancelled state.
4. Register or withdraw, complete any eligibility step and see registration status.
   For a paid event, choose the connected game account and M-Pesa number, generate a
   new Idempotency-Key once, start STK Push, then poll the returned payment ID until
   it is `succeeded`, `failed` or `review`. Reuse the same key after network retries.
   A `succeeded` payment is a registration only when its `registrationStatus` is
   `registered`; `refund_pending` means the money is being returned. Free and paid
   entry both refuse a blocked player with `409 competition_ineligible`: show its
   `message` and switch on `issue.code`, as for the eligibility preflight.
5. Receive match reminders, check in, view the opponent, including the eFootball
   in-game name and User ID in the room's `gameAccount`, and the Friend Match
   instructions.
6. After the match, either player submits the result (score and declaration, no
   screenshot); the opponent sees it and confirms or rejects it.
7. After a rejection, each player sends one screenshot of the Full Time screen
   before the deadline, then follows the match through Tonits review.
8. Follow bracket progress, round-robin tables and final placements, match history,
   notifications, ranking and public player card.
9. Handle loading, empty, offline, retry, expired-session and API-error states.

Use go_router for navigation, a typed API client generated from the OpenAPI
contract (or hand-written models with strict JSON parsing), Riverpod or Bloc for
state, flutter_secure_storage for refresh/session secrets, firebase_messaging for
push, image_picker for screenshots, and an upload client that supports short-lived
signed object-storage URLs. Keep domain features separate from UI widgets and keep
all HTTP calls behind the typed client.

## API access

Read the authoritative OpenAPI 0.9.0 contract in
`services/api/openapi/openapi.yaml` and the integration sequence in
`docs/mobile-api-requirements.md`. The backend now covers Konami ID registration/sign-in and rotating
sessions; email/phone/password/legal/profile/avatar; game accounts and verification; discovery,
eligibility, registration and M-Pesa; matches, result submission and confirmation,
screenshots; rankings, histories, notifications and account lifecycle. Generate models
from OpenAPI and validate responses at runtime. Do not call the provider callback route from the app and do
not invent fields or endpoints.

Route every push tap and inbox item by `data.kind` and the ids in `data`, using the
notification catalogue in `docs/mobile-api-requirements.md`. Mark the tapped item read
with its `notificationId`. `actionUrl` is an in-app route hint, never an API path.
Register the device push token after every sign-in and on each app launch (it is
bound to the session), and delete it with `DELETE /v1/me/push-tokens/{id}` before
logout. The backend still delivers pushes through Expo's push service; it moves to
Firebase Cloud Messaging before release, so build against FCM tokens.

Set a capability to unavailable only when its required deployment service is not
configured (for example Daraja, push delivery or object storage). A local UI action must never
pretend that a fixture accepted money, evidence or a match result.

Take the API base URL from the build configuration (`--dart-define=API_URL=...`):

- Android emulator: `http://10.0.2.2:8090` (the local Docker stack)
- iOS simulator: `http://127.0.0.1:8090`
- physical device: `http://<backend-computer-lan-ip>:8090`
- production: the HTTPS API origin

Never embed database credentials, payment secrets or object-storage secrets in
the app. Production API calls require HTTPS. Send the access token as a Bearer
token, rotate it using a refresh token stored in secure storage, attach
`X-Request-ID`, and send `Idempotency-Key` on retryable mutations.

Implement typed repositories for auth/session, current profile/legal/avatar, game
accounts, competitions/eligibility/registrations/brackets/standings, payments/refunds, match
check-in, result submission, confirmation and screenshots, signed evidence, push/inbox,
rankings/search and public player histories.

## Result workflow

After the match either player submits the result with
`POST /v1/matches/{matchId}/score-reports` (score only, no screenshot). The opponent
sees it in `resultVerification.submittedResult` and answers with
`POST /v1/matches/{matchId}/score-reports/confirmation` (`confirm` or `reject`)
within the confirmation window (10 minutes by default). Confirming settles the
result at once; if the opponent doesn't answer in time, the submitted result stands.
A rejection opens the screenshot window: each player sends one ready screenshot with
`POST /v1/matches/{matchId}/score-reports/screenshot`, and the match goes to Tonits
review. A player who sends no screenshot in time is removed from the tournament,
and if nobody submits a result before the result deadline both entries are removed.
Make every deadline visible and drive actions only from `lifecycle` and
`allowedActions`; `awaiting_resolution` means a deadline has passed and the server
is settling the match. When the match is over, `lifecycle` is `completed`,
`forfeited` or `cancelled`, and `outcome` (`won`, `lost`, `drawn` or `no_result`)
tells the player how it ended for them, including a forfeit, which has no score;
explain it with `completionReason`. Show a removal only on the match whose
`resultVerification.entryRemoved` is true; a removed player's unplayed fixtures show
`out_of_competition` and leave the active list.

Cloudflare R2 is the selected private object store; the client uses only API-issued
URLs and never needs Cloudflare credentials or a hard-coded bucket URL.
Declare checksum, media type and size to the API, upload directly to private object
storage through the returned short-lived signed URL, complete the upload, then poll
`GET /v1/evidence/uploads/{id}` with backoff until `ready=true`. Completion queues
asynchronous verification. Evidence is JPEG or PNG screenshots only; video is not
accepted. Convert HEIC/HEIF to JPEG before hashing; send the exact signed length,
type, SHA256 and `If-None-Match: *` headers. Attach only the opaque ready evidence IDs
to the screenshot submission. Start uploading as soon as the result is rejected so
the screenshot is ready before the deadline.
Never send or persist an object
key in the mobile client. Compress
images responsibly, strip unnecessary metadata, show upload progress and allow
retry. Never place raw evidence in analytics or public player profiles.

## Delivery quality

Create an environment example, README and automated checks. Add unit tests for
score validation and state transitions, integration tests for the typed client,
and end-to-end coverage for register -> check-in -> submit -> confirm, and
submit -> reject -> screenshot. Run `flutter analyze`, `flutter test` and Android/iOS
build validation. Return a list
of screens, architecture decisions, API assumptions, test results and any deployment
service that must be configured before fixtures can be disabled.
