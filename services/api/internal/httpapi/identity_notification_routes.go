package httpapi

import "net/http"

// registerIdentityNotificationRoutes wires the authenticated player identity,
// device, notification, preferences, legal-consent and account-lifecycle APIs.
// The current legal-document endpoint is public so onboarding clients can render
// the exact versions they will ask a player to accept.
func (s *Server) registerIdentityNotificationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/legal/documents/current", s.getCurrentLegalDocuments)

	authenticated := func(methodPath string, handler http.HandlerFunc) {
		mux.Handle(methodPath, s.requireAuth(handler))
	}
	authenticated("POST /v1/me/push-tokens", s.upsertPushToken)
	authenticated("DELETE /v1/me/push-tokens/{id}", s.revokePushToken)

	authenticated("GET /v1/me/notifications", s.listNotifications)
	authenticated("GET /v1/me/notifications/unread-count", s.getUnreadNotificationCount)
	authenticated("POST /v1/me/notifications/read-all", s.markAllNotificationsRead)
	authenticated("POST /v1/me/notifications/{id}/read", s.markNotificationRead)

	authenticated("GET /v1/me/notification-preferences", s.getNotificationPreferences)
	authenticated("PATCH /v1/me/notification-preferences", s.patchNotificationPreferences)
	authenticated("GET /v1/me/legal-acceptances", s.listLegalAcceptances)
	authenticated("POST /v1/me/legal-acceptances", s.acceptLegalDocuments)

	authenticated("GET /v1/me/sessions", s.listSessions)
	authenticated("DELETE /v1/me/sessions/{id}", s.revokeSession)
	authenticated("GET /v1/me/account-deletion", s.getAccountDeletionRequest)
	authenticated("POST /v1/me/account-deletion", s.requestAccountDeletion)
	authenticated("DELETE /v1/me/account-deletion", s.cancelAccountDeletion)
	authenticated("POST /v1/me/account-deletion/execute", s.executeAccountDeletion)
}
