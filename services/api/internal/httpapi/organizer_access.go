package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gamics-io/gamics/services/api/internal/organizer"
	"github.com/jackc/pgx/v5"
)

type organizerContextKey struct{}

type organizerMembership struct {
	OrganizationID string
	Role           organizer.Role
}

// organizerRoute is the only way an organizer handler should be mounted. It
// forces every route to name the permission it needs, so a handler cannot be
// registered without an authorization decision attached to it.
func (s *Server) organizerRoute(permission organizer.Permission, handler http.HandlerFunc) http.Handler {
	return s.requireAuth(s.requireOrgPermission(permission, handler))
}

// requireOrgPermission resolves the caller's membership in the organization
// named by the {orgId} path value and refuses the request unless that
// membership grants the permission.
//
// Membership is read from the writer, never a replica: a revoked role must stop
// working immediately, and replication lag on an authorization check is an
// authorization bug.
func (s *Server) requireOrgPermission(permission organizer.Permission, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.requireDatabase(w) {
			return
		}
		organizationID := strings.TrimSpace(r.PathValue("orgId"))
		if !uuidPattern.MatchString(organizationID) {
			writeOrganizationNotFound(w)
			return
		}
		userID := identityFromContext(r.Context()).UserID
		var role, status string
		err := s.db.Writer.QueryRow(r.Context(), `SELECT member.role,organization.status
			FROM organization_members member
			JOIN organizations organization ON organization.id=member.organization_id
			WHERE member.organization_id=$1 AND member.user_id=$2`, organizationID, userID).Scan(&role, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			// Non-members are told the organization does not exist. Answering
			// 403 here would confirm the identifier and let anyone enumerate
			// organizations.
			writeOrganizationNotFound(w)
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify your organizer access.")
			return
		}
		if status != "active" {
			writeError(w, http.StatusForbidden, "organization_suspended",
				"This organization is suspended. Contact Gamics support.")
			return
		}
		membership := organizerMembership{OrganizationID: organizationID, Role: organizer.Role(role)}
		if !membership.Role.Can(permission) {
			writeError(w, http.StatusForbidden, "insufficient_role",
				"Your role in this organization does not allow that action.")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), organizerContextKey{}, membership)))
	})
}

func organizerFromContext(ctx context.Context) organizerMembership {
	value, _ := ctx.Value(organizerContextKey{}).(organizerMembership)
	return value
}

func writeOrganizationNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "organization_not_found", "Organization not found.")
}
