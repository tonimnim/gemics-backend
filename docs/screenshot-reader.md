# Screenshot reader: architecture and contract

Status: the Go side is built (queue, worker, checks, automatic decisions,
learned team names, training feed, staff view). The Python reader is built
separately on the team's EasyOCR fork (https://github.com/tonimnim/EasyOCR) and
plugs in through `POST /v1/read` below.

## What it reads

The eFootball "Full Time" screen. Three real samples show the layout and how it
varies:

| | Sample 1 | Sample 2 | Sample 3 |
|---|---|---|---|
| Banner | `Argentina 3 ⓔ 3 shinegum` | `Mugz FC 2 ⓔ 1 SQUAD 0` | `mzee mzima 3 ⓔ 0 CR Flamengo` |
| Size | 826×371 phone crop | 960×540, console style | 826×371 phone crop |
| Shots row label | `Shots` | `Total Shots` | `Shots` |
| Extras | | coach row `Ruben Amorim \| R. Martínez`, `Highlights` button | |

The banner holds the left team name, the left score, the eFootball logo, the
right score and the right team name. A `Full Time` caption sits under it, then a
13-row table with the label in the middle and each side's value on the same row:
Possession (%), Shots / Total Shots, Shots on Target, Fouls, Offsides, Corner
Kicks, Free Kicks, Passes, Successful Passes, Crosses, Interceptions, Tackles,
Saves. Team names are the players' eFootball team names (club or national team
names they chose), not Tonits usernames. Left and right follow the game room,
not Tonits' home and away.

## Architecture

```
player app ──upload──▶ R2/MinIO  (existing presigned upload + integrity worker)
     │
     └─final score report with 1–3 screenshots──▶ Go API
                                                   │ queues a reading per screenshot
                                                   ▼
                                     screenshot_readings (Postgres)
                                                   │ reader worker (Go, in the API)
                                                   │ downloads the bytes from storage
                                                   ▼
                              POST /v1/read ──▶ Python reader (EasyOCR fork, internal only)
                                                   │ JSON reading
                                                   ▼
                     Go stores it, maps left/right to home/away by team name,
                     shows it to staff, and may decide the review automatically
```

- **Go pushes, Python stays stateless.** The reader gets image bytes and returns
  JSON. It needs no database or storage credentials, so it scales by adding
  replicas behind one internal URL. Go owns the queue, retries and decisions.
- **Internal network only.** The reader has no public port. Every request
  carries `Authorization: Bearer $VISION_TOKEN`.
- **Failure is safe.** If the reader is down or unsure, the review stays with
  staff exactly as today.

## Endpoints the Python reader implements

### `POST /v1/read`

Request: the raw image as the body.

| Header | Value |
|---|---|
| `Content-Type` | `image/jpeg` or `image/png` |
| `Authorization` | `Bearer <VISION_TOKEN>` |
| `X-Request-ID` | Go's request id, for joint logs |
| `X-Evidence-ID` | the upload's UUID, for logs only |

Limits: 25 MiB, one image per request. Go times out after `VISION_TIMEOUT`
(default 30 s) and retries with backoff.

Response `200` (sample 2):

```json
{
  "screen": "match_result",
  "fullTime": true,
  "left":  { "team": "Mugz FC", "score": 2, "confidence": 0.98 },
  "right": { "team": "SQUAD 0", "score": 1, "confidence": 0.97 },
  "penalties": null,
  "managers": { "left": "Ruben Amorim", "right": "R. Martínez" },
  "stats": {
    "possession": [64, 36], "shots": [16, 1], "shotsOnTarget": [12, 1],
    "fouls": [0, 0], "offsides": [0, 0], "cornerKicks": [3, 0], "freeKicks": [0, 0],
    "passes": [156, 96], "successfulPasses": [122, 66], "crosses": [4, 0],
    "interceptions": [28, 24], "tackles": [5, 5], "saves": [0, 7]
  },
  "checks": {
    "possessionSumsTo100": true, "goalsWithinShotsOnTarget": true,
    "goalsPlusSavesWithinShotsOnTarget": true, "successfulWithinPasses": true
  },
  "confidence": 0.96,
  "imageHash": "c3d1f0e0b8a4c2e1",
  "model": { "engine": "easyocr", "version": "1.7.2+tonits-efootball-3" },
  "durationMs": 840
}
```

