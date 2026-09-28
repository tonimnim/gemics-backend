package httpapi

import "net/http"

// registerPaymentReviewRoutes is the only mount point for the global M-Pesa
// review queue. Organization roles never reach these platform-admin routes.
func (s *Server) registerPaymentReviewRoutes(mux *http.ServeMux) {
	permission := platformPaymentReviewManage
	mux.Handle("GET /v1/admin/payment-reviews", s.platformRoute(permission, s.listPaymentReviews))
	mux.Handle("GET /v1/admin/payment-reviews/{id}", s.platformRoute(permission, s.getPaymentReview))
	mux.Handle("POST /v1/admin/payment-reviews/{id}/decisions", s.platformRoute(permission, s.decidePaymentReview))
}
