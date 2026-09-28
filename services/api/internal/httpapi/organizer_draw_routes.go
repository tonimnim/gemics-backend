package httpapi

import (
	"net/http"

	"github.com/gamics-io/gamics/services/api/internal/organizer"
)

// registerOrganizerDrawRoutes mounts draw generation separately from the rest
// of the organizer console. Keeping this registrar modular lets server.go opt
// into the write surface explicitly, and makes it impossible to accidentally
// expose it without the competition-management permission.
func (s *Server) registerOrganizerDrawRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/organizations/{orgId}/competitions/{competitionId}/draws",
		s.organizerRoute(organizer.PermissionCompetitionWrite, s.createOrganizerDraw))
}