| Field | Meaning |
|---|---|
| `screen` | `match_result`, or `unknown` for any other image (for example a profile screenshot). With `unknown`, `left`, `right` and `stats` are `null`. |
| `left` / `right` | Team name as printed (keep case and spaces) and the score, 0–99, each with its own confidence. |
| `penalties` | `{ "left": 4, "right": 3 }` when the screen shows a shoot-out, otherwise `null`. |
| `managers` | Optional coach row; `null` when absent (samples 1 and 3). |
| `stats` | Fixed keys whatever the label says (`Total Shots` → `shots`). Leave out rows you couldn't read rather than guessing. |
| `checks` | Plausibility rules (see "Edited and AI-generated screenshots"); a failed check lowers confidence and is shown to staff. |
| `confidence` | 0–1. Your belief that **both scores and both team names** are right. Go only auto-decides above `VISION_AUTO_MIN_CONFIDENCE` (default 0.9). |
| `imageHash` | A 64-bit perceptual hash (pHash, hex). Go uses it to catch one screenshot reused for another match. |
| `model` | Engine and model version. Go stores it with every reading. |

Errors: `415` wrong type, `413` too large, `422` undecodable image, `401` bad
token, `503` model not loaded. Body: `{ "error": "code", "message": "…" }`. Go
retries `5xx` and network errors, and does not retry `4xx`.

### `GET /healthz`

`{ "ok": true, "model": { "engine": "easyocr", "version": "…" } }`. Used by the
container healthcheck and by Go before it starts sending work.

## Endpoints the Go API adds

| Endpoint | Who | Purpose |
|---|---|---|
| `GET /v1/admin/result-reviews/{id}` (extended) | reviewers | Each screenshot gains a `reading`: what it says, its confidence, and, when the team names can be matched to the players, the score as home/away and which claim it supports. |
| `POST /v1/admin/screenshot-readings/{evidenceId}/retry` | reviewers | Read a screenshot again while the review is open, for example after a model upgrade. Audited; refused once decided. |
| `GET /v1/internal/vision/training-examples?cursor=` | the training job, `VISION_TRAINING_TOKEN` | Labelled real screenshots for training, in screen terms, from staff decisions only (see step 6 below). This is how real data reaches the trainer without anyone copying files by hand. |

## Connecting your service

1. **Run it where the API can reach it.** `compose.yaml` has a `vision`
   service stub behind the `vision` profile. It builds the tonitsOCR repository
   next to this one (`VISION_BUILD_CONTEXT`, default `../tonitsOCR/EasyOCR`)
   with `VISION_DOCKERFILE` (default `tonits_vision/Dockerfile`), or uses a
   prebuilt `VISION_IMAGE`. It publishes no port and is hardened like the
   other services: read-only, no capabilities, 2 GB, `/tmp` writable. The
   container must run the HTTP server on port 8000, check `VISION_TOKEN`, and
   answer `GET /healthz` with `200` once the models are loaded. Bake the models
   into the image (`VISION_DOWNLOAD_MODELS=0`).

   ```
   docker compose --env-file .env.docker --profile vision up -d --build
   ```

   It passes the reader's own settings: `VISION_RECOGNIZER`,
   `VISION_MODEL_DIR`, `VISION_USER_NETWORK_DIR`, `VISION_MAX_QUEUE` and, for
   an optional LLM second opinion, `VISION_FALLBACK_API_KEY` /
   `VISION_FALLBACK_MODEL` / `VISION_FALLBACK_BELOW`.
2. **Point the API at it** in `.env.docker` (or the production environment):

   ```
   VISION_URL=http://vision:8000
   VISION_TOKEN=<the same 32+ character secret your service checks>
   ```

   Plain `http://` is accepted only for an internal host (a service name or a
   private address); anything else must be `https://`.

   Restart the API. Its log says `screenshot reader enabled`.
3. **Check it by hand** from inside the network:

   ```
   curl -s http://vision:8000/healthz
   curl -s -X POST http://vision:8000/v1/read \
     -H "Authorization: Bearer $VISION_TOKEN" \
     -H "Content-Type: image/png" -H "X-Evidence-ID: test" \
     --data-binary @fulltime.png
   ```

4. **Watch it work.** When the two players' reports disagree and they upload
   screenshots, each one gets a row in `screenshot_readings`. Within seconds
   the API sends it to `/v1/read`, and reviewers see what it read on the review
   page. A reading can be repeated with "Read again" on that page, or
   `POST /v1/admin/screenshot-readings/{evidenceId}/retry`.
