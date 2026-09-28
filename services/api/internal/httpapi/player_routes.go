package httpapi

import "net/http"

// registerPlayerDiscoveryRoutes keeps this API slice's wiring in one place. Call
// it once from New after the games route is registered.
func (s *Server) registerPlayerDiscoveryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/rankings", s.rankings)
	mux.HandleFunc("GET /v1/players", s.players)
	mux.HandleFunc("GET /v1/players/{id}", s.publicPlayer)
	mux.HandleFunc("GET /v1/players/{id}/matches", s.publicPlayerMatches)
	mux.HandleFunc("GET /v1/players/{id}/competitions", s.publicPlayerCompetitions)
	s.registerMediaRoutes(mux)
}
