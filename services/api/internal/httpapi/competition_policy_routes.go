package httpapi

import "net/http"

// registerCompetitionPolicyRoutes is kept separate so the API assembly has one
// explicit integration point for player-specific competition decisions. These
// responses must never be placed behind the public competition cache.
func (s *Server) registerCompetitionPolicyRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/competitions/{id}/eligibility", s.requireAuth(http.HandlerFunc(s.getCompetitionEligibility)))
}
