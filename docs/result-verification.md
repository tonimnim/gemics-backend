# eFootball result verification

> Implementation status: blind dual score reports, the report and response
> windows, removal from the tournament, the Gamics review queue, conduct strikes
> and the registration ban are implemented in the Go API and OpenAPI 0.9.0
> (`match-score-reports.paths.yaml`, `result-reviews.paths.yaml`). Last audited:
> 2026-09-29.

Konami does not give Gamics a trusted public match-result API, so a score is a
player's claim until the other entry agrees with it or Gamics decides. Gamics
never records a score on one side's word alone, and no organizer ever decides a
result: organizers run competitions, and disagreements go to Gamics staff.

## How a result is settled

1. Both entries check in. The match is `in_progress` and has a result deadline
   (`resultDueAt`).
2. After playing, each entry reports its score with
   `POST /v1/matches/{matchId}/score-reports`: the home score, the away score,
   an optional game-by-game breakdown and, for a tied knockout match, the
   penalty shootout. There is no screenshot at this stage. Any starter or
   substitute can report for their entry, once; coaches cannot.
3. The report is blind. The reporter never sees the other entry's claim, and
   the other entry never sees theirs. The room only says whether the opponent
   has reported.
4. The first report starts the other entry's **report window** (10 minutes by
   default). That entry gets a push straight away ("Your opponent reported the
   score") and a reminder 3 minutes before the window ends ("Report your score
   now").
5. When the second report arrives, the server compares the two claims on the
   totals and the tiebreak. The games breakdown never decides agreement, so two
   honest players cannot disagree on how they split the score.
   - **Same score:** the result is confirmed at once. The bracket advances and
     ratings are applied.
   - **Different score:** both entries are told "Scores don't match", without
     either score, and the **response window** opens (10 minutes by default).
6. In the response window each entry may respond once, with
   `POST /v1/matches/{matchId}/score-reports/final`: its final score plus one
   to three processed JPEG or PNG screenshots that prove it. The claims are
   compared again after every response. An entry's current claim is its final
   report if it sent one, otherwise its initial report.
   - A response that now agrees with the other entry's current claim confirms
     the result at once.
   - A response that still differs waits for the other entry.
   - When both entries have responded and still differ, the match goes to the
     **Gamics review queue**. Both entries are told the result is under review.

## Deadlines and removal

Silence has a cost, because a player who stops responding would otherwise
stall the whole bracket. A player removed from the tournament has their entry
disqualified: they lose the match that triggered it, they play no further
matches, and they receive no placement. Removal is final.

| Situation | Outcome |
|---|---|
| One entry reported and the other did not report before the report window ended | The silent entry is removed. The reporter wins by forfeit (`report_timeout`). |
| Scores differed and exactly one entry responded before the response window ended | The non-responder is removed. The responder wins by forfeit (`response_timeout`). |
| Scores differed and neither entry responded | Both entries are removed and the match is cancelled (`response_timeout`). |
| Nobody reported before `resultDueAt` | Both entries are removed and the match is cancelled (`no_result_reported`). |

Every removed player gets a "Removed from tournament" push that names the
reason. Removals and forfeits never change ratings; they follow the same rule
as a no-show.

One case never removes anyone: **a screenshot stuck in Gamics' own pipeline.**
If a non-responder uploaded a screenshot during the response window and, at the
deadline, it is still processing or failed for a Gamics-side reason
(`verification_unavailable` or `processing_aborted`), the match goes to the
review queue with reason `evidence_unavailable` instead. A Gamics fault must
never disqualify an honest player. Uploads that the player left unfinished, that
were rejected as invalid images, or that were never uploaded (`upload_missing`)
do not count.

A background worker checks the deadlines every 10 seconds. Between a deadline
and the worker's next pass, the room shows the lifecycle `awaiting_resolution`
and offers no action, so the app never offers an action that would fail with
`report_window_closed` or `response_window_closed`. Every deadline is judged
on the database clock, so the worker and a late request agree on which side of
a deadline they are.

### Windows are configurable

Organizers can change the windows per competition in
`rules.matchVerification`, in whole minutes:

| Key | Default | Bounds |
|---|---|---|
| `reportWindowMinutes` | 10 | 5 to 60 |
| `reminderBeforeDeadlineMinutes` | 3 | at least 1, and less than the report window |
| `responseWindowMinutes` | 10 | 5 to 60 |

Any other key, including the settings of the retired single-submission flow
such as `confirmationWindowMinutes` and `autoConfirmEnabled`, is refused with
`400 invalid_match_verification_rules`. The windows
are copied onto the match when the first report arrives, so a later rules edit
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
cancellation policy. Reports and review decisions on a cancelled or completed
competition are refused with `409 competition_closed`.

A competition cancelled after it was published stays readable to players: its
detail and bracket return `status: cancelled`, although it leaves the discovery
list. Free entries stay `registered` and `GET /v1/me/registrations` reports
`competitionStatus: cancelled`; new entries are refused with the eligibility issue
`competition_cancelled`.
A draft cancelled before publication was never public and stays `404`.

## The Gamics review queue

Reviews are decided by Gamics platform staff with the `reviewer` or `admin`
role, through `/v1/admin/result-reviews`. Organization roles never reach it.

- **What reviewers see.** Both entries' initial and final claims, who reported
  each one, the final reports' screenshots (fetched through
  `GET /v1/evidence/{id}`), the verification timeline, each reporter's active
  strike count and, for `evidence_unavailable` reviews, every upload the
  non-responding players made during the response window with its current
  status (`responseWindowUploads`). The stuck screenshot stays listed after
  processing completes, and reviewers can open completed ones through
  `GET /v1/evidence/{id}` even though they never reached a report.
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
  | `accept_home` | The home entry's current claim becomes the result | applied |
  | `accept_away` | The away entry's current claim becomes the result | applied |
  | `corrected_score` | A score the reviewer sets becomes the result | applied |
  | `remove_both` | The match is cancelled and both entries are removed | unchanged |

  Every decision needs a note of 10 to 2000 characters, is audited, and sends
  both entries the same "Review complete" push without the score. Removed
  entries get the "Removed from tournament" push instead.

