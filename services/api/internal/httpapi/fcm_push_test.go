package httpapi

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

// fcmTestServer serves the OAuth token endpoint and the FCM send endpoint. It
// checks the service account's signed assertion, and answers each send with
// the handler given.
func fcmTestServer(t *testing.T, send http.HandlerFunc) (*fcmClient, *atomic.Int32) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var minted atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
				t.Errorf("grant_type = %q", r.FormValue("grant_type"))
			}
			parts := strings.Split(r.FormValue("assertion"), ".")
			if len(parts) != 3 {
				t.Fatalf("assertion has %d parts", len(parts))
			}
			signature, _ := base64.RawURLEncoding.DecodeString(parts[2])
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
				t.Error("the assertion is not signed by the service account key")
			}
			claimsJSON, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims map[string]any
			_ = json.Unmarshal(claimsJSON, &claims)
			if claims["iss"] != "push@ozone-test.iam.gserviceaccount.com" || claims["scope"] != fcmScope ||
				claims["aud"] != server.URL+"/token" {
				t.Errorf("assertion claims = %v", claims)
			}
			minted.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access-1", "expires_in": 3600})
		case "/v1/projects/ozone-test/messages:send":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			send(w, r)
		default:
			t.Errorf("unexpected request to %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	account := &config.FCMServiceAccount{ProjectID: "ozone-test", ClientEmail: "push@ozone-test.iam.gserviceaccount.com",
		TokenURI: server.URL + "/token", PrivateKey: key}
	return newFCMClient(account, server.URL, 5*time.Second), &minted
}

func TestFCMClientSendsWithACachedAccessToken(t *testing.T) {
	client, minted := fcmTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var request fcmRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		message := request.Message
		if message.Token != "installation-token" || message.Notification.Title != "New match message" ||
			message.Data["matchId"] != "m-1" || message.Android.Priority != "HIGH" ||
			message.Android.Notification.Tag != "chat:m-1" || message.APNS.Headers["apns-collapse-id"] != "chat:m-1" ||
			message.APNS.Headers["apns-priority"] != "10" {
			t.Errorf("message = %+v", message)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "projects/ozone-test/messages/0:1"})
	})
	message := fcmMessage{Token: "installation-token", Title: "New match message", Body: "Pat sent you a message.",
		Data: map[string]string{"kind": "match.chat_message", "matchId": "m-1"}, CollapseKey: "chat:m-1"}
	for range 2 {
		if result := client.Send(context.Background(), message); result.Code != "" || result.MessageID != "projects/ozone-test/messages/0:1" {
			t.Fatalf("send = %+v", result)
		}
	}
	if minted.Load() != 1 {
		t.Fatalf("minted %d access tokens for two sends, want 1", minted.Load())
	}
	// A token close to expiry is renewed before it is used.
	client.now = func() time.Time { return time.Now().Add(56 * time.Minute) }
	client.Send(context.Background(), message)
	if minted.Load() != 2 {
		t.Fatalf("minted %d access tokens, want a renewal", minted.Load())
	}
}

func TestFCMClientClassifiesErrors(t *testing.T) {
	cases := []struct {
		name                    string
		status                  int
		errorStatus, fcmCode    string
		message, retryAfter     string
		code                    string
		permanent, unregistered bool
		wait                    time.Duration
	}{
		{name: "unregistered", status: 404, errorStatus: "NOT_FOUND", fcmCode: "UNREGISTERED", code: "fcm_unregistered",
			permanent: true, unregistered: true},
		{name: "other project", status: 403, errorStatus: "PERMISSION_DENIED", fcmCode: "SENDER_ID_MISMATCH",
			code: "fcm_sender_id_mismatch", permanent: true, unregistered: true},
		{name: "bad token", status: 400, errorStatus: "INVALID_ARGUMENT", fcmCode: "INVALID_ARGUMENT",
			message: "The registration token is not a valid FCM registration token", code: "fcm_invalid_argument",
			permanent: true, unregistered: true},
		{name: "bad payload", status: 400, errorStatus: "INVALID_ARGUMENT", fcmCode: "INVALID_ARGUMENT",
			message: "Invalid value at 'message.data'", code: "fcm_invalid_argument", permanent: true},
		{name: "quota", status: 429, errorStatus: "RESOURCE_EXHAUSTED", fcmCode: "QUOTA_EXCEEDED", retryAfter: "30",
			code: "fcm_quota_exceeded", wait: 30 * time.Second},
		{name: "unavailable", status: 503, errorStatus: "UNAVAILABLE", fcmCode: "UNAVAILABLE", code: "fcm_unavailable"},
		{name: "apns key", status: 401, errorStatus: "UNAUTHENTICATED", fcmCode: "THIRD_PARTY_AUTH_ERROR",
			code: "fcm_third_party_auth_error"},
		{name: "no body", status: 500, code: "fcm_http_500"},
	}
	for _, test := range cases {
		client, _ := fcmTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
			if test.retryAfter != "" {
				w.Header().Set("Retry-After", test.retryAfter)
			}
			w.WriteHeader(test.status)
			if test.errorStatus != "" {
				_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(test.status) + `,"message":"` + test.message +
					`","status":"` + test.errorStatus + `","details":[{"@type":"type.googleapis.com/google.firebase.fcm.v1.FcmError",` +
					`"errorCode":"` + test.fcmCode + `"}]}}`))
			}
		})
		result := client.Send(context.Background(), fcmMessage{Token: "installation-token", Title: "t", Body: "b"})
		if result.Code != test.code || result.Permanent != test.permanent || result.Unregistered != test.unregistered ||
			result.RetryAfter != test.wait || result.MessageID != "" {
			t.Errorf("%s: %+v", test.name, result)
		}
		if strings.Contains(result.Code, "installation-token") || strings.Contains(result.Code, "access-1") {
			t.Errorf("%s: the code leaks a token: %s", test.name, result.Code)
		}
	}
}

