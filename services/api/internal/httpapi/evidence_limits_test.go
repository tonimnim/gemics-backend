package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
)

func TestUploadLimitsFailClosedWithoutRedis(t *testing.T) {
	s := &Server{}
	tests := []struct {
		name  string
		check func(http.ResponseWriter, *http.Request) bool
		code  string
	}{
		{name: "evidence upload", check: func(w http.ResponseWriter, r *http.Request) bool { return s.allowEvidenceUpload(w, r, 1024) }, code: "evidence_limits_unavailable"},
		{name: "evidence action", check: s.allowEvidenceAction, code: "evidence_limits_unavailable"},
		{name: "avatar upload", check: func(w http.ResponseWriter, r *http.Request) bool { return s.allowAvatarUpload(w, r, 1024) }, code: "avatar_limits_unavailable"},
		{name: "avatar action", check: s.allowAvatarAction, code: "avatar_limits_unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			if test.check(recorder, httptest.NewRequest(http.MethodPost, "/v1/uploads", nil)) {
				t.Fatal("upload budget bypassed without Redis")
			}
			assertMediaError(t, recorder, http.StatusServiceUnavailable, test.code)
			if recorder.Header().Get("Retry-After") != "5" {
				t.Fatalf("temporary outage lacks retry guidance: %q", recorder.Header().Get("Retry-After"))
			}
		})
	}
}

func TestUploadBudgetsUseSeparateHashTaggedKeys(t *testing.T) {
	s := &Server{config: config.Config{CacheNamespace: "test:v1"}}
	request := httptest.NewRequest(http.MethodPost, "/v1/uploads", nil)
	request = request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: "user-1"}))
	tests := []struct {
		name   string
		budget uploadBudget
		want   string
	}{
		{name: "evidence", budget: s.evidenceUploadBudget(), want: "test:v1:security:evidence:{user-1}:"},
		{name: "avatar", budget: s.avatarUploadBudget(), want: "test:v1:security:avatar:{user-1}:"},
	}
	for _, test := range tests {
		if got := s.uploadBudgetKey(request, test.budget); got != test.want {
			t.Errorf("%s budget key = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestAvatarHandlersEnforceLimitsBeforeStorageOrDatabase(t *testing.T) {
	// A nil Redis client fails every limit closed. The storage fake fails the
	// test on any call, and the empty cluster would panic on a query.
	s := &Server{db: &database.Cluster{}, evidenceStore: completionNoStorageIO{t}}
	create := httptest.NewRequest(http.MethodPost, "/v1/me/avatar/uploads", strings.NewReader(
		`{"mediaType":"image/png","byteSize":1024,"sha256":"`+strings.Repeat("ab", 32)+`"}`))
	recorder := httptest.NewRecorder()
	s.createAvatarUpload(recorder, create)
	assertMediaError(t, recorder, http.StatusServiceUnavailable, "avatar_limits_unavailable")

	id := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	complete := httptest.NewRequest(http.MethodPost, "/v1/me/avatar/uploads/"+id+"/complete", nil)
	complete.SetPathValue("id", id)
	recorder = httptest.NewRecorder()
	s.completeAvatarUpload(recorder, complete)
	assertMediaError(t, recorder, http.StatusServiceUnavailable, "avatar_limits_unavailable")
}

func TestEvidenceIntentEnforcesBudgetBeforePresign(t *testing.T) {
	s := &Server{db: &database.Cluster{}, evidenceStore: completionNoStorageIO{t}}
	request := httptest.NewRequest(http.MethodPost, "/v1/evidence/uploads", strings.NewReader(
		`{"mediaType":"image/jpeg","byteSize":1024,"sha256":"`+strings.Repeat("ab", 32)+`"}`))
	recorder := httptest.NewRecorder()
	s.createEvidenceUpload(recorder, request)
	assertMediaError(t, recorder, http.StatusServiceUnavailable, "evidence_limits_unavailable")
}

func TestHEICScreenshotsRequireClientConversion(t *testing.T) {
	s := &Server{}
	for _, mediaType := range []string{"image/heic", "image/heif", "image/svg+xml", "text/html"} {
		if _, message := s.validateEvidenceUploadMetadata(createEvidenceInput{MediaType: mediaType, ByteSize: 1024}); message == "" {
			t.Fatalf("unsupported decoder accepted %s", mediaType)
		}
	}
}

func assertMediaError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if recorder.Code != status || body.Error != code {
		t.Fatalf("response = %d %q, want %d %q", recorder.Code, body.Error, status, code)
	}
}
