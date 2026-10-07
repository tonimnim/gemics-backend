package httpapi

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIntegrationStaffRolesGateTheAdminDashboard walks the dashboard's access
// model: staff are ordinary registered players with a platform role, admins
// grant and revoke roles, every staff role manages the Gamics organization's
// competitions, and only admins see money.
func TestIntegrationStaffRolesGateTheAdminDashboard(t *testing.T) {
	h := newRegistrationHarness(t)
	mux := http.NewServeMux()
	h.server.registerAdminRoutes(mux)
	h.server.registerOrganizerRoutes(mux)
	call := func(method, target, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, target, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	suffix := strings.ToUpper(rand.Text())[:6]
	register := func(name string) registrationSession {
		return h.session(h.call(h.server.register, http.MethodPost, "/v1/auth/register",
			`{"username":"`+name+`_`+strings.ToLower(suffix)+`","konamiId":"`+name+`-`+suffix+`-ID","password":"staff password"}`, ""),
			http.StatusCreated)
	}
	admin, operator, player := register("Admin"), register("Oper"), register("Plain")

	// A player is not staff.
	h.expect(call(http.MethodGet, "/v1/admin/me", "", player.AccessToken), http.StatusForbidden, "platform_access_denied")

	// The first admin is granted outside the API, as gamics-staff does.
	if _, err := h.server.db.Writer.Exec(t.Context(), `INSERT INTO platform_staff_roles(user_id,role) VALUES ($1,'admin')`,
		admin.Player.ID); err != nil {
		t.Fatal(err)
	}
	me := call(http.MethodGet, "/v1/admin/me", "", admin.AccessToken)
	h.expect(me, http.StatusOK, "")
	if !strings.Contains(me.Body.String(), `"role":"admin"`) || !strings.Contains(me.Body.String(), `"staff.manage"`) ||
		!strings.Contains(me.Body.String(), `"gamicsOrganizationId":"`+gamicsOrganizationID+`"`) {
		t.Fatalf("admin self = %s", me.Body)
	}
	adminOverview := call(http.MethodGet, "/v1/admin/overview", "", admin.AccessToken)
	h.expect(adminOverview, http.StatusOK, "")
	if !strings.Contains(adminOverview.Body.String(), `"finance":{`) {
		t.Fatalf("admin overview has no finance: %s", adminOverview.Body)
	}

	// Admins grant roles by Konami ID, typed however the player types it.
	granted := call(http.MethodPost, "/v1/admin/staff", `{"konamiId":"oper `+strings.ToLower(suffix)+` id","role":"reviewer"}`, admin.AccessToken)
	h.expect(granted, http.StatusOK, "")
	if !strings.Contains(granted.Body.String(), `"role":"reviewer"`) {
		t.Fatalf("staff after grant = %s", granted.Body)
	}
	h.expect(call(http.MethodPost, "/v1/admin/staff", `{"konamiId":"NOBODY-`+suffix+`-ID","role":"support"}`, admin.AccessToken),
		http.StatusNotFound, "player_not_found")
	h.expect(call(http.MethodPost, "/v1/admin/staff", `{"konamiId":"Admin-`+suffix+`-ID","role":"support"}`, admin.AccessToken),
		http.StatusConflict, "cannot_change_own_role")

	// Staff run Gamics competitions, but cannot manage staff or see money.
	gamicsCompetitions := "/v1/organizations/" + gamicsOrganizationID + "/competitions"
	h.expect(call(http.MethodGet, "/v1/admin/staff", "", operator.AccessToken), http.StatusForbidden, "platform_access_denied")
	h.expect(call(http.MethodGet, gamicsCompetitions, "", operator.AccessToken), http.StatusOK, "")
	staffOverview := call(http.MethodGet, "/v1/admin/overview", "", operator.AccessToken)
	h.expect(staffOverview, http.StatusOK, "")
	if !strings.Contains(staffOverview.Body.String(), `"finance":null`) {
		t.Fatalf("staff overview shows finance: %s", staffOverview.Body)
	}

	// A player who is not staff never reaches the Gamics organization.
	h.expect(call(http.MethodGet, gamicsCompetitions, "", player.AccessToken), http.StatusNotFound, "")
	h.expect(call(http.MethodPost, "/v1/admin/staff", `{"konamiId":"Oper-`+suffix+`-ID","role":"operator"}`, admin.AccessToken),
		http.StatusOK, "")
	// No other organization opens to staff.
	h.expect(call(http.MethodGet, "/v1/organizations/0f8e2f53-4c0b-4d7e-9a3c-6f1d2b8e4a51/competitions", "", operator.AccessToken),
		http.StatusNotFound, "")

	// Revoking takes effect on the next request.
	h.expect(call(http.MethodDelete, "/v1/admin/staff/"+operator.Player.ID, "", admin.AccessToken), http.StatusNoContent, "")
	h.expect(call(http.MethodGet, gamicsCompetitions, "", operator.AccessToken), http.StatusNotFound, "")
	h.expect(call(http.MethodGet, "/v1/admin/me", "", operator.AccessToken), http.StatusForbidden, "platform_access_denied")
}
