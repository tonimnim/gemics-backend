# M-Pesa Daraja integration

## Current state

The API contains a sandbox/production Daraja client, OAuth token reuse, STK Push,
STK Query, durable callback journaling, idempotent payment intents, velocity limits,
capacity reservation and atomic paid registration. Configuration defaults to
`MPESA_ENVIRONMENT=disabled`; no real Safaricom secret is committed to this project.

Do not describe an accepted STK request as a payment. `ResponseCode=0` means only
that Daraja accepted the prompt. Gamics marks a payment successful only after a
success callback is consistent with the stored amount, phone and request IDs and an
authenticated STK Query confirms the same checkout. The payment and competition
entry then commit together.

## Required private configuration

Put these values in a deployment secret manager (or the ignored `.env.docker` only
for local work):

```text
MPESA_ENVIRONMENT=sandbox
MPESA_CONSUMER_KEY=...
MPESA_CONSUMER_SECRET=...
MPESA_SHORT_CODE=...
MPESA_PASSKEY=...
MPESA_CALLBACK_BASE_URL=https://api.example.com
MPESA_CALLBACK_TOKEN=<32-128 random URL-safe characters>
MPESA_TRANSACTION_TYPE=CustomerPayBillOnline
```

`MPESA_CALLBACK_BASE_URL` is an HTTPS origin, without a path. Gamics appends
`/v1/payments/mpesa/callback/<token>`. Rotate the token if it appears in any proxy,
WAF or historical application log, and configure edge logs to redact that path.

Sandbox uses `https://sandbox.safaricom.co.ke`; production uses
`https://api.safaricom.co.ke`. Production also requires a Daraja Go-Live app,
active PayBill/Till, business shortcode, Lipa-na-M-Pesa passkey and an approved
public callback. Start with Safaricom's official [Getting Started](https://developer.safaricom.co.ke/apis/GettingStarted),
[Authorization](https://developer.safaricom.co.ke/apis/Authorization) and
[M-Pesa Express](https://developer.safaricom.co.ke/apis/MpesaExpressSimulate)
documentation.

## Client flow

1. The signed-in, fully onboarded player chooses a competition, their matching game
   account and a Safaricom number.
2. The app creates one random `Idempotency-Key` and calls
   `POST /v1/payments/mpesa/stk-push`. Network retries reuse that same key.
3. The writer transaction locks competition capacity and creates one active payment
   reservation per player/competition before Daraja is contacted.
4. The app shows “check your phone” and polls `GET /v1/payments/{id}`. Backend query
   leases and exponential backoff prevent mobile polling from hammering Daraja. An app
   that lost the payment ID finds it as `paymentId` on
   `GET /v1/competitions/{id}/eligibility`. Whenever it is not null the app reads that
   payment, whatever `requiredAction` says; `requiredAction` is `poll_payment` while the
   payment is unfinished, even after registration closed or the places filled. At
   `review` the app stops fast polling and tells the player they will not be charged
   twice.
5. A verified success creates `competition_entries` and `entry_members`, updates the
   payment and writes audit/outbox events in one PostgreSQL transaction. If the place is
   gone by then (the competition was cancelled, the draw was made, registration ended,
   the places filled or the payer reached the conduct strike limit), the same
   transaction creates a `withdrawal_pending` entry that never plays and a mandatory
   full refund.

Payment status meanings:

- `initiating`: provider request is being made;
- `pending`: prompt accepted, waiting for a terminal provider result;
- `callback_received`: callback is durable and verification is in progress;
- `succeeded`: the collection is verified and committed together with its registration
  outcome; it does not by itself mean the payer is registered;
- `failed`: Daraja confirmed a terminal non-success result;
- `review`: outcome or business state is ambiguous; do not create another charge.

Every payment view (the STK response, `GET /v1/payments/{id}` and `GET /v1/me/payments`)
also carries `registrationStatus` and `refund`, derived from the entry and the latest
refund and shown only to the payer. After `succeeded`, the app must read
`registrationStatus`:

- `pending`: the payment is not final yet;
- `registered`: the payment holds an active entry;
- `refund_pending`: the entry will not play and a full refund is owed, either because
  the payment arrived after its place was gone or because the payer asked to withdraw;
  `refund` has its status, amount, reason and timestamps;
- `refunded`: the refund was paid out with a recorded provider receipt;
- `removed`: the entry was removed from the competition after it was registered;
- `not_registered`: no place is held, for example after a failed payment.

## Failure and abuse controls

- Client idempotency is unique per user, and a partial unique index prevents multiple
  active/successful charges for one competition even with different keys.
- Per-user, phone and trusted-client-IP velocity limits protect users from repeated
  STK prompts. The maximum fee is bounded by `MPESA_MAX_AMOUNT_MINOR`.
- Callback bytes are retained exactly with a SHA-256 deduplication key. A callback
  that arrives before provider IDs are saved remains unprocessed and is replayed
  after the STK response update.
- Safaricom's documented callback contract does not provide an application-level
  signature. Gamics therefore uses an unguessable callback path, HTTPS/WAF controls,
  strict ID/amount/phone matching, and authenticated STK Query before granting entry.
- Receipt, checkout and idempotency uniqueness make duplicate and out-of-order
  callbacks safe. Unknown or conflicting outcomes enter `review` rather than causing
  another charge.
- A non-zero STK Query result never fails a payment by itself because query responses
  can still be intermediate. Only a matching failure callback is terminal; repeated
  unresolved query results eventually enter `review` for reconciliation.
- Callback journaling remains available when the outbound Daraja client is disabled.
  Pending events receive HTTP 503 after durable storage so Safaricom retries instead
  of treating an unprocessed event as complete.
- A player with any existing entry, including a withdrawn or disqualified entry,
  cannot start another STK Push. Organizer reactivation/refund policy must resolve
  that history before another collection attempt.
- A production operations job must continuously reconcile stale
  `initiating/pending/callback_received/review` rows with STK Query and merchant
  statements. Safaricom also documents [Pull Transactions](https://developer.safaricom.co.ke/apis/PullTransaction).

## Go-live gates

Do not switch to `MPESA_ENVIRONMENT=production` until all of these are complete:

1. Safaricom Go-Live approval, real PayBill/Till and a tested HTTPS callback.
2. A separate least-privilege secret store and secret rotation procedure.
3. WAF rate limits, redacted edge logs and alerts for callback/query failures.
4. A reconciliation worker plus organizer/admin views for payments needing review,
   capacity exceptions and refunds.
5. Sandbox tests for success, user cancel, timeout, insufficient funds, duplicate
   callbacks, callback-before-response, database outage and receipt collision.
6. A documented refund process and accounting reconciliation against the merchant
   statement.
7. A retention schedule for callback phone/payment data, audit records and receipts,
   reviewed against Kenyan tax, consumer-protection and privacy obligations.

Never send Daraja credentials, the passkey or callback token to React Native or the
marketing website.