## Strikes and the registration ban

A reviewer can record a conduct strike against the reporter of a claim that the
decision rejected: the away reporter when accepting home, the home reporter when
accepting away, either reporter whose claim differs from a corrected score, and
either reporter on `remove_both`. At most two strikes per decision. A player
whose claim matched the accepted result can never be struck, and neither can a
player whose initial report was replaced by their entry's final report.

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

| Data | Reporting entry | Opponent entry | Organizer | Gamics reviewer or admin | Public |
|---|---|---|---|---|---|
| An entry's own initial and final score | yes, in its room | never | no | yes, in the review | no |
| Whether the opponent reported or responded | yes | yes | no | yes | no |
| Screenshots | the uploader only | never | never | yes | no |
| The confirmed score | once completed | once completed | in the bracket | yes | bracket and history |

A reviewer or admin with a conflict of interest is treated like the player or
organizer they are. Pushes carry only identifiers and the event `kind`, never a
score (see the catalogue in [mobile API handoff](mobile-api-requirements.md)), and
every error a player can receive depends only on their own entry. The response
stored for an idempotent retry holds only the caller's own view.

## Automated decisions later

The decision logic has one entry point that takes the decider as a parameter,
so an automated decider can be added later without a second code path. It is
not built. When it is, it will be recorded as a `system` decider with its own
reference, it will not be able to set a corrected score, and it will never
record a strike: a ban always needs a human decision.

## Evidence is a signal, not proof

Screenshots can be edited. The blind dual report is the main control: an
invented score only survives if the opponent invents the same one. Further
layers worth adding:

