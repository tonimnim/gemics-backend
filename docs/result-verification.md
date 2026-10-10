# eFootball result verification

> Implementation status: submit and confirm, the confirmation and screenshot
> windows, removal from the tournament, the Gamics review queue, conduct strikes
> and the registration ban are implemented in the Go API and OpenAPI 0.9.0
> (`match-score-reports.paths.yaml`, `result-reviews.paths.yaml`). Last audited:
> 2026-10-09.

Konami does not give Gamics a trusted public match-result API, so a score is a
player's claim until the other entry confirms it, lets it stand, or Gamics
decides. Gamics
never records a score on one side's word alone, and no organizer ever decides a
result: organizers run competitions, and disagreements go to Gamics staff.

## How a result is settled

1. Both entries check in. The match is `in_progress` and has a result deadline
   (`resultDueAt`).
2. After playing, **either entry submits the result** with
   `POST /v1/matches/{matchId}/score-reports`: the home score, the away score,
   an optional game-by-game breakdown and, for a tied knockout match, the
   penalty shootout. There is no screenshot at this stage. Any starter or
   substitute can submit for their entry, once per match; coaches cannot. Once
   one entry has submitted, the other can only confirm or reject.
3. The other entry gets a push ("Confirm the match result") and sees the
   submitted score in its room. It has the **confirmation window** (10 minutes
   by default) to answer with
   `POST /v1/matches/{matchId}/score-reports/confirmation`, and gets a reminder
   3 minutes before it ends ("Confirm the result now").
   - **Confirm:** the result is final at once. The bracket advances and ratings
     are applied.
   - **No answer in time:** the submitted result stands, exactly as if it had
     been confirmed (resolution `confirmation_timeout`, origin `unanswered`).
   - **Reject:** both entries are told "Result rejected" and the **screenshot
     window** opens (10 minutes by default).
4. In the screenshot window each entry sends **one** processed JPEG or PNG
   screenshot of the Full Time screen with
   `POST /v1/matches/{matchId}/score-reports/screenshot`. Nobody enters a new
   score: the submitted result is the only claim. When both screenshots are in,
   the match goes to the **Gamics review queue**, where the screenshot reader
   reads both and says whether they show the submitted result (see
   [screenshot reader](screenshot-reader.md)). Both entries are told the
   result is under review.

## Deadlines and removal

Silence has a cost, because a player who stops responding would otherwise
stall the whole bracket. A player removed from the tournament has their entry
disqualified: they lose the match that triggered it, they play no further
matches, and they receive no placement. Removal is final.

| Situation | Outcome |
|---|---|
| The other entry neither confirmed nor rejected before the confirmation window ended | Nobody is removed: the submitted result stands (`confirmation_timeout`). |
| The result was rejected and exactly one entry sent its screenshot before the screenshot window ended | The entry without a screenshot is removed. The other wins by forfeit (`response_timeout`). |
| The result was rejected and neither entry sent a screenshot | Both entries are removed and the match is cancelled (`response_timeout`). |
| Nobody submitted a result before `resultDueAt` | Both entries are removed and the match is cancelled (`no_result_reported`). |

Every removed player gets a "Removed from tournament" push that names the
reason. Removals and forfeits never change ratings; they follow the same rule
as a no-show.

One case never removes anyone: **a screenshot stuck in Gamics' own pipeline.**
If an entry uploaded a screenshot during the screenshot window and, at the
deadline, it is still processing or failed for a Gamics-side reason
(`verification_unavailable` or `processing_aborted`), the match goes to the
review queue with reason `evidence_unavailable` instead. A Gamics fault must
never disqualify an honest player. Uploads that the player left unfinished, that
were rejected as invalid images, or that were never uploaded (`upload_missing`)
do not count.

A background worker checks the deadlines every 10 seconds. Between a deadline
and the worker's next pass, the room shows the lifecycle `awaiting_resolution`
and offers no action, so the app never offers an action that would fail with
`confirmation_window_closed` or `screenshot_window_closed`. Every deadline is judged
on the database clock, so the worker and a late request agree on which side of
a deadline they are.

### Windows are configurable

Organizers can change the windows per competition in
`rules.matchVerification`, in whole minutes:

