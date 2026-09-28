package httpapi

import (
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

type platformPermission string

const (
	platformRefundView          platformPermission = "refund.view"
	platformRefundManage        platformPermission = "refund.manage"
	platformVerificationManage  platformPermission = "game_account_verification.manage"
	platformPaymentReviewManage platformPermission = "payment_review.manage"
	platformResultReviewManage  platformPermission = "result_review.manage"
	platformPlayerStrikeRevoke  platformPermission = "player_strike.revoke"
)

func platformRoleCan(role string, permission platformPermission) bool {
	switch strings.TrimSpace(role) {
	case "admin":
		return true
	case "support":
		return permission == platformRefundView
	case "reviewer":
		return permission == platformVerificationManage || permission == platformResultReviewManage
	default:
		return false
	}
}

// platformRoute is intentionally separate from organizerRoute. Organization
// ownership never grants access to global payment, account-verification or
// result-review queues.
func (s *Server) platformRoute(permission platformPermission, next http.HandlerFunc) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.requireDatabase(w) {
			return
		}
		userID := identityFromContext(r.Context()).UserID
		var role string
		err := s.db.Writer.QueryRow(r.Context(), `SELECT staff.role FROM platform_staff_roles staff
			JOIN users player ON player.id=staff.user_id AND player.status='active'
			WHERE staff.user_id=$1 AND staff.revoked_at IS NULL`, userID).Scan(&role)
		if err == pgx.ErrNoRows || (err == nil && !platformRoleCan(role, permission)) {
			writeError(w, http.StatusForbidden, "platform_access_denied", "This operation requires Gamics staff access.")
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify staff access.")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