- OCR to prefill and compare scores, with the image kept for human review;
- perceptual hashes to detect a reused screenshot;
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
| R2 | Each entry reports its score blind: the score only, no screenshot. No response, error or push reveals the other entry's claim. |
| R3 | The first report opens the other entry's report window (10 minutes by default), with one reminder before it ends (3 minutes before, by default). |
| R4 | Two reports that agree confirm the result at once. The bracket advances and ratings are applied. |
| R5 | The silent entry is removed from the tournament when the report window ends, and the reporter wins by forfeit. |
| R6 | Differing reports open the response window. Each entry may respond once with a final score and one to three screenshots, and the claims are compared again after every response. Two responses that still differ go to Gamics review. When the window ends, a non-responder is removed, unless its screenshot is stuck in Gamics' pipeline. |
| R7 | If nobody reports before `resultDueAt`, both entries are removed and the match is cancelled. |
| R8 | Gamics staff decide disputed results (`accept_home`, `accept_away`, `corrected_score`, `remove_both`) and can strike the reporter of a rejected claim; enough active strikes block new registrations. Decisions have one entry point, so an automated decider can be added later. |
| R9 | Removal works in single elimination, double elimination and round robin, including groups. Removed entries get no placement, and removals and forfeits never change ratings. |
| R10 | Screenshot processing is hardened against hostile files: JPEG and PNG only, structure checked before decoding, bounded decode memory and time-limited retries. See [screenshot pipeline](screenshot-pipeline.md). |

### Match state and verification phase

A match keeps its `matches.state`; the finer phase lives in
`match_result_verifications`. Before acting on a live match, every planner
checks the locked rows against this table, and a combination that fits no row
is treated as a bug: nothing is written and the transaction rolls back.

| `matches.state` | Verification phase | Meaning |
|---|---|---|
| `in_progress` | no row | Both entries checked in and nobody has reported. |
| `awaiting_confirmation` | `awaiting_second_report` | Exactly one entry has reported; the report window is running. |
| `disputed` | `awaiting_responses` | The initial reports differ; the response window is running. At most one entry has responded. |
| `disputed` | `in_review` | The match is in the Gamics review queue: both entries responded and still differ (`reports_differ`), or a non-responder's screenshot is stuck (`evidence_unavailable`), in which case zero or one entry has responded. |
| `completed`, `forfeit` or `cancelled` | `resolved`, or no row | Terminal. There is no row when nobody reported (R7) or when the competition was cancelled before any report. |

A resolved row records how it ended: `agreed`, `report_timeout`,
`response_timeout`, `platform_review` or `competition_cancelled`.

### Transitions

X is the entry that reported first and Y the other entry. T2 to T17 run under
the competition gate and the match lock and judge deadlines on the database
clock (D26); T1 is the existing check-in, which does neither. At the exact
deadline instant the worker acts and a request is refused. Every transition
except T3 raises the match version. T2 to T14 and T16 never run on a cancelled
or completed competition (D20).