func TestFCMClientRenewsARefusedAccessToken(t *testing.T) {
	var sends atomic.Int32
	client, minted := fcmTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if sends.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"status":"UNAUTHENTICATED"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "projects/ozone-test/messages/2"})
	})
	message := fcmMessage{Token: "installation-token", Title: "t", Body: "b"}
	if result := client.Send(context.Background(), message); result.Code != "fcm_unauthenticated" || result.Permanent {
		t.Fatalf("refused send = %+v", result)
	}
	if result := client.Send(context.Background(), message); result.Code != "" || minted.Load() != 2 {
		t.Fatalf("retry = %+v after %d mints", result, minted.Load())
	}
}

func TestPushResolutionFollowsFCM(t *testing.T) {
	claim := notificationPushClaim{DeliveryID: testNotificationEventID, Attempts: 1}
	if resolution := resolveNotificationPush(claim, fcmSendResult{MessageID: "projects/p/messages/1"}, 8); resolution.State != "delivered" ||
		resolution.MessageID != "projects/p/messages/1" {
		t.Fatalf("accepted push = %+v", resolution)
	}
	if resolution := resolveNotificationPush(claim, fcmSendResult{Code: "fcm_unregistered", Permanent: true, Unregistered: true}, 8); resolution.State != "permanent_failure" ||
		!resolution.RevokeRegisteredToken {
		t.Fatalf("unregistered push = %+v", resolution)
	}
	resolution := resolveNotificationPush(claim, fcmSendResult{Code: "fcm_quota_exceeded", RetryAfter: 20 * time.Minute}, 8)
	if resolution.State != "retry" || resolution.RevokeRegisteredToken || resolution.RetryAfter != 20*time.Minute {
		t.Fatalf("rate-limited push = %+v", resolution)
	}
	if resolution = resolveNotificationPush(claim, fcmSendResult{Code: "fcm_unavailable"}, 8); resolution.RetryAfter <= 0 {
		t.Fatalf("unavailable push = %+v", resolution)
	}
	claim.Attempts = 8
	if resolution = resolveNotificationPush(claim, fcmSendResult{Code: "fcm_unavailable"}, 8); resolution.State != "permanent_failure" {
		t.Fatalf("last attempt = %+v", resolution)
	}
}

func TestPushClaimBecomesAnFCMMessage(t *testing.T) {
	action := "/matches/" + testNotificationMatchID + "/chat"
	claim := notificationPushClaim{NotificationID: "n-1", PushToken: "installation-token", Title: "New match message",
		Body: "Pat sent you a message.", ActionURL: &action,
		Data: json.RawMessage(`{"kind":"match.chat_message","matchId":"` + testNotificationMatchID + `"}`)}
	message := claim.fcmMessage()
	if message.Token != "installation-token" || message.Data["notificationId"] != "n-1" || message.Data["actionUrl"] != action ||
		message.Data["kind"] != "match.chat_message" || message.CollapseKey != "chat:"+testNotificationMatchID {
		t.Fatalf("chat push = %+v", message)
	}
	claim.Data = json.RawMessage(`{"kind":"match.ready","matchId":"` + testNotificationMatchID + `"}`)
	if message = claim.fcmMessage(); message.CollapseKey != "" {
		t.Fatalf("only chat pushes collapse: %+v", message)
	}
}

func TestPushLeaseCoversTheWholeBatch(t *testing.T) {
	server := &Server{config: config.Config{NotificationPushBatch: 100, FCMConcurrency: 8, FCMTimeout: 10 * time.Second}}
	// 100 sends, 8 at a time, is 13 rounds of up to 10 s each.
	if lease := server.notificationPushLease(); lease < 130*time.Second {
		t.Fatalf("lease %s is shorter than a batch can take", lease)
	}
}
