package httpapi

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// A player reading their own data straight after changing it must see the
// change. The replica may not have it yet: right after registration, the game
// account list could come back empty, and the match room could still offer a
// result the player has just confirmed. So a player's own writes mark them in
// Redis for DatabaseMaxReplicaLag, the replica lag the API already tolerates,
// and while the mark lasts their own reads go to the writer. Everyone else's
// reads, and all public reads, stay on the replica.

func (s *Server) recentWriteKey(userID string) string {
	return s.securityKey("rw:" + userID)
}

// markRecentWrite records that userID has just written. It is a best-effort
// hint: if Redis is down the mark is lost, and selfReader then fails safe to
// the writer anyway.
func (s *Server) markRecentWrite(ctx context.Context, userID string) {
	if userID == "" || s.redis == nil || s.db == nil || s.db.Reader == s.db.Writer {
		return
	}
	if err := s.redis.Set(ctx, s.recentWriteKey(userID), "1", s.config.DatabaseMaxReplicaLag).Err(); err != nil {
		s.logger.Warn("mark recent write", "error", err)
	}
}

// selfReader returns the pool for reading userID's own data: the writer while
// their last write may not have reached the replica, or whenever that can't be
// known, and the replica otherwise.
func (s *Server) selfReader(ctx context.Context, userID string) *pgxpool.Pool {
	if s.db.Reader == s.db.Writer || s.redis == nil {
		return s.db.Writer
	}
	marked, err := s.redis.Exists(ctx, s.recentWriteKey(userID)).Result()
	if err != nil || marked > 0 {
		return s.db.Writer
	}
	return s.db.Reader
}

// isWrite reports whether a request method can change state.
func isWrite(method string) bool {
	switch method {
	case "GET", "HEAD", "OPTIONS":
		return false
	}
	return true
}
