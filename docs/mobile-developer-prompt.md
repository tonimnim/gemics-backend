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
5. Receive match reminders, check in, view opponent and Friend Match instructions.
6. Submit a result: winner/score, final-result screenshot upload and declaration.
7. Review an opponent submission and choose Confirm or Dispute with a reason and evidence.
8. Follow bracket progress, match history, notifications, ranking and public player card.
9. Handle loading, empty, offline, retry, expired-session and API-error states.

Use Expo Router, strict TypeScript, TanStack Query for server state, React Hook
Form plus Zod for forms, Expo SecureStore for refresh/session secrets, Expo
Notifications, Expo ImagePicker/Camera, and an upload client that supports
short-lived signed object-storage URLs. Keep domain features separate from UI
components and keep all HTTP calls behind a typed API client.

## API access

Read the backend contract in `services/api/openapi/openapi.yaml`. The implemented
identity endpoints cover email OTP request/verification, token refresh, logout,
current player, profile and game accounts. Do not invent any other endpoints; put
unfinished competition and match resources behind repository interfaces until
their OpenAPI contract and Go handlers are added.

Use `EXPO_PUBLIC_API_URL`:

- Android emulator: `http://10.0.2.2:8080`
- iOS simulator: `http://127.0.0.1:8080`
- physical device: `http://<backend-computer-lan-ip>:8080`
- production: `https://api.gamics.io`

Never embed database credentials, payment secrets or object-storage secrets in
the app. Production API calls require HTTPS. Send the access token as a Bearer
token, rotate it using a refresh token stored in SecureStore, attach
`X-Request-ID`, and send `Idempotency-Key` on retryable mutations.

Prepare typed repositories for these required backend resources without
inventing response fields: auth/session, current profile, game accounts,
competitions, registrations, match check-in, my matches, result submissions,
result confirmation/dispute, signed evidence uploads, push tokens and rankings.

## Result workflow

The winner submits the score plus a final-result screenshot. The opponent then
confirms or disputes. A matching confirmation finalizes the result. A dispute
requires both players' evidence and enters a referee queue. A screenshot-backed
submission may auto-confirm after the published response deadline; a text-only
claim must not win by opponent silence. High-stakes matches require evidence
from both players and can require a short screen recording or referee monitoring.

Upload directly to private object storage through a short-lived signed URL, then
send only the object key, checksum, media type and size to the API. Compress
images responsibly, strip unnecessary metadata, show upload progress and allow
retry. Never place raw evidence in analytics or public player profiles.

## Delivery quality

Create an environment example, README and automated checks. Add unit tests for
score validation and state transitions, integration tests for the typed client,
and end-to-end coverage for register -> check-in -> submit -> confirm/dispute.
Run TypeScript, lint, Expo Doctor and Android/iOS build validation. Return a list
of screens, architecture decisions, API assumptions, test results and anything
still blocked by an unimplemented backend endpoint.
