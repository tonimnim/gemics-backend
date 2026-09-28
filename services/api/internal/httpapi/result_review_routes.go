package httpapi

import "net/http"

// registerResultReviewRoutes is the only mount point for the Gamics result
// review queue and the conduct strike feed. Organization roles never reach
// these platform routes, and every review route also refuses a staff member
// who plays in, captains in or organizes the match (D27). Only admins revoke
// strikes.
func (s *Server) registerResultReviewRoutes(mux *http.ServeMux) {
	review := platformResultReviewManage
	mux.Handle("GET /v1/admin/result-reviews", s.platformRoute(review, s.listResultReviews))
	mux.Handle("GET /v1/admin/result-reviews/{id}", s.platformRoute(review, s.getResultReview))
	mux.Handle("POST /v1/admin/result-reviews/{id}/decisions", s.platformRoute(review, s.decideResultReview))
	mux.Handle("GET /v1/admin/player-strikes", s.platformRoute(review, s.listPlayerStrikes))
	mux.Handle("POST /v1/admin/player-strikes/{id}/revocations",
		s.platformRoute(platformPlayerStrikeRevoke, s.revokePlayerStrike))
}
