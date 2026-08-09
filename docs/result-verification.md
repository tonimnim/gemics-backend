# eFootball result verification

Konami does not currently provide Gamics with a trusted public match-result API,
so a result is a participant claim until it is confirmed or reviewed.

## Recommended launch workflow

1. Both assigned players check in and receive the opponent and match deadline.
2. After playing, the winner submits the score and uploads the final-result
   screenshot. The submission records the match ID, user ID and server time.
3. The opponent receives a push notification and chooses **Confirm** or
   **Dispute**. Confirmation finalizes the result and advances the bracket.
4. If disputed, both players provide their result screen and a referee decides.
   The decision and evidence hashes remain in the audit log.
5. If the opponent is silent, a screenshot-backed result can auto-confirm after
   the published timeout, followed by a short appeal window. A claim without
   evidence never wins by silence.

For ordinary free tournaments this gives good trust without requiring two
uploads every match. For paid entry, qualifiers, finals or meaningful prizes,
require evidence from both players and allow referees to request a short screen
recording that shows the result and relevant match history.

## Evidence is a signal, not proof

Screenshots can be edited. Gamics should add layered controls:

- independent score entry or explicit opponent confirmation;
- OCR to prefill and compare scores, with the image retained for human review;
- perceptual hashes to detect a reused screenshot;
- submission deadlines, immutable timestamps and an append-only audit trail;
- anomaly flags for repeated collusion, impossible schedules and unusual score patterns;
- referee queues, appeals and published penalties for false evidence;
- monitored screen sharing or recording for high-value final matches.

EXIF metadata and automated image-tampering scores must never be the sole basis
for accepting or rejecting a result.
