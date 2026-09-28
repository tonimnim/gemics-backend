package httpapi

import "net/http"

// registerMediaRoutes keeps the private evidence-status and avatar media
// surface modular. It is invoked from the existing player route registrar so
// server construction remains centralized.
func (s *Server) registerMediaRoutes(mux *http.ServeMux) {
	authenticated := func(methodPath string, handler http.HandlerFunc) {
		mux.Handle(methodPath, s.requireAuth(handler))
	}

	authenticated("GET /v1/evidence/uploads/{id}", s.getEvidenceUpload)

	authenticated("POST /v1/me/avatar/uploads", s.createAvatarUpload)
	authenticated("POST /v1/me/avatar/uploads/{id}/complete", s.completeAvatarUpload)
	authenticated("PATCH /v1/me/avatar", s.updateProfileAvatar)
	authenticated("GET /v1/me/avatar", s.getMyAvatarAccess)
	mux.HandleFunc("GET /v1/players/{playerId}/avatar", s.getPublicAvatarAccess)
}