5. **Turn on automatic decisions** only after the reader is accurate on real
   screenshots: `VISION_AUTO_DECIDE=true`. The threshold is
   `VISION_AUTO_MIN_CONFIDENCE` (default 0.9).
6. **Pull training data** for the model with its own token, which is never
   sent to the reader:

   ```
   curl -s "https://<api>/v1/internal/vision/training-examples?limit=100" \
     -H "Authorization: Bearer $VISION_TRAINING_TOKEN"
   ```

   Response: `{ "items": [...], "nextCursor": "..." }`. Each item has a
   15-minute `downloadUrl`, `contentType`, a `label` in screen terms
   (`screen`, `left`/`right` team and score, `penalties`), `source`
   (`staff_decision`), `decidedAt`, what the reader returned, and
   `readerCorrect`. Only trustworthy labels are exported: best-of-one matches
   decided by staff, where both players' screenshots agree and none carries a
   blocking flag (hints such as `similar_image` or the saves rule are allowed).
   A forged screenshot never becomes a label, and nor do the reader's own
   automatic decisions. Set `VISION_TRAINING_CIDRS` and block `/v1/internal/`
   at the public gateway as well.

How Go treats your answers:

| Your answer | What Go does |
|---|---|
| `200` with a valid body | Stores the reading, re-checks the numbers, maps the teams, flags reuse, then may decide |
| `200` with a malformed body | Fails the reading for good (staff decide) |
| `415`, `413`, `422` | Fails the reading for good |
| `503 {"error":"busy"}`, `429` | Retries in 10–20 s; never counts as a failure |
| `401`, other `5xx`, timeout, connection error | The reader is unavailable: retries with backoff (30 s doubling to 30 min) for about a day, never blaming the screenshot |

Before claiming any work the API checks `GET /healthz`; while it fails,
nothing is claimed. Each claim takes a fresh token and a lease of
`VISION_TIMEOUT` + 1 minute, and every write names the token, so a crashed
replica's work is picked up again and a late answer from an expired claim is
dropped.

## Data the Go side keeps

`screenshot_readings`, one row per screenshot bound to a final report:

- `evidence_id` (PK), `match_id`, `report_id`
- `status`: `queued`, `read` or `failed`; `attempts`; `available_at`; `last_error`
- `screen`, `confidence`, `full_time`, `left_team`, `left_score`, `right_team`,
  `right_score`, `left_penalties`, `right_penalties`, `stats` (jsonb)
- `image_hash`, `stats_fingerprint`, `reused_match_id`, `reuse_kind`
- `engine`, `model_version`, `read_at`

`player_team_names` holds the eFootball team names learned for each player
(`user_id`, `name_key`, `name`, `confirmations`). It is deleted with the
player's account.

## Mapping left/right to home/away

eFootball team names aren't Tonits usernames, and a player can set every name
Tonits holds about them (display name, username, in-game name). A loser who
renamed themselves after the winner's team could otherwise flip the mapping and
win automatically. So:

1. **Only learned team names map a side.** Go learns them from **staff**
   decisions, and only when both players' screenshots carry exactly the same
   names (no OCR tolerance), no reading carries a blocking flag (hints are
   allowed), and the decided result
   fits the screenshot one way round. The reader never learns from its own
   automatic decisions.
2. **Both names must map**, the left to one player and the right to the other,
   and neither to both. One OCR slip is tolerated in names of six or more
   characters; substrings and extra words never match. Two players whose
   learned names are that close make the mapping unknown.
3. **A new player's first dispute goes to staff.** That decision teaches their
   team name, and later screenshots map themselves.
4. **A draw without penalties needs no mapping:** the score reads the same
   either way. A shoot-out needs it, and is always checked by staff until a
   real shoot-out screenshot confirms the display.
5. **Two screenshots are compared after mapping.** If the game ever showed
   each player their own team on the left, the two genuine screenshots would be
   mirror images; Go accepts a pair that agrees as read or mirrored, as long as
   both land on the same home/away score.

## Edited and AI-generated screenshots

No software can reliably tell whether a screenshot was edited or AI-generated.
Forgery detectors are an arms race: they miss good fakes and flag real
screenshots that were simply recompressed by WhatsApp or the phone. So Tonits
never trusts a single screenshot. It relies on things a forger can't control:

1. **Two independent witnesses.** Screenshots only matter when the blind
   reports disagree, and then both players upload their own. Both phones show
   the same screen of the same match, so genuine screenshots agree on
   **everything**: both team names, the score and all 26 stat values. A forger
   has to fake their image while the opponent uploads the real one. The two then
   disagree, and the reader shows staff exactly which numbers differ. This holds
   however good AI image generation gets.
2. **The stats table is a checksum.** A goal is a shot on target, so for each
   side `goals ≤ shots on target`. Also shots on target ≤ shots, successful
   passes ≤ passes, and possession adds up to 100. Editing only the score, the
   common forgery, usually breaks one of these. An own goal is the rare honest
   exception (a goal without a shot), which is why a failed check is a flag for
   staff, never a rejection on its own. `goals + opponent's saves ≤ shots on
   target` looks like it should hold too, but a genuine console screen breaks
   it (FC Barcelona 4 goals + 7 saves against 10 on target), so it is only a
   hint for staff.
3. **Identity.** The team names must belong to the two players in this match
   (see mapping below). A real screenshot from someone else's match fails.
4. **Reuse.** The same 26-number stat table in two matches is reuse; only
   the later upload is flagged. Low-information tables (fewer than 20 passes a
   side) are skipped, since unrelated short matches can share them. An
   identical `imageHash` is only a hint for staff: on this fixed layout two
   different matches can hash alike. Both lookups are exact and indexed.
5. **Time.** Final reports are only accepted inside the response window, which
   leaves little time to fabricate.
6. **Deterrence.** A rejected claim earns a strike, and repeated strikes block
   entry. That already exists.

The reader returns these as flags (`checks`, plus Go's identity and reuse
checks). Image forensics, such as error-level analysis or an AI-image
detector, may be added later as one more flag for staff. It should never be a
reason to decide, because its false positives would punish honest players.

**The weak case** is a dispute where only one player provides evidence. Go never
auto-decides it. For high-value matches (finals, big prizes), require stronger
capture:

- **Android:** an in-app recorder using the screen-capture API (MediaProjection)
  records the end of the match and uploads it straight away, so the file never
  passes through the gallery.
- **iOS:** the same through a ReplayKit broadcast extension.

A recording captured live by the Tonits app is the only evidence that is close
to unforgeable.

## When Go enters the result itself

The existing `system` decider path does this. It can accept one player's claim,
but it can never invent a score or give strikes. All of these must hold:

- every screenshot attached to the review has been read;
- at least one screenshot **from each player** reads `match_result`, with
  `fullTime: true`, above the confidence threshold;
- the two players' screenshots agree on **every** field read: team names, score,
  penalties and each stat, not just the score;
- every plausibility check passes (the saves hint aside), and shots on target
  was read;
- both team names map to the two players through learned names;
- no screenshot's stat table was used for another match;
- the match is best of one, there was no shoot-out, and the agreed score
  equals exactly one player's claim.

In that case the player whose claim is contradicted is contradicted by their own
screenshot. Otherwise staff decide as today, with each reading, its flags and
the fields that differ shown beside the screenshots. Auto-decisions are off
until `VISION_AUTO_DECIDE=true`.

## Scale and failure

**Load.** Screenshots exist only for disputes (blind reports that disagree),
1–3 per player. Even at 100,000 matches a day with 10% disputed, that is about
30,000 images a day: 0.35 a second on average, a few a second at peaks. A
reader process takes 1–4 s per image on CPU, so peak load needs roughly 5–15
reader processes. That's ordinary horizontal scaling.

**Off the critical path.** Nothing in match play waits for the reader.
Disputes keep their deadlines and go to staff exactly as before; the reader
only adds information and, when safe, an earlier decision. If it is down for
a day, the only cost is that staff decide without readings.

**Scaling each part:**

