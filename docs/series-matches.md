# Best-of-3 and best-of-5 series

Status: built in the Go API, OpenAPI and the staff dashboard. The player app
still needs its side (see [mobile API handoff](mobile-api-requirements.md)),
and the screenshot reader needs real penalty-finish samples before it can
settle games decided on penalties.

## The rules

- **Format.** A match is a best of 1, 3 or 5, chosen per round. Early rounds
  can be a best of 1, the semifinal and final a best of 3 or 5.
- **Scoring.** One point per game won. The first to a majority wins the
  series: 2 in a best of 3, 3 in a best of 5. Games that can no longer change
  the result are not played.
- **Knockout games need a winner.** A game level at full time is settled by
  penalties in that game.
- **Round robin.** Games can end level, and so can a series nobody won. Group
  points are games won: a drawn game scores nothing, and a walkover counts as
  the games a series needs. Ties are broken by game difference, then goal
  difference, then goals scored.
- **When it's set.** Staff set the format when they create the tournament. It
  is frozen once registration opens, because players enter knowing it.

| | Games to win | Games played | Screenshots per player in a dispute | Total |
|---|---|---|---|---|
| Best of 1 | 1 | 1 | 1 | 2 |
| Best of 3 | 2 | 2 or 3 | 2 or 3 | 4 to 6 |
| Best of 5 | 3 | 3 to 5 | 3 to 5 | 6 to 10 |

## Setting it: `rules.matchFormat`

```json
{ "matchFormat": { "bestOf": 1, "rounds": { "semifinal": 3, "final": 5 } } }
```

- **Default.** `bestOf` applies to every match; `rounds` overrides named rounds.
- **Round names** are counted back from the final, because the bracket size is
  only known at the draw:

  | Format | Round names |
  |---|---|
  | Single elimination | `final`, `semifinal`, `quarterfinal`, `round_of_16` up to `round_of_1024`, plus `third_place` |
  | Double elimination | `final` (the grand final and its reset), `winners_final`, `losers_final` |
  | Round robin | none; `bestOf` only |

- **Validation.** Only the values 1, 3 and 5 are allowed. A round the format
  doesn't have is refused with `400 invalid_match_format`. A change after
  registration opens is `409 field_locked`.
- **At the draw.** Each match's best-of is copied onto `matches.best_of`, and
  every result rule reads that column. `competition_stages.best_of` keeps the
  default. The draw request no longer takes a best-of.
- **Where to see it.** The competition detail returns the format as
  `matchFormat`, with defaults applied. Bracket matches and the match room
  return each match's `bestOf`.
- **Admin.** The New competition sheet has a Match format section: "Every
  round" plus a Bo1/Bo3/Bo5 row for each round the field can reach. The
  Generate draw dialog shows the format read-only and offers the third-place
  match.

## Time

A series gets 15 more minutes per game after the first:

- **Result window.** The draw's result window becomes, for example, 60 minutes
  for a best of 1, 90 for a best of 3 and 120 for a best of 5. It applies when
  a match is scheduled at the draw and when progression readies it.
- **Round robin.** The interval between rounds grows by the same amount.
- **Screenshot window.** A series defaults to 15 minutes instead of 10, unless
  the competition sets `responseWindowMinutes`.

## Results

A series is reported as **games won**: who won and by how many, such as 2-1.
Nobody enters the score of each game, and nobody reports penalties; they only
decide who won a level knockout game. The games themselves are shown only by
the screenshots of a dispute.

`POST /v1/matches/{matchId}/score-reports` for a series:

- **The body is `homeScore` and `awayScore` as games won**, plus
  `declarationAccepted`. `games` and `tiebreak` are refused.
- **Knockout:** exactly one side has `gamesToWin`: 2-0 or 2-1 in a best of 3;
  3-0, 3-1 or 3-2 in a best of 5.
- **Round robin:** each side has at most `gamesToWin`, at most `bestOf` games
  are won in total, and the series may end level, for example 1-1.
- **Errors:** anything else is `400 invalid_score` with a message to show.
- **Confirming** is on games won.
- **After it's settled:**
  - the winner is whoever won more games;
  - ratings change once per series, from its result;
  - the room's `result` and the bracket's `score` are games won;
  - round-robin standings rows add `gamesWon` and `gamesLost`, and series
    add no goals;
  - match list summaries carry `bestOf` and `gamesToWin`.
- **Staff corrections** are games won too.

## Disputes

After a rejection, each entry sends its screenshots:

```json
{ "screenshots": [ { "game": 1, "evidenceId": "…" }, { "game": 2, "evidenceId": "…" } ] }
```

- **How many.** Each game once, numbered 1 to the match's `bestOf`. A best of 1
  can still send `{"evidenceId": "…"}`.
- **Missing games.** Sending at least one screenshot counts as responding, so
  a missing game never removes anyone. It counts against that entry in review.
- **Storage.** `match_result_report_evidence.position` is the game index
  (game 1 is 0).
- **What the app sees:**
  - `verificationPolicy.screenshotEvidence` has `perGame` and `maxItems` (=
    `bestOf`);
  - `resultVerification.myScreenshots` lists the viewer's screenshots, and
    `myScreenshot` is the first of them.
- **What staff see:** the game of each screenshot, the reported games won, and the
  reader's per-game verdict.

## Screenshot reader

The Python reader needs no change: each game is one Full Time screen. The Go
comparison is per game; the details are in
[screenshot reader](screenshot-reader.md#series-best-of-3-and-5).

- **Game by game.** Game N's screenshots must agree with each other. The reader
  counts who won each game (penalties decide a level knockout game) and checks
  the tally against the reported games won.
- **Numbering slips.** When players numbered their games differently, the
  screenshots are re-paired by what they show, and staff decide.
- **Reuse.** One player's screenshot sent for two games is flagged
  `duplicate_in_series`.
- **Automatic decisions.** The reader settles a series only when the
  screenshots show the complete series, each game from both players, the tally
  matches, and no game went to penalties.

## Still to do

- **Player app.** Built by the app session against this contract; it still
  needs a run against a live series (one is seeded on the local stack).
- **Penalty samples.** 3 to 5 real screenshots of games won on penalties, so
  the reader can settle those games too.
- **Training feed.** Series aren't included yet; it still takes best-of-1
  matches only.
- **Early uploads (optional).** Let players upload each game's screenshot
  right after it's played, used only if there's a dispute.
