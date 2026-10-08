package httpapi

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

type platformPermission string

const (
	platformOverviewView        platformPermission = "overview.view"
	platformRefundView          platformPermission = "refund.view"
	platformRefundManage        platformPermission = "refund.manage"
	platformVerificationManage  platformPermission = "game_account_verification.manage"
	platformPaymentReviewManage platformPermission = "payment_review.manage"
	platformResultReviewManage  platformPermission = "result_review.manage"
	platformPlayerStrikeRevoke  platformPermission = "player_strike.revoke"
	platformCompetitionManage   platformPermission = "competition.manage"
	platformStaffManage         platformPermission = "staff.manage"
	platformPlayerView          platformPermission = "player.view"
	platformPlayerSuspend       platformPermission = "player.suspend"
	// platformFinanceView covers revenue figures. Money queues have their own
	// permissions, which only admins hold too.
	platformFinanceView platformPermission = "finance.view"
	// platformFinanceManage covers entering exchange rates by hand.
	platformFinanceManage platformPermission = "finance.manage"
)

// gamicsOrganizationID is the organization Gamics runs its own competitions
// under, seeded by migration 000001. Staff with competition.manage act as its
// admins; there are no outside organizers in V1.
const gamicsOrganizationID = "6a1c5e00-0000-4000-8000-000000000001"

// platformRoles lists every staff role, least to most powerful.
var platformRoles = []string{"support", "reviewer", "operator", "admin"}

// platformRolePermissions is the whole staff permission matrix. Every staff
// role creates and runs competitions, which includes seeing each
// competition's registrations, sees the overview and looks players up; none
// sees money. Operators and admins suspend players. Admins hold every
// permission: finance, payment reviews, refund decisions and granting staff
// roles.
var platformRolePermissions = map[string][]platformPermission{
	"support": {platformOverviewView, platformCompetitionManage, platformPlayerView},
	"reviewer": {platformOverviewView, platformCompetitionManage, platformPlayerView, platformVerificationManage,
		platformResultReviewManage},
	"operator": {platformOverviewView, platformCompetitionManage, platformPlayerView, platformVerificationManage,
		platformResultReviewManage, platformPlayerStrikeRevoke, platformPlayerSuspend},
	"admin": {platformOverviewView, platformRefundView, platformRefundManage, platformVerificationManage,
		platformPaymentReviewManage, platformResultReviewManage, platformPlayerStrikeRevoke,
		platformCompetitionManage, platformStaffManage, platformFinanceView, platformFinanceManage, platformPlayerView,
		platformPlayerSuspend},
}

func platformRoleCan(role string, permission platformPermission) bool {
	return slices.Contains(platformRolePermissions[strings.TrimSpace(role)], permission)
}

// platformRoute is intentionally separate from organizerRoute. Organization
// ownership never grants access to global payment, account-verification or
// result-review queues.
func (s *Server) platformRoute(permission platformPermission, next http.HandlerFunc) http.Handler {
	return s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.requireDatabase(w) {
			return
		}
		allowed, err := s.staffCan(r.Context(), identityFromContext(r.Context()).UserID, permission)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify staff access.")
			return
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "platform_access_denied", "This operation requires Gamics staff access.")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// staffRole returns the caller's active staff role, or "" for a non-staff or
// inactive account. It reads the writer so a revoked role stops at once.
func (s *Server) staffRole(ctx context.Context, userID string) (string, error) {
	var role string
	err := s.db.Writer.QueryRow(ctx, `SELECT staff.role FROM platform_staff_roles staff
		JOIN users player ON player.id=staff.user_id AND player.status='active'
		WHERE staff.user_id=$1 AND staff.revoked_at IS NULL`, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}

func (s *Server) staffCan(ctx context.Context, userID string, permission platformPermission) (bool, error) {
	role, err := s.staffRole(ctx, userID)
	return err == nil && platformRoleCan(role, permission), err
}
