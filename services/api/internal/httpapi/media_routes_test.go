package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
)

func TestEvidenceUploadRejectsVideo(t *testing.T) {
	server := &Server{config: config.Config{EvidenceMaxBytes: 8 << 20}}
	duration := 45.5
	tests := []struct {
		name     string
		input    createEvidenceInput
		wantKind string
	}{
		{name: "mp4 with duration", input: createEvidenceInput{MediaType: "video/mp4", ByteSize: 2 << 20, DurationSeconds: &duration}},
		{name: "mp4 without duration", input: createEvidenceInput{MediaType: "video/mp4", ByteSize: 2 << 20}},
		{name: "quicktime with duration", input: createEvidenceInput{MediaType: "video/quicktime", ByteSize: 2 << 20, DurationSeconds: &duration}},
		{name: "quicktime without duration", input: createEvidenceInput{MediaType: "video/quicktime", ByteSize: 2 << 20}},
		{name: "webm with duration", input: createEvidenceInput{MediaType: "video/webm", ByteSize: 2 << 20, DurationSeconds: &duration}},
		{name: "webm without duration", input: createEvidenceInput{MediaType: "video/webm", ByteSize: 2 << 20}},
		{name: "png with duration", input: createEvidenceInput{MediaType: "image/png", ByteSize: 2 << 20, DurationSeconds: &duration}},
		{name: "jpeg with duration", input: createEvidenceInput{MediaType: "image/jpeg", ByteSize: 2 << 20, DurationSeconds: &duration}},
		{name: "heic", input: createEvidenceInput{MediaType: "image/heic", ByteSize: 2 << 20}},
		{name: "png", input: createEvidenceInput{MediaType: "image/png", ByteSize: 2 << 20}, wantKind: "image"},
		{name: "jpeg", input: createEvidenceInput{MediaType: "image/jpeg", ByteSize: 2 << 20}, wantKind: "image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, message := server.validateEvidenceUploadMetadata(test.input)
			if test.wantKind == "" {
				if message != unsupportedEvidenceMediaMessage {
					t.Fatalf("message = %q, want the screenshot-only rejection", message)
				}
				return
			}
			if message != "" || policy.Kind != test.wantKind {
				t.Fatalf("screenshot rejected: policy=%+v message=%q", policy, message)
			}
		})
	}
}

func TestEvidenceUploadSizeBounds(t *testing.T) {
	tests := []struct {
		name       string
		configured int64
		byteSize   int64
		valid      bool
	}{
		{name: "empty", configured: 8 << 20, byteSize: 0},
		{name: "one byte", configured: 8 << 20, byteSize: 1, valid: true},
		{name: "configured maximum", configured: 8 << 20, byteSize: 8 << 20, valid: true},
		{name: "above configured maximum", configured: 8 << 20, byteSize: 8<<20 + 1},
		{name: "unset uses the default", byteSize: defaultImageEvidenceMaxBytes, valid: true},
		{name: "above the default", byteSize: defaultImageEvidenceMaxBytes + 1},
		{name: "configured above the hard cap", configured: 64 << 20, byteSize: 25<<20 + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{config: config.Config{EvidenceMaxBytes: test.configured}}
			_, message := server.validateEvidenceUploadMetadata(createEvidenceInput{MediaType: "image/png", ByteSize: test.byteSize})
			if (message == "") != test.valid {
				t.Fatalf("byteSize %d valid=%v, message=%q", test.byteSize, test.valid, message)
			}
		})
	}
}

func TestCreateEvidenceUploadRejectsVideoBeforeAnySideEffect(t *testing.T) {
	// Neither Redis nor storage is configured usefully: validation must answer
	// before the budget or a presigned URL is touched.
	server := &Server{db: &database.Cluster{}, evidenceStore: completionNoStorageIO{t}}
	request := httptest.NewRequest(http.MethodPost, "/v1/evidence/uploads", strings.NewReader(
		`{"mediaType":"video/mp4","byteSize":1024,"sha256":"`+strings.Repeat("ab", 32)+`","durationSeconds":12}`))
	recorder := httptest.NewRecorder()
	server.createEvidenceUpload(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "invalid_evidence_upload" || body.Message != unsupportedEvidenceMediaMessage {
		t.Fatalf("video intent answered %+v", body)
	}
}

func TestEvidenceViewDistinguishesProcessingFromReady(t *testing.T) {
	view := evidenceRecordView(evidenceRecord{ID: "evidence", MediaKind: "image", Status: "processing"})
	if view.Ready {
		t.Fatal("processing screenshot was reported ready")
	}
	view = evidenceRecordView(evidenceRecord{ID: "evidence", MediaKind: "image", Status: "completed"})
	if !view.Ready {
		t.Fatal("verified screenshot was not reported ready")
	}
}

func TestAvatarUploadPolicy(t *testing.T) {
	if extension, ok := validateAvatarUploadMetadata("image/webp", avatarMaxBytes); !ok || extension != ".webp" {
		t.Fatalf("valid WebP avatar rejected: extension=%q ok=%v", extension, ok)
	}
	if _, ok := validateAvatarUploadMetadata("video/mp4", 1024); ok {
		t.Fatal("video avatar accepted")
	}
	if _, ok := validateAvatarUploadMetadata("image/png", avatarMaxBytes+1); ok {
		t.Fatal("oversized avatar accepted")
	}
}

func TestValidUniqueUUIDList(t *testing.T) {
	first := "4d3e5536-bf3c-4dba-a643-575e43f56970"
	second := "b252e94f-a746-42c0-a54b-ff5bf289a64a"
	cases := []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "empty", values: []string{}, want: true},
		{name: "unique", values: []string{first, second}, want: true},
		{name: "duplicate", values: []string{first, first}, want: false},
		{name: "not a UUID", values: []string{first, "evidence"}, want: false},
	}
	for _, testCase := range cases {
		if got := validUniqueUUIDList(testCase.values); got != testCase.want {
			t.Errorf("%s: validUniqueUUIDList(%v) = %v, want %v", testCase.name, testCase.values, got, testCase.want)
		}
	}
}

func TestMediaRoutesAreRegistered(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := New(config.Config{RequestTimeout: time.Second}, logger, "test")
	protected := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/evidence/uploads/4d3e5536-bf3c-4dba-a643-575e43f56970"},
		{http.MethodPost, "/v1/me/avatar/uploads"},
		{http.MethodPost, "/v1/me/avatar/uploads/4d3e5536-bf3c-4dba-a643-575e43f56970/complete"},
		{http.MethodPatch, "/v1/me/avatar"},
		{http.MethodGet, "/v1/me/avatar"},
	}
	for _, item := range protected {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(item.method, item.path, nil)
		server.http.Handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s was not auth-protected/registered: status=%d", item.method, item.path, recorder.Code)
		}
	}

	publicRecorder := httptest.NewRecorder()
	publicRequest := httptest.NewRequest(http.MethodGet, "/v1/players/4d3e5536-bf3c-4dba-a643-575e43f56970/avatar", nil)
	server.http.Handler.ServeHTTP(publicRecorder, publicRequest)
	if publicRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("public avatar route was not registered: status=%d", publicRecorder.Code)
	}
}
