package httpapi

import (
	"context"
	"time"
)

const (
	matchChatSweepInterval = time.Hour
	matchChatSweepBatch    = 200
)

// matchChatExpiredSQL deletes a batch of chats whose match was final 7 days
// ago, unless a result review of the match is queued or was settled in the
// last 30 days, or a report of the chat is open or was resolved in the last 30
// days. Deleting a chat removes its messages, read positions and reports.
const matchChatExpiredSQL = `WITH expired AS (
		SELECT chat.match_id FROM match_chats chat
		JOIN matches m ON m.id=chat.match_id
		WHERE m.state IN ('completed','forfeit','cancelled')
		  AND COALESCE(m.completed_at,m.updated_at)<now()-interval '7 days'
		  AND NOT EXISTS (SELECT 1 FROM match_result_reviews review WHERE review.match_id=chat.match_id
			AND (review.status='queued' OR review.decided_at>now()-interval '30 days'))
		  AND NOT EXISTS (SELECT 1 FROM match_chat_reports report WHERE report.match_id=chat.match_id
			AND (report.status='open' OR report.resolved_at>now()-interval '30 days'))
		ORDER BY chat.match_id LIMIT $1
		FOR UPDATE OF chat SKIP LOCKED
	)
	DELETE FROM match_chats chat USING expired WHERE chat.match_id=expired.match_id`

// runMatchChatRetention deletes expired match chats once an hour.
func (s *Server) runMatchChatRetention(ctx context.Context) {
	if s.db == nil {
		return
	}
	s.deleteExpiredMatchChats(ctx)
	ticker := time.NewTicker(matchChatSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.deleteExpiredMatchChats(ctx)
		}
	}
}

// deleteExpiredMatchChats deletes batches until none is left and returns how
// many chats it deleted.
func (s *Server) deleteExpiredMatchChats(ctx context.Context) int64 {
	var total int64
	for ctx.Err() == nil {
		command, err := s.db.Writer.Exec(ctx, matchChatExpiredSQL, matchChatSweepBatch)
		if err != nil {
			s.logger.Warn("match chat retention sweep failed", "error", err)
			return total
		}
		total += command.RowsAffected()
		if command.RowsAffected() < matchChatSweepBatch {
			break
		}
	}
	if total > 0 {
		s.logger.Info("deleted expired match chats", "chats", total)
	}
	return total
}
