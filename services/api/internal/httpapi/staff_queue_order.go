package httpapi

import "time"

// Staff lists can show every status at once (status=all). They put open
// records first, oldest first, so the longest wait is on top, then finished
// records, newest first. One bigint key carries both orders: an open record's
// key is its time in nanoseconds, and a finished record's key is
// finishedQueueKeyBase minus its time, which is always larger. Keyset paging
// then needs only (key, id).
const finishedQueueKeyBase int64 = 9_000_000_000_000_000_000

// staffQueueOrderKey returns the SQL expression for a row's order key. The
// arguments are trusted SQL fragments from this package, never input.
func staffQueueOrderKey(openCondition, openTime, finishedTime string) string {
	return `(CASE WHEN ` + openCondition + ` THEN (extract(epoch FROM ` + openTime + `)*1000000000)::bigint
		ELSE ` + "9000000000000000000" + `-(extract(epoch FROM ` + finishedTime + `)*1000000000)::bigint END)`
}

// staffQueueKey computes the same key in Go, for the next page's cursor.
// PostgreSQL stores microseconds, so both sides agree exactly.
func staffQueueKey(open bool, openTime, finishedTime time.Time) int64 {
	if open {
		return openTime.UnixNano()
	}
	return finishedQueueKeyBase - finishedTime.UnixNano()
}

// encodeStaffQueueKeyCursor binds an order-key cursor to the operator and the
// filter scope, like encodeStaffQueueCursor does for time cursors.
func (s *Server) encodeStaffQueueKeyCursor(kind, scope, actorID string, key int64, id string) (string, error) {
	return encodePublicCursor(publicCursor{
		Kind: kind, ExpiresAt: time.Now().Add(paymentCursorTTL).Unix(), Query: actorID, Scope: scope,
		SortTime: key, ID: id,
	}, s.config.AccessTokenSecret)
}

func timeOr(value *time.Time, fallback time.Time) time.Time {
	if value != nil {
		return *value
	}
	return fallback
}
