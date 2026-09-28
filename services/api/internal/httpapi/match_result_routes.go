package httpapi

import "net/http"

// registerMatchResultRoutes mounts the blind score-report routes. Both need a
// signed-in player, and the handlers answer 404 unless the caller is a starter
// or substitute of one of the match's entries.
func (s *Server) registerMatchResultRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/matches/{matchId}/score-reports", s.requireAuth(http.HandlerFunc(s.createScoreReport)))
	mux.Handle("POST /v1/matches/{matchId}/score-reports/final", s.requireAuth(http.HandlerFunc(s.createFinalScoreReport)))
}