| Key | Default | Bounds |
|---|---|---|
| `reportWindowMinutes` (time to confirm or reject) | 10 | 5 to 60 |
| `reminderBeforeDeadlineMinutes` | 3 | at least 1, and less than the confirmation window |
| `responseWindowMinutes` (time to send a screenshot) | 10 | 5 to 60 |

Any other key, including the settings of the retired single-submission flow
such as `confirmationWindowMinutes` and `autoConfirmEnabled`, is refused with
`400 invalid_match_verification_rules`. The windows
are copied onto the match when the result is submitted, so a later rules edit
never moves a live deadline. The room publishes the windows that apply in
`verificationPolicy`.

Draws also leave room to play: `resultWindowMinutes` must be at least the
check-in grace period plus 15 minutes, so a late but valid check-in still has
time to finish the match before both entries could be removed.

## Removal in each format

- **Single elimination.** A removed loser simply loses. If a removed entry had
  already won a slot further on, that slot is voided, and its next opponent
  advances by walkover. When both entries of a match are removed, the next
  match becomes a walkover, or is cancelled if both of its feeders are void.
  A removed semifinal loser never plays for bronze; the bronze match becomes a
  walkover.
- **Double elimination.** A removed entry never drops into the losers bracket;
  its losers-bracket slot is voided and that match becomes a walkover. In the
  grand final, if the winners-bracket champion is removed, the losers-bracket
  champion wins the title by walkover. If the losers-bracket champion is
  removed, no reset match is played. If both finalists are removed, there is no
  champion.
- **Round robin, including groups.** Results the removed entry already played
  stand. Each of its remaining fixtures is settled when its round is released:
  a walkover to the live opponent (3 points and a walkover in the standings), or
  a cancelled match if neither entry is live. Rounds are released across all
  groups of the stage at once, so one match waiting on a review holds back the
  next round in every group.
- **Placements.** Removed entries receive no placement. Round-robin tables rank
  live entries only. Knockout placements rank everyone as before and then leave
  removed entries out, without promoting anyone into the gap; there is no
  champion when the final was cancelled. `GET /v1/competitions/{id}/standings`
  publishes both: each group table is ranked with the placement rule itself,
  with removed entries listed last, unranked, and the final placements appear
  once the last result completes the stage.

## Cancelled competitions

Cancelling a competition cancels every match that has not finished
(`competition_cancelled`), settles any open result verification and closes any
queued review, in the same transaction as the cancellation. Nobody is removed,
no rating changes and no strike is recorded; refunds follow the competition's
cancellation policy. Results, answers, screenshots and review decisions on a cancelled or
completed competition are refused with `409 competition_closed`.

A competition cancelled after it was published stays readable to players: its
detail and bracket return `status: cancelled`, although it leaves the discovery
list. Free entries stay `registered` and `GET /v1/me/registrations` reports
`competitionStatus: cancelled`; new entries are refused with the eligibility issue
`competition_cancelled`.
A draft cancelled before publication was never public and stays `404`.

## The Gamics review queue

Reviews are decided by Gamics platform staff with the `reviewer` or `admin`
role, through `/v1/admin/result-reviews`. Organization roles never reach it.

- **What reviewers see.** The submitted result and who submitted it, who
  rejected it, both screenshots (fetched through `GET /v1/evidence/{id}`) with
  the screenshot reader's reading of each, the verification timeline, the
  active strike count of everyone involved and, for `evidence_unavailable`
  reviews, every upload the players without a screenshot made during the
  screenshot window with its current status (`responseWindowUploads`). The
  stuck screenshot stays listed after processing completes, and reviewers can
  open completed ones through `GET /v1/evidence/{id}` even though they never
  reached a submission.
- **Conflicts of interest.** A staff member who plays in the match, captains
  either entry, or belongs to the competition's organization cannot see the
  review in the queue, cannot open it (`403 result_review_conflict`), cannot
  decide it, and cannot open its screenshots.
- **Order.** The queue is first in, first out. There is no claim or lease; a
  decision sends the `expectedVersion` it was made on and loses with
  `409 review_version_conflict` if someone else decided first.
- **Decisions.**

  | Decision | Match outcome | Ratings |
  |---|---|---|
  | `accept_home` or `accept_away` | The submitted result stands. Only the side that submitted it can be accepted. | applied |
  | `corrected_score` | A score the reviewer sets becomes the result, for example the one both screenshots show | applied |
  | `remove_both` | The match is cancelled and both entries are removed | unchanged |

  Every decision needs a note of 10 to 2000 characters, is audited, and sends
  both entries the same "Review complete" push without the score. Removed
  entries get the "Removed from tournament" push instead.

