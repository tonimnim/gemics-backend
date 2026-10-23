package httpapi

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/config"
)

// Push notifications go through Firebase Cloud Messaging's HTTP v1 API
// (docs/push-notifications.md). The service account signs a JWT and trades it
// for an access token that lasts an hour; each push is one request, which FCM
// accepts or refuses straight away.

const (
	fcmScope = "https://www.googleapis.com/auth/firebase.messaging"
	// fcmTokenRefreshLead renews the access token this long before it expires.
	fcmTokenRefreshLead = 5 * time.Minute
)

type fcmClient struct {
	account    *config.FCMServiceAccount
	sendURL    string
	httpClient *http.Client
	now        func() time.Time

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

func newFCMClient(account *config.FCMServiceAccount, apiURL string, timeout time.Duration) *fcmClient {
	return &fcmClient{
		account:    account,
		sendURL:    strings.TrimRight(apiURL, "/") + "/v1/projects/" + url.PathEscape(account.ProjectID) + "/messages:send",
		httpClient: &http.Client{Timeout: timeout},
		now:        time.Now,
	}
}

// fcmMessage is one push to one installation. Data values are strings, as FCM
// requires. CollapseKey makes a newer push replace an older one on the device,
// such as successive messages of one chat.
type fcmMessage struct {
	Token       string
	Title       string
	Body        string
	Data        map[string]string
	CollapseKey string
}

// fcmSendResult is FCM's answer: a message name when it accepted the push, or a
// normalized error code. Unregistered means the token is dead and the
// installation must register again.
type fcmSendResult struct {
	MessageID    string
	Code         string
	Permanent    bool
	Unregistered bool
	RetryAfter   time.Duration
}

type fcmRequest struct {
	Message fcmWireMessage `json:"message"`
}

type fcmWireMessage struct {
	Token        string            `json:"token"`
	Notification fcmNotification   `json:"notification"`
	Data         map[string]string `json:"data,omitempty"`
	Android      fcmAndroid        `json:"android"`
	APNS         fcmAPNS           `json:"apns"`
}

type fcmNotification struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type fcmAndroid struct {
	Priority     string                 `json:"priority"`
	CollapseKey  string                 `json:"collapse_key,omitempty"`
	Notification fcmAndroidNotification `json:"notification"`
}

type fcmAndroidNotification struct {
	Sound string `json:"sound"`
	Tag   string `json:"tag,omitempty"`
}

type fcmAPNS struct {
	Headers map[string]string `json:"headers"`
	Payload fcmAPNSPayload    `json:"payload"`
}

type fcmAPNSPayload struct {
	APS fcmAPS `json:"aps"`
}

type fcmAPS struct {
	Sound string `json:"sound"`
}

type fcmErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type      string `json:"@type"`
			ErrorCode string `json:"errorCode"`
		} `json:"details"`
	} `json:"error"`
}

// Send delivers one push. It never returns the response body or the token in
// an error code, so codes are safe to store and log.
func (client *fcmClient) Send(ctx context.Context, message fcmMessage) fcmSendResult {
	accessToken, err := client.token(ctx)
	if err != nil {
		return fcmSendResult{Code: "fcm_auth_failed"}
	}
	wire := fcmRequest{Message: fcmWireMessage{
		Token:        message.Token,
		Notification: fcmNotification{Title: message.Title, Body: message.Body},
		Data:         message.Data,
		Android: fcmAndroid{Priority: "HIGH", CollapseKey: message.CollapseKey,
			Notification: fcmAndroidNotification{Sound: "default", Tag: message.CollapseKey}},
		APNS: fcmAPNS{Headers: map[string]string{"apns-priority": "10"}, Payload: fcmAPNSPayload{APS: fcmAPS{Sound: "default"}}},
	}}
	if message.CollapseKey != "" {
		wire.Message.APNS.Headers["apns-collapse-id"] = message.CollapseKey
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return fcmSendResult{Code: "fcm_encode_request", Permanent: true}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.sendURL, bytes.NewReader(body))
	if err != nil {
		return fcmSendResult{Code: "fcm_invalid_endpoint", Permanent: true}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)
	response, err := client.httpClient.Do(request)
	if err != nil {
		return fcmSendResult{Code: "fcm_transport_error"}
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, notificationPushResponseLimit))
	if response.StatusCode == http.StatusOK {
		var accepted struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &accepted) != nil || strings.TrimSpace(accepted.Name) == "" {
			return fcmSendResult{Code: "fcm_invalid_response"}
		}
		return fcmSendResult{MessageID: strings.TrimSpace(accepted.Name)}
	}
	return client.classify(response, raw)
}

