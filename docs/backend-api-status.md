# Backend API status

Last audited: 2026-09-28 against OpenAPI `0.7.0` and the registered Go routes.

## Sources of truth

- API service: `services/api`
- Authoritative contract: `services/api/openapi/openapi.yaml`
- Mobile integration sequence: `docs/mobile-api-requirements.md`
- Result policy: `docs/result-verification.md`
- Production topology: `docs/production-architecture.md`
- Route registration: `services/api/internal/httpapi/server.go` and the modular route
  registrars it calls

Requirements documents explain behavior; they do not override OpenAPI.

## Implemented API surface

The contract currently covers:

- Email OTP sign-in, rotating refresh sessions, explicit logout, and device/session
  revocation.
- Current-player onboarding, versioned legal acceptance, profile and avatar media,
  preferences, and cooling-off account deletion.
- Expo push-device registration and notification inbox/read operations.
- eFootball Mobile accounts and evidence-based verification workflow, with separate
  manual and publisher-verification semantics.
- Supported games, competition discovery/detail, structured eligibility, typed
  bracket, round-robin standings and final placements, free registration/withdrawal,
  and paid entry through M-Pesa STK.
- Player payment history/status, refund history, paid withdrawal, administrative
  reconciliation review, and protected refund decisions.
- Active/history matches, the blind match room and verification policy, idempotent
  check-in, private JPEG/PNG screenshot evidence, blind initial score reports, and
  final score reports with screenshots after a mismatch.
- Deadline handling by the result verification worker: report reminders, removal of
  silent entries from the tournament, and escalation of stuck screenshots to review.
- The Gamics result review queue (list, detail, decision) for platform reviewers and
  admins, conduct strikes with admin revocation, and the strike-based registration ban.
- Player search, snapshot rankings, public profiles, avatar, match history, and
  competition history with bounded signed cursors.
- Organization membership/RBAC, competition authoring/lifecycle, deterministic draw
  persistence, knockout/double-elimination/round-robin progression, no-show handling,
  standings, placements, stage completion, and competition completion.

The provider-only `POST /v1/payments/mpesa/callback/{token}` is intentionally in the
backend contract and must never be called by a player client.

## Production services still required

These are deployment integrations, not missing player routes:

1. SMTP credentials for email OTP delivery.
2. Daraja production credentials, Till/PayBill configuration, and a public HTTPS
   callback origin.
3. Daraja Pull Transactions (or an equivalent independent receipt source) to recover
   a success when the STK callback is truly lost.
4. An M-Pesa B2C/refund provider worker to execute durable refund requests. The API
   never claims a refund succeeded until a provider receipt is stored.
5. Production private S3-compatible object storage for screenshots and avatars.
   Docker uses local MinIO for development.
6. Expo push credentials and network access to the Expo push/receipt services.
7. A transactional-email notification provider/queue beyond OTP email, including
   bounce and complaint handling.
8. A secure operational bootstrap for the first platform administrator/reviewer.
9. Production control-plane infrastructure: managed PostgreSQL failover/replica,
   managed Redis, load balancer/WAF, secrets manager, backups, observability, and
   autoscaling. Docker Compose is a development topology, not automatic HA.

## Explicit follow-up product work

- Build the Gamics staff console for the result review queue and strike feed. The
  API is complete; there is no staff web client in this repository.
- Decide whether an automated (system) review decider is wanted. The decision path
  already accepts one, but none is built, and it can never record strikes.
- Add transactional notification email templates and delivery jobs if email alerts
  are enabled.
- Add a historical bracket correction/unwind workflow for an administrator overturning
  an already-progressed result.
- Add richer round-robin tie-break rules such as head-to-head if a competition format
  requires them; current persisted ordering uses points, goal difference, and goals.

Phone OTP is not required for launch. Konami result ingestion is also not required:
the implemented trust model is blind dual score reports, screenshots after a
mismatch, removal of silent entries, and Gamics staff review of the claims that
still differ.
