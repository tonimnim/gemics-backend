# Push notifications

Every notification lands in the player's inbox. Push-enabled kinds (the notification
catalogue in [mobile-api-requirements.md](mobile-api-requirements.md)) are also sent to
the player's phones through **Firebase Cloud Messaging (FCM)**, Android and iOS alike.

## How a push travels

1. An event is projected into the inbox. For each of the player's live installations
   whose preference allows it, a row is queued in `notification_push_deliveries`.
2. The delivery worker claims up to `NOTIFICATION_PUSH_BATCH` rows and sends them,
   `FCM_CONCURRENCY` at a time, to the FCM HTTP v1 API. At claim time it drops rows
   whose installation was revoked, whose session ended, whose player is no longer
   active or whose preference was turned off.
3. FCM answers each push straight away:

   | FCM answer | Delivery | Installation |
   |---|---|---|
   | Accepted (a message name) | `delivered` | kept |
   | `UNREGISTERED`, `SENDER_ID_MISMATCH`, or `INVALID_ARGUMENT` about the token | `permanent_failure` | revoked: the app must register again |
   | `INVALID_ARGUMENT` about anything else | `permanent_failure` | kept |
   | `QUOTA_EXCEEDED`, `UNAVAILABLE`, `INTERNAL`, `THIRD_PARTY_AUTH_ERROR`, 5xx | `retry` with backoff, honouring `Retry-After` | kept |
   | 401 or 403 for our access token | `retry`; a new token is minted | kept |

   A delivery that still fails after `NOTIFICATION_PUSH_MAX_ATTEMPTS` tries becomes
   `permanent_failure`. The stored error code is `fcm_` plus FCM's code, and never
   holds the token or the response body.

A push carries the inbox item's title and body as the notification, and `data` (all
strings): the item's data plus `notificationId` and `actionUrl`. Pushes of one match
chat share the collapse key `chat:{matchId}`, so a newer one replaces the older on the
device.

## Credentials

The API signs in to FCM as a Firebase **service account**. It signs a JWT with the
account's key and trades it at Google's token endpoint for an access token that lasts
an hour, renewed five minutes before it expires.

1. Firebase console → Project settings → Service accounts → **Generate new private
   key**. This downloads a JSON file. It is a secret: never commit it.
2. Put it in `.env.docker` on one line, base64-encoded:

   ```bash
   echo "FCM_CREDENTIALS_JSON=$(base64 -w0 path/to/key.json)" >> .env.docker
   ```

   Raw JSON on one line works too. `FCM_PROJECT_ID` overrides the file's
   `project_id`.
3. Restart the API. It logs that FCM push delivery is enabled; without credentials it
   logs that delivery is disabled, and the inbox keeps working.

**iOS** needs an APNs authentication key from the Apple Developer account, uploaded in
the Firebase console under Project settings → Cloud Messaging → Apple app
configuration. Until then iOS pushes fail with `THIRD_PARTY_AUTH_ERROR` and are
retried; Android works without it.

## Settings

| Variable | Default | Meaning |
|---|---|---|
| `FCM_CREDENTIALS_JSON` | empty (push off) | The service account JSON, raw or base64 |
| `FCM_PROJECT_ID` | the file's `project_id` | The Firebase project to send through |
| `FCM_API_URL` | `https://fcm.googleapis.com` | The FCM API base, HTTPS when push is on |
| `FCM_TIMEOUT` | `10s` | Per request, to FCM and to the token endpoint |
| `FCM_CONCURRENCY` | `8` | Pushes sent at once (1–64) |
| `NOTIFICATION_PUSH_BATCH` | `100` | Pushes claimed per round (1–100) |
| `NOTIFICATION_PUSH_MAX_ATTEMPTS` | `8` | Tries before a delivery fails for good |

## App side

The app registers the token the FCM SDK returns with `POST /v1/me/push-tokens` after
every sign-in, on each launch and whenever FCM rotates it, and deletes it with
`DELETE /v1/me/push-tokens/{id}` before logging out. Expo tokens were dropped when the
API moved to FCM (migration `000008_fcm_push`); installations register again.