| # | Trigger | From to | Effect |
|---|---|---|---|
| T1 | The second entry checks in | `ready` to `in_progress` | The match can be reported. Check-in itself is unchanged. |
| T2 | X's initial report, before `resultDueAt` | `in_progress` to `awaiting_confirmation` (`awaiting_second_report`) | Report stored; the windows are copied from the rules onto a new verification row; Y gets `result.report_received`. |
| T3 | Worker: the reminder time is reached, the report window is still open and no reminder was sent | no change | Y gets `result.report_reminder`, once. The match version does not change. |
| T4 | Y's initial report, equal to X's, inside the report window | `awaiting_confirmation` to `completed` (`played`) | Canonical result (`agreed_reports`); verification `agreed`; progression; ratings; `match.result_confirmed`. |
| T5 | Y's initial report, different from X's, inside the report window | `awaiting_confirmation` to `disputed` (`awaiting_responses`) | The response window starts; both entries get `result.mismatch` without either score. |
| T6 | Worker: the report window ended and Y never reported (R5) | `awaiting_confirmation` to `forfeit` (`report_timeout`) | Y is removed and X wins; verification `report_timeout`; no ratings; `match.forfeited` and `competition.entry_removed`. |
| T7 | A final report that makes the current claims equal | `disputed` to `completed` (`played`) | As T4, with the screenshots bound to the final report. |
| T8 | A final report that still differs while the other entry has not responded | `disputed` to `disputed` (`awaiting_responses`) | Report and screenshots stored. No push. |
| T9 | A final report that still differs after the other entry responded | `disputed` to `disputed` (`in_review`) | Review queued with reason `reports_differ`; both entries get `result.under_review`. |
| T10 | Worker: the response window ended with exactly one responder, and the non-responder is not evidence-blocked (R6) | `disputed` to `forfeit` (`response_timeout`) | The non-responder is removed and the responder wins; verification `response_timeout`; no ratings. |
| T11 | Worker: the response window ended with no responder, and neither entry is evidence-blocked (R6) | `disputed` to `cancelled` (`response_timeout`) | Both entries are removed; verification `response_timeout`; no ratings. |
| T12 | Worker: `resultDueAt` passed with no report (R7) | `in_progress` to `cancelled` (`no_result_reported`) | Both entries are removed; no verification row is created; no ratings. |
| T13 | Review decision `accept_home`, `accept_away` or `corrected_score` | `disputed` (`in_review`) to `completed` (`platform_review`) | Canonical result (`platform_review`); verification `platform_review`; review `decided`; progression; ratings; optional strikes; `result.review_decided` instead of `match.result_confirmed`. |
| T14 | Review decision `remove_both` | `disputed` (`in_review`) to `cancelled` (`platform_review`) | Both entries are removed; verification `platform_review`; review `decided`; no ratings; optional strikes; `result.review_decided`. |
| T15 | Any match reaching a terminal state | the next matches of the stage | Progression. Knockout voids a removed entry's slots and gives walkovers; round robin settles its fixtures when their round is released. See [Removal in each format](#removal-in-each-format). |
| T16 | Worker: the response window ended and a non-responder's screenshot is still processing or failed for a Gamics-side reason | `disputed` to `disputed` (`in_review`) | Review queued with reason `evidence_unavailable`; both entries get `result.under_review`. Nobody is removed. |
| T17 | The organizer cancels the competition | every `pending`, `ready`, `in_progress`, `awaiting_confirmation` or `disputed` match to `cancelled` (`competition_cancelled`) | In the cancel transaction: open verifications resolve as `competition_cancelled` and queued reviews close. No removal, progression, ratings, strike or per-match push. |

### Decisions

| Decision | What the code does | Why |
|---|---|---|
| D6 | `games` is optional. Claims agree on the home score, the away score and the tiebreak only. The stored breakdown is the shared one when both sides sent identical games, otherwise one aggregate game. | Honest players must never disagree over game order or detail. |
| D13 | A review has exactly four decisions. A liar on the losing side is struck, not removed. An automated decider can neither set a corrected score nor record a strike. | An unreviewed automated decision must never lead to a ban. |
| D14 | Strikes come only from staff review decisions, against the reporter of a rejected current claim, at most two per decision, and stay active until an admin revokes one. `RESULT_STRIKE_BAN_THRESHOLD` active strikes block free registration and M-Pesa checkout; an M-Pesa payment that completes after the limit gets a withdrawal-pending entry that never plays and a mandatory full refund. Existing entries are not affected. | Strikes are for liars, not for typo fixers or the accepted side, and paying must not bypass the ban. |
| D17 | `matchVerification` accepts only the three window keys on write. Retired single-submission keys are refused by name, and stored rules that still carry them are read without them. | A clear contract, while older stored rules keep working. |
| D18 | A draw needs `resultWindowMinutes` of at least the check-in grace period plus 15 minutes. | R7 removes both entries, so a late but valid check-in must still leave time to play. |
| D20 | Cancelling a competition cancels its live matches (T17) without removals, ratings or strikes. Reports, review decisions and the worker refuse or skip cancelled and completed competitions; no other competition status is gated. | Nobody is removed, rated or banned over an event that did not finish. Check-in has no status gate, so gating reports on other statuses could strand live matches. |
| D26 | Deadlines are judged on `statement_timestamp()` read after the competition gate is held, not on the transaction start time. | A request that waited on the gate can neither slip in after a deadline nor backdate a new one. |
| D27 | Staff who play in the match, captain an entry or belong to the competition's organization cannot list, open or decide its review, read its screenshots, see its strikes in the strike feed or revoke them. | Organizers never decide results (R1), nobody sees a claim early (R2), and a strike carries the decider's note. |
| D28 | Score reports, withdrawals, paid withdrawals and organizer status changes run an unlocked membership or ownership probe before they take the competition gate. | A caller with no stake in the competition cannot make its finalization queue behind spam requests. |
