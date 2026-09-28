# Gamics mobile

The player product is an Expo SDK 57 / React Native application using Expo
Router. It targets Android and iOS; the marketing site remains the web surface.

Copy `.env.example` to `.env` and replace the sample LAN address with the
machine running the Go API. Android emulators can use `http://10.0.2.2:8080`.

```sh
npm install
npm start
```

Use a development build for product work. Expo Go is useful for early layout
checks, but evidence capture, notifications and secure session storage will be
validated in development builds before release.

The current player build includes the native five-tab shell, compact searchable
rankings, public player profiles, match history and a blind score-report
prototype: a score-only report, and a final score with one to three screenshots
when the reports don't match. The opponent's score is never shown. Match, ranking
and evidence mutations are visibly marked demo-only and capability-gated; they do
not claim a backend action succeeded.

Backend handoff:

- implemented routes: `../../services/api/openapi/openapi.yaml`;
- planned mobile ranking/match/evidence contract: `../../docs/mobile-api-requirements.md`;
- full separate-mobile-developer prompt: `../../docs/mobile-developer-prompt.md`.

Before an Android/iOS release, replace demo repositories only after the matching Go
handler and OpenAPI operation exist, then change that capability to `available`.