// classify maps an FCM error to a stored code and what to do next
// (https://firebase.google.com/docs/reference/fcm/rest/v1/ErrorCode).
func (client *fcmClient) classify(response *http.Response, raw []byte) fcmSendResult {
	var failure fcmErrorResponse
	_ = json.Unmarshal(raw, &failure)
	errorCode := failure.Error.Status
	for _, detail := range failure.Error.Details {
		if strings.HasSuffix(detail.Type, "google.firebase.fcm.v1.FcmError") && detail.ErrorCode != "" {
			errorCode = detail.ErrorCode
		}
	}
	if errorCode == "" {
		errorCode = "http_" + strconv.Itoa(response.StatusCode)
	}
	result := fcmSendResult{Code: "fcm_" + strings.ToLower(errorCode), RetryAfter: retryAfter(response.Header.Get("Retry-After"))}
	if len(result.Code) > 128 {
		result.Code = result.Code[:128]
	}
	switch {
	case errorCode == "UNREGISTERED" || errorCode == "SENDER_ID_MISMATCH":
		result.Permanent, result.Unregistered = true, true
	case errorCode == "INVALID_ARGUMENT":
		// A malformed token is the installation's fault; any other invalid
		// argument is ours, and must not cost the player their registration.
		result.Permanent = true
		result.Unregistered = strings.Contains(strings.ToLower(failure.Error.Message), "registration token")
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		// The access token was refused: mint a new one on the next attempt.
		client.mu.Lock()
		client.accessToken = ""
		client.mu.Unlock()
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 ||
		errorCode == "THIRD_PARTY_AUTH_ERROR":
		// Retried with backoff. THIRD_PARTY_AUTH_ERROR is a missing or expired
		// APNs key in the Firebase console, which staff can fix.
	default:
		result.Permanent = response.StatusCode >= 400 && response.StatusCode < 500
	}
	return result
}

func retryAfter(value string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds <= 0 {
		return 0
	}
	return min(time.Duration(seconds)*time.Second, notificationPushRetryMaximum)
}

// token returns a cached access token, minting a new one when it is about to
// expire.
func (client *fcmClient) token(ctx context.Context) (string, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	now := client.now()
	if client.accessToken != "" && now.Add(fcmTokenRefreshLead).Before(client.expiresAt) {
		return client.accessToken, nil
	}
	assertion, err := client.assertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.account.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.httpClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var granted struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if response.StatusCode != http.StatusOK ||
		json.NewDecoder(io.LimitReader(response.Body, notificationPushResponseLimit)).Decode(&granted) != nil ||
		granted.AccessToken == "" || granted.ExpiresIn <= 0 {
		return "", errors.New("the Firebase access token request was refused")
	}
	client.accessToken = granted.AccessToken
	client.expiresAt = now.Add(time.Duration(granted.ExpiresIn) * time.Second)
	return client.accessToken, nil
}

// assertion is the service account's signed JWT asking for the FCM scope.
func (client *fcmClient) assertion(now time.Time) (string, error) {
	claims, err := json.Marshal(map[string]any{
		"iss": client.account.ClientEmail, "scope": fcmScope, "aud": client.account.TokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, client.account.PrivateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