## Strikes and the registration ban

A reviewer can record a conduct strike against the player the decision proves
wrong: the player who rejected the result when it stands (accepted, or
corrected to the same score), the player who submitted it when it is corrected
to a different score, and either of them on `remove_both`. At most two strikes
per decision. A teammate who only sent a screenshot can never be struck.

Strikes stay active until an admin revokes one with a reason. The strike feed
leaves out strikes against the viewer and strikes from matches the viewer plays
in, captains in or organizes, since a strike carries the decider's note. For
the same reason an admin can never revoke such a strike
(`403 result_review_conflict`). A player with
`RESULT_STRIKE_BAN_THRESHOLD` active strikes (3 by default, 0 disables the ban)
cannot register for new competitions: the eligibility check reports the
blocking issue `conduct_suspended`, and free registration and M-Pesa checkout
both answer `409 competition_ineligible` with `issue.code` `conduct_suspended`. An M-Pesa
payment that completes after the player reached the limit creates a
`withdrawal_pending` entry that never plays, together with a mandatory full
refund (the payment reads `succeeded` with `registrationStatus`
`refund_pending`); that entry holds a place against the competition's capacity until the
refund succeeds. Entries the player already holds are not affected. The player is told when a strike is
recorded and when one is removed.

## Who sees what

| Data | Submitting entry | Other entry | Organizer | Gamics reviewer or admin | Public |
|---|---|---|---|---|---|
| The submitted result | yes, in its room | yes, in its room, to confirm or reject | no | yes, in the review | no |
| Whether the other entry sent its screenshot | yes | yes | no | yes | no |
| Screenshots | the uploader only | never | never | yes | no |
| The confirmed score | once completed | once completed | in the bracket | yes | bracket and history |

A reviewer or admin with a conflict of interest is treated like the player or
organizer they are. Pushes carry only identifiers and the event `kind`, never a
score (see the catalogue in [mobile API handoff](mobile-api-requirements.md)), and
every error a player can receive depends only on their own entry. The response
stored for an idempotent retry holds only the caller's own view.

## Automated decisions later

The decision logic has one entry point that takes the decider as a parameter,
so an automated decider uses the same code path as staff. The screenshot
reader is the first one (see [screenshot reader](screenshot-reader.md)). It
is recorded as a `system` decider with its own reference
(`screenshot-reader:<review id>`), it cannot set a corrected score, and it
never records a strike: a ban always needs a human decision. It decides only
when both players' own screenshots agree with each other and show the submitted
result, and it is off until `VISION_AUTO_DECIDE=true`.

## Evidence is a signal, not proof

Screenshots can be edited. The other entry's confirmation is the main control:
an invented score only survives if the opponent lets it through. After a
rejection, each player's screenshot is checked against the other's. Further
layers worth adding:

- OCR to read and compare scores, with the image kept for human review (built:
  the screenshot reader);
- perceptual hashes and stats fingerprints to detect a reused screenshot
  (built);
- anomaly flags for repeated collusion, impossible schedules and unusual score
  patterns;
- monitored screen sharing or recording for high-value finals.

EXIF metadata and automated image-tampering scores must never be the only basis
for accepting or rejecting a result. See [screenshot pipeline](screenshot-pipeline.md)
for how screenshots are uploaded and verified.

## Reference: rules, transitions and decisions

Comments in the Go API cite the rules (R), transitions (T) and decisions (D)
below by label. They describe what the code does.

### Rules

