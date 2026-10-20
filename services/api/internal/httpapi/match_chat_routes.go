package httpapi

import "net/http"

// registerMatchChatRoutes mounts the match chat. The player routes answer 404
// unless the caller is a starter or substitute of one of the match's entries;
// the staff routes need result_review.manage.
func (s *Server) registerMatchChatRoutes(mux *http.ServeMux) {
	mux.Handle("GET /v1/matches/{matchId}/chat", s.requireAuth(http.HandlerFunc(s.getMatchChat)))
	mux.Handle("POST /v1/matches/{matchId}/chat/messages", s.requireAuth(http.HandlerFunc(s.postMatchChatMessage)))
	mux.Handle("POST /v1/matches/{matchId}/chat/read", s.requireAuth(http.HandlerFunc(s.readMatchChat)))
	mux.Handle("POST /v1/matches/{matchId}/chat/reports", s.requireAuth(http.HandlerFunc(s.reportMatchChatMessage)))
	review := platformResultReviewManage
	mux.Handle("GET /v1/admin/match-chats/{matchId}", s.platformRoute(review, s.getAdminMatchChat))
	mux.Handle("GET /v1/admin/chat-reports", s.platformRoute(review, s.listMatchChatReports))
	mux.Handle("POST /v1/admin/chat-reports/{id}/resolution", s.platformRoute(review, s.resolveMatchChatReport))
}
