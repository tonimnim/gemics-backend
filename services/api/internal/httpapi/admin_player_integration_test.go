package httpapi

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestIntegrationStaffFindAndSuspendPlayers covers the players page: every
// staff role can search players, only operators and admins suspend, a
// suspension ends the player's sessions and blocks sign-in, and reactivation
// lets them sign in again.
func TestIntegrationStaffFindAndSuspendPlayers(t *testing.T) {
	h := newRegistrationHarness(t)
	mux := http.NewServeMux()
	h.server.registerAdminPlayerRoutes(mux)
	h.server.registerAdminRoutes(mux)
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
			`{"username":"`+name+`_`+strings.ToLower(suffix)+`","konamiId":"`+name+`-`+suffix+`-ID","password":"player password"}`, ""),
			http.StatusCreated)
	}
	operator, support, target := register("Oper"), register("Supp"), register("Target")
	if _, err := h.server.db.Writer.Exec(t.Context(), `INSERT INTO platform_staff_roles(user_id,role)
		VALUES ($1,'operator'),($2,'support')`, operator.Player.ID, support.Player.ID); err != nil {
		t.Fatal(err)
	}

	// Plain players never see the list.
	h.expect(call(http.MethodGet, "/v1/admin/players", "", target.AccessToken), http.StatusForbidden, "platform_access_denied")

	// Support finds the player by Konami ID, typed loosely.
	found := call(http.MethodGet, "/v1/admin/players?q=target+"+strings.ToLower(suffix), "", support.AccessToken)
	h.expect(found, http.StatusOK, "")
	var list struct {
		Data []adminPlayer `json:"data"`
	}
	if err := json.Unmarshal(found.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != target.Player.ID || list.Data[0].Status != "active" {
		t.Fatalf("search by Konami ID = %s", found.Body)
	}
	// A LIKE wildcard is searched literally.
	wildcard := call(http.MethodGet, "/v1/admin/players?q=%25", "", support.AccessToken)
	h.expect(wildcard, http.StatusOK, "")
	if strings.Contains(wildcard.Body.String(), target.Player.ID) {
		t.Fatal("a % search matched every player")
	}
	h.expect(call(http.MethodGet, "/v1/admin/players/"+target.Player.ID, "", support.AccessToken), http.StatusOK, "")

	// Support cannot suspend; reasons are required; staff are protected.
	reason := `{"reason":"Repeated abuse in match chat."}`
	h.expect(call(http.MethodPost, "/v1/admin/players/"+target.Player.ID+"/suspension", reason, support.AccessToken),
		http.StatusForbidden, "platform_access_denied")
	h.expect(call(http.MethodPost, "/v1/admin/players/"+target.Player.ID+"/suspension", `{"reason":"short"}`, operator.AccessToken),
		http.StatusBadRequest, "invalid_reason")
	h.expect(call(http.MethodPost, "/v1/admin/players/"+support.Player.ID+"/suspension", reason, operator.AccessToken),
		http.StatusConflict, "player_is_staff")

	// Suspension ends the player's session and blocks sign-in.
	suspended := call(http.MethodPost, "/v1/admin/players/"+target.Player.ID+"/suspension", reason, operator.AccessToken)
	h.expect(suspended, http.StatusOK, "")
	if !strings.Contains(suspended.Body.String(), `"status":"suspended"`) ||
		!strings.Contains(suspended.Body.String(), `"reason":"Repeated abuse in match chat."`) {
		t.Fatalf("suspended player = %s", suspended.Body)
	}
	h.expect(call(http.MethodGet, "/v1/admin/players", "", target.AccessToken), http.StatusUnauthorized, "")
	login := `{"konamiId":"Target-` + suffix + `-ID","password":"player password"}`
	h.expect(h.call(h.server.login, http.MethodPost, "/v1/auth/login", login, ""), http.StatusForbidden, "account_unavailable")
	h.expect(call(http.MethodPost, "/v1/admin/players/"+target.Player.ID+"/suspension", reason, operator.AccessToken),
		http.StatusConflict, "player_status_conflict")

	// Reactivation lets them back in.
	h.expect(call(http.MethodPost, "/v1/admin/players/"+target.Player.ID+"/reactivation",
		`{"reason":"Appeal accepted after review."}`, operator.AccessToken), http.StatusOK, "")
	h.expect(h.call(h.server.login, http.MethodPost, "/v1/auth/login", login, ""), http.StatusOK, "")
}