| Rule | Meaning |
|---|---|
| R1 | Organizers never decide a result. The referee system and the single-submission confirm and dispute flow are retired. |
| R2 | Either entry submits the result once: the score only, no screenshot. Both entries see it; the other entry can only confirm or reject it. |
| R3 | The submission opens the other entry's confirmation window (10 minutes by default), with one reminder before it ends (3 minutes before, by default). |
| R4 | A confirmation settles the result at once. The bracket advances and ratings are applied. |
| R5 | When the confirmation window ends without an answer, the submitted result stands, exactly as if confirmed. Nobody is removed. |
| R6 | A rejection opens the screenshot window. Each entry sends one screenshot and no score; when both are in, the match goes to Gamics review. When the window ends, an entry without a screenshot is removed, unless its screenshot is stuck in Gamics' pipeline. |
| R7 | If nobody submits a result before `resultDueAt`, both entries are removed and the match is cancelled. |
| R8 | Gamics staff decide rejected results (accept the submitted one, `corrected_score`, `remove_both`) and can strike the player the decision proves wrong; enough active strikes block new registrations. Decisions have one entry point, so the screenshot reader decides through it too. |
| R9 | Removal works in single elimination, double elimination and round robin, including groups. Removed entries get no placement, and removals and forfeits never change ratings. |
| R10 | Screenshot processing is hardened against hostile files: JPEG and PNG only, structure checked before decoding, bounded decode memory and time-limited retries. See [screenshot pipeline](screenshot-pipeline.md). |

### Match state and verification phase

A match keeps its `matches.state`; the finer phase lives in
`match_result_verifications`. Before acting on a live match, every planner
checks the locked rows against this table, and a combination that fits no row
is treated as a bug: nothing is written and the transaction rolls back.

| `matches.state` | Verification phase | Meaning |
|---|---|---|
| `in_progress` | no row | Both entries checked in and nobody has submitted a result. |
| `awaiting_confirmation` | `awaiting_confirmation` | One entry submitted the result; the other entry's confirmation window is running. |
| `disputed` | `awaiting_screenshots` | The other entry rejected the result (`rejected_by`); the screenshot window is running. At most one entry has sent its screenshot. |
| `disputed` | `in_review` | The match is in the Gamics review queue: both entries sent screenshots (`reports_differ`), or an entry's screenshot is stuck (`evidence_unavailable`), in which case zero or one entry has sent one. |
| `completed`, `forfeit` or `cancelled` | `resolved`, or no row | Terminal. There is no row when nobody submitted a result (R7) or when the competition was cancelled before one. |

A resolved row records how it ended: `agreed`, `confirmation_timeout`,
`response_timeout`, `platform_review` or `competition_cancelled`. In
`match_result_reports` the submitted result is the one `initial` row; each
screenshot after a rejection is a `final` row without a score.

### Transitions

X is the entry that submitted the result and Y the other entry. T2 to T17 run
under the competition gate and the match lock and judge deadlines on the
database clock (D26); T1 is the existing check-in, which does neither. At the
exact deadline instant the worker acts and a request is refused. Every
transition except T3 raises the match version. T2 to T14 and T16 never run on a
cancelled or completed competition (D20).