| Part | How it scales | Limit to watch |
|---|---|---|
| Reader (Python) | Replicas behind one internal URL; one image at a time per process; `VISION_MAX_QUEUE` sheds load with 503 `busy` | CPU; about 2 GB memory per process for the models |
| Reading worker (Go) | Runs in API processes with `VISION_WORKER=true`. In production run it on one or two dedicated replicas (same image, no public traffic) and turn it off on public replicas, so reading load follows the reader's size, not API traffic | Total in flight = worker replicas × `VISION_CONCURRENCY`; keep it at or below reader capacity |
| Queue (Postgres) | `FOR UPDATE SKIP LOCKED` claims on a partial index of queued rows; batches the size of free slots | Tens of thousands a day is small for Postgres |
| Reuse checks | Exact, indexed matches on the stat fingerprint and image hash | Constant cost as history grows |
| Staff view | A handful of indexed queries per review | None |
| Training feed | Cursor-paged, 100 per page, signed URLs | Run training off-peak |

**What fails safe:**
- reader down: nothing claimed; screenshots wait up to a day, then staff;
- reader overloaded: 503 `busy`, retried in seconds;
- a replica dies mid-read: the lease expires and another replica reads it;
- a late answer from an expired claim: dropped;
- a decision lost to a restart: the next sweep re-evaluates the review;
- a bad image: failed for good, staff decide;
- reading turned off: waiting screenshots aren't shown as "reading", and
  ones older than 14 days are closed when it's turned back on.

**Before turning on automatic decisions:** run the reader's `evaluate.py` on a
held-out set of real screenshots and set `VISION_AUTO_MIN_CONFIDENCE` to where
exact-score accuracy is at least 99.5%. Record that threshold with the model
version.

## OCR or an "intelligent" model?

Reading this screen is an OCR job: the layout is fixed and the text is clean
and high-contrast. A fine-tuned OCR model plus layout rules is the right core:

- cheap on a CPU;
- deterministic, so the same image always gives the same answer;
- auditable, since every number traces back to a box on the image;
- after fine-tuning on the eFootball font, it should read clean screenshots
  almost perfectly.

The "intelligence" you need is narrower:

| Need | Best tool |
|---|---|
| Is this the Full Time screen, and which UI version? | A tiny image classifier (e.g. MobileNet), trained on a few hundred screenshots |
| Read names, scores, stats | Fine-tuned EasyOCR plus layout rules |
| Is it consistent and plausible? | Deterministic rules (above), not a model |
| Odd images (photo of a screen, heavy crop, new season UI) | Optional vision-LLM second opinion, used only when OCR confidence is low |
| Is it forged? | No model is trustworthy; use the cross-checks above and humans |

A large vision-LLM (Claude, GPT) should never be the main reader. It can
misread digits, costs money per image and gives different answers on reruns. It
is useful as a second, independent reader on hard images: if it and the OCR
agree, confidence is high; if not, staff decide. Konami changes the UI between
seasons, so keep the classifier and recogniser retrainable from
`training-examples`, and gate every model release on the real-screenshot test
set.

## Settings (Go)

| Variable | Default | Meaning |
|---|---|---|
| `VISION_URL` | empty (off) | Base URL of the reader, for example `http://vision:8000` |
| `VISION_TOKEN` | — | Shared bearer token, 32+ characters |
| `VISION_TIMEOUT` | `30s` | Per request |
| `VISION_CONCURRENCY` | `2` | Readings in flight per API replica |
| `VISION_AUTO_DECIDE` | `false` | Allow automatic decisions |
| `VISION_AUTO_MIN_CONFIDENCE` | `0.9` | Threshold for automatic decisions (calibrate first) |
| `VISION_WORKER` | `true` | Run the reading worker in this process |
| `VISION_TRAINING_TOKEN` | — | Separate 32+ character token for the training feed |
| `VISION_TRAINING_CIDRS` | — | Client networks allowed to call the training feed |

## Suggested Python layout (for the team building it)

- **Detection:** EasyOCR's CRAFT detector as is.
- **Recognition:** fine-tune EasyOCR's English recognizer on the eFootball font
  using the fork's `trainer/`. Start from synthetic screens; real screenshots
  from `training-examples` replace them over time.
- **Layout:** anchor on the 13 stat labels, which never change apart from
  `Shots`/`Total Shots`. The table's centre gives the left and right columns.
  The two digits nearest the centre above `Full Time` are the score, and the
  text on the same banner line either side of them is the team names.
- **Checks:** goals ≤ shots on target, goals + opponent's saves ≤ shots on
  target, shots on target ≤ shots, successful passes ≤ passes, possession adds
  up to 100, scores 0–99. These drive `confidence`.
- **Evaluation:** report exact-score accuracy and team-name accuracy on a held-out
  set of real screenshots for every model version, and ship a new version only
  when it beats the last.
