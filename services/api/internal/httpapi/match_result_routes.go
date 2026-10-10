package httpapi

import "net/http"

// registerMatchResultRoutes mounts the result routes: submit the result,
// confirm or reject the opponent's, and send a screenshot after a rejection.
// Each needs a signed-in player, and the handlers answer 404 unless the caller
// is a starter or substitute of one of the match's entries.
func (s *Server) registerMatchResultRoutes(mux *http.ServeMux) {
	mux.Handle("POST /v1/matches/{matchId}/score-reports", s.requireAuth(http.HandlerFunc(s.createScoreReport)))
	mux.Handle("POST /v1/matches/{matchId}/score-reports/confirmation", s.requireAuth(http.HandlerFunc(s.createResultConfirmation)))
	mux.Handle("POST /v1/matches/{matchId}/score-reports/screenshot", s.requireAuth(http.HandlerFunc(s.createResultScreenshot)))
}
