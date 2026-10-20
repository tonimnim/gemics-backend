# Match chat

Most opponents in an Ozone match are not eFootball friends, so they need a way to agree
when to play and to swap the Friend Match room code. Match chat is one thread per match
between the players of its two entries. It is built for that job and nothing more: no
direct messages, no group chat, and no chat history that outlives the match for long.

The larger design in [chat-architecture.md](chat-architecture.md) (persistent
conversations, a WebSocket gateway, a partitioned message store) stays the plan if chat
ever outgrows matches.

## Who can do what

| Who | Read | Post | Report |
|---|---|---|---|
| Starters and substitutes of the match's two entries | Until the chat is deleted | While the chat is open | The other side's messages |
| Ozone staff with `result_review.manage` | The full transcript, every read audited | Never | Resolve reports |
| Anyone else | `404 match_not_found`, as for the match room | – | – |

The chat is **open** once both sides of the match are known and closes when the match is
final (`completed`, `forfeit` or `cancelled`). A closed chat stays readable, and players
can still report a message in it.

## Messages

| `kind` | Content | Notes |
|---|---|---|
| `text` | 1–500 characters | Emails, links and phone numbers are replaced by `[hidden]` before storing |
| `quick_reply` | One of `ready_now`, `five_minutes`, `ten_minutes`, `friend_request_sent`, `room_created`, `good_game` | Stored as a code; the app words it |
| `room_code` | 1–32 letters, digits, spaces or dashes | Pins the code on the chat and the match card |
| `time_proposal` | A start time | Must be in the future and before the match's `resultDueAt` |
| `time_answer` | Accept or decline the latest proposal | Only the other side; accepting sets `agreedStartAt` |

`agreedStartAt` records what the players arranged. It moves no deadline: check-in and the
result deadline still decide no-shows and removals.

Hiding contact details keeps the arrangement on Ozone, which matters because 13–17 year
olds play and because side bets arranged elsewhere are out of reach. The raw text is
never stored. Room codes have their own kind, so the phone-number rule never eats one.

Every message has a `seq` that is gapless per chat. It is taken under a lock on the chat
row, so seqs commit in order and a client polling `after` its last seq never skips a
message. A `clientMessageId` makes sending safe to retry.

Limits: 20 messages a minute per player (Redis; if Redis is down the limit is skipped)
and 500 messages per chat.

## Delivery

The app polls `GET /v1/matches/{matchId}/chat?after={seq}` every few seconds while the
chat is on screen. Each poll marks the player as present for 15 seconds in Redis.

When a message is sent, each opponent who is not present and has not been pushed about
this chat in the last two minutes gets a `match.chat_message` notification (preference
`matchPush`). Marking the chat read clears the two-minute spacing. The push names the
sender but never shows the message, so nothing private appears on a lock screen.

Unread counts come from read positions: sending a message also reads everything before
it, so every message past a player's read position is the opponent's.

## Retention

The retention worker runs hourly and deletes a chat, with its messages, read positions
and reports, once all of these hold:

- the match has been final for 7 days;
- no result review of the match is queued, and none was settled in the last 30 days;
- no report of the chat is open, and none was resolved in the last 30 days.

So a disputed match keeps its chat as evidence until the review is decided and for 30
days after, and a reported chat stays until staff deal with the report and for 30 days
after.

## Staff

- `GET /v1/admin/match-chats/{matchId}` returns the whole transcript with its reports.
  Each read writes a `match_chat.viewed` audit event.
- `GET /v1/admin/chat-reports?status=open|resolved` pages the report queue.
- `POST /v1/admin/chat-reports/{id}/resolution` records `dismissed` or `actioned`.
  Acting on a report, such as suspending the player, uses the existing player admin
  routes.

## Schema

Migration `000007_match_chat` adds `match_chats` (one row per chat: last seq, pinned
room code, agreed start), `match_chat_messages`, `match_chat_reads` (read position and
last push per player) and `match_chat_reports`. Deleting a chat cascades to the rest.
