package httpapi

import (
	"net/http"

	"github.com/gamics-io/gamics/services/api/internal/organizer"
)

// registerOrganizerRoutes wires the organizer surface. Every route except the
// two bootstrap routes goes through organizerRoute, which resolves the caller's
// membership in {orgId} and refuses the request unless it grants the named
// permission.
func (s *Server) registerOrganizerRoutes(mux *http.ServeMux) {
	// Bootstrap: there is no organization to authorize against yet.
	mux.Handle("POST /v1/organizations", s.requireAuth(http.HandlerFunc(s.createOrganization)))
	mux.Handle("GET /v1/me/organizations", s.requireAuth(http.HandlerFunc(s.listMyOrganizations)))

	mux.Handle("GET /v1/organizations/{orgId}",
		s.organizerRoute(organizer.PermissionOrganizationRead, s.getOrganization))
	mux.Handle("PATCH /v1/organizations/{orgId}",
		s.organizerRoute(organizer.PermissionOrganizationManage, s.patchOrganization))

	mux.Handle("GET /v1/organizations/{orgId}/members",
		s.organizerRoute(organizer.PermissionMemberRead, s.listOrganizationMembers))
	mux.Handle("POST /v1/organizations/{orgId}/members",
		s.organizerRoute(organizer.PermissionMemberManage, s.addOrganizationMember))
	mux.Handle("PATCH /v1/organizations/{orgId}/members/{userId}",
		s.organizerRoute(organizer.PermissionMemberManage, s.patchOrganizationMember))
	mux.Handle("DELETE /v1/organizations/{orgId}/members/{userId}",
		s.organizerRoute(organizer.PermissionMemberManage, s.removeOrganizationMember))

	mux.Handle("GET /v1/organizations/{orgId}/competitions",
		s.organizerRoute(organizer.PermissionCompetitionRead, s.listOrganizerCompetitions))
	mux.Handle("POST /v1/organizations/{orgId}/competitions",
		s.organizerRoute(organizer.PermissionCompetitionWrite, s.createOrganizerCompetition))
	mux.Handle("GET /v1/organizations/{orgId}/competitions/{competitionId}",
		s.organizerRoute(organizer.PermissionCompetitionRead, s.getOrganizerCompetition))
	mux.Handle("PATCH /v1/organizations/{orgId}/competitions/{competitionId}",
		s.organizerRoute(organizer.PermissionCompetitionWrite, s.patchOrganizerCompetition))
	mux.Handle("POST /v1/organizations/{orgId}/competitions/{competitionId}/transitions",
		s.organizerRoute(organizer.PermissionCompetitionPublish, s.transitionOrganizerCompetition))
	mux.Handle("GET /v1/organizations/{orgId}/competitions/{competitionId}/entries",
		s.organizerRoute(organizer.PermissionEntryRead, s.listOrganizerCompetitionEntries))
}