| # | Trigger | From to | Effect |
|---|---|---|---|
| T1 | The second entry checks in | `ready` to `in_progress` | The match can be reported. Check-in itself is unchanged. |
| T2 | X submits the result, before `resultDueAt` | `in_progress` to `awaiting_confirmation` (`awaiting_confirmation`) | Result stored; the windows are copied from the rules onto a new verification row; Y gets `result.report_received`. |
| T3 | Worker: the reminder time is reached, the confirmation window is still open and no reminder was sent | no change | Y gets `result.report_reminder`, once. The match version does not change. |
| T4 | Y confirms, inside the confirmation window | `awaiting_confirmation` to `completed` (`played`) | Canonical result (`agreed_reports`, submitted by X and confirmed by Y); verification `agreed`; progression; ratings; `match.result_confirmed`. |
| T5 | Y rejects, inside the confirmation window | `awaiting_confirmation` to `disputed` (`awaiting_screenshots`) | `rejected_by` recorded; the screenshot window starts; both entries get `result.mismatch`. |
| T6 | Worker: the confirmation window ended without an answer (R5) | `awaiting_confirmation` to `completed` (`played`) | Canonical result (`unanswered`, submitted by X, no confirmer); verification `confirmation_timeout`; progression; ratings; `match.result_confirmed`. Nobody is removed. |
| T8 | A screenshot while the other entry hasn't sent one | `disputed` to `disputed` (`awaiting_screenshots`) | Screenshot stored. No push. |
| T9 | A screenshot after the other entry sent one | `disputed` to `disputed` (`in_review`) | Review queued with reason `reports_differ`; both entries get `result.under_review`. |
| T10 | Worker: the screenshot window ended with exactly one screenshot, and the other entry is not evidence-blocked (R6) | `disputed` to `forfeit` (`response_timeout`) | The entry without a screenshot is removed and the other wins; verification `response_timeout`; no ratings. |
| T11 | Worker: the screenshot window ended with none, and neither entry is evidence-blocked (R6) | `disputed` to `cancelled` (`response_timeout`) | Both entries are removed; verification `response_timeout`; no ratings. |
| T12 | Worker: `resultDueAt` passed with no result (R7) | `in_progress` to `cancelled` (`no_result_reported`) | Both entries are removed; no verification row is created; no ratings. |
| T13 | Review decision accepting the submitted result, or `corrected_score` | `disputed` (`in_review`) to `completed` (`platform_review`) | Canonical result (`platform_review`); verification `platform_review`; review `decided`; progression; ratings; optional strikes; `result.review_decided` instead of `match.result_confirmed`. |
| T14 | Review decision `remove_both` | `disputed` (`in_review`) to `cancelled` (`platform_review`) | Both entries are removed; verification `platform_review`; review `decided`; no ratings; optional strikes; `result.review_decided`. |
| T15 | Any match reaching a terminal state | the next matches of the stage | Progression. Knockout voids a removed entry's slots and gives walkovers; round robin settles its fixtures when their round is released. See [Removal in each format](#removal-in-each-format). |
| T16 | Worker: the screenshot window ended and an entry's screenshot is still processing or failed for a Gamics-side reason | `disputed` to `disputed` (`in_review`) | Review queued with reason `evidence_unavailable`; both entries get `result.under_review`. Nobody is removed. |
| T17 | The organizer cancels the competition | every `pending`, `ready`, `in_progress`, `awaiting_confirmation` or `disputed` match to `cancelled` (`competition_cancelled`) | In the cancel transaction: open verifications resolve as `competition_cancelled` and queued reviews close. No removal, progression, ratings, strike or per-match push. |

T7 (agreement after a response) belonged to the blind dual-report flow and no
longer exists: after a rejection nobody enters a new score.

### Decisions

| Decision | What the code does | Why |
|---|---|---|
| D6 | `games` is optional. Without it the server records one aggregate game. Two results are equal on the home score, the away score and the tiebreak only. | Game order or detail must never decide whether a result stands. |
| D13 | A review has exactly four decisions. A liar on the losing side is struck, not removed. An automated decider can neither set a corrected score nor record a strike. | An unreviewed automated decision must never lead to a ban. |
| D14 | Strikes come only from staff review decisions, against the player the decision proves wrong (the rejecter or the submitter), at most two per decision, and stay active until an admin revokes one. `RESULT_STRIKE_BAN_THRESHOLD` active strikes block free registration and M-Pesa checkout; an M-Pesa payment that completes after the limit gets a withdrawal-pending entry that never plays and a mandatory full refund. Existing entries are not affected. | Strikes are for liars, not for typo fixers or the accepted side, and paying must not bypass the ban. |
| D17 | `matchVerification` accepts only the three window keys on write. Retired single-submission keys are refused by name, and stored rules that still carry them are read without them. | A clear contract, while older stored rules keep working. |
| D18 | A draw needs `resultWindowMinutes` of at least the check-in grace period plus 15 minutes. | R7 removes both entries, so a late but valid check-in must still leave time to play. |
| D20 | Cancelling a competition cancels its live matches (T17) without removals, ratings or strikes. Reports, review decisions and the worker refuse or skip cancelled and completed competitions; no other competition status is gated. | Nobody is removed, rated or banned over an event that did not finish. Check-in has no status gate, so gating reports on other statuses could strand live matches. |
| D26 | Deadlines are judged on `statement_timestamp()` read after the competition gate is held, not on the transaction start time. | A request that waited on the gate can neither slip in after a deadline nor backdate a new one. |
| D27 | Staff who play in the match, captain an entry or belong to the competition's organization cannot list, open or decide its review, read its screenshots, see its strikes in the strike feed or revoke them. | Organizers never decide results (R1), and a strike carries the decider's note. |
| D28 | Results, answers, screenshots, withdrawals, paid withdrawals and organizer status changes run an unlocked membership or ownership probe before they take the competition gate. | A caller with no stake in the competition cannot make its finalization queue behind spam requests. |
