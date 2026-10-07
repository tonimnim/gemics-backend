package mpesa

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestInitiateCachesTokenAndBuildsDarajaRequest(t *testing.T) {
	var tokenCalls atomic.Int32
	var stkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/v1/generate":
			tokenCalls.Add(1)
			user, password, ok := r.BasicAuth()
			if !ok || user != "key" || password != "secret" {
				t.Error("missing OAuth basic credentials")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "token", "expires_in": "3599"})
		case "/mpesa/stkpush/v1/processrequest":
			stkCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Error("missing bearer token")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			expectedPassword := base64.StdEncoding.EncodeToString([]byte("174379passkey20260809142233"))
			// 11:22:33 UTC is 14:22:33 in Nairobi, the clock Daraja expects.
			if body["Password"] != expectedPassword || body["Timestamp"] != "20260809142233" ||
				body["Amount"].(float64) != 250 || body["PhoneNumber"] != "254712345678" {
				t.Errorf("unexpected STK request: %+v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MerchantRequestID": "merchant-1", "CheckoutRequestID": "checkout-1",
				"ResponseCode": "0", "ResponseDescription": "Success", "CustomerMessage": "Check phone",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := New(Config{Environment: "sandbox", ConsumerKey: "key", ConsumerSecret: "secret",
		ShortCode: "174379", Passkey: "passkey", CallbackURL: "https://example.com/callback/token",
		TransactionType: "CustomerPayBillOnline", BaseURL: server.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return time.Date(2026, 8, 9, 11, 22, 33, 0, time.UTC) }
	for range 2 {
		response, err := client.Initiate(t.Context(), InitiateRequest{PhoneNumber: "254712345678", AmountKES: 250, AccountReference: "GM123", Description: "TournamentFee"})
		if err != nil || response.CheckoutRequestID != "checkout-1" {
			t.Fatalf("unexpected response: %+v %v", response, err)
		}
	}
	if tokenCalls.Load() != 1 || stkCalls.Load() != 2 {
		t.Fatalf("unexpected calls: token=%d stk=%d", tokenCalls.Load(), stkCalls.Load())
	}
}

func TestDarajaCodesAcceptStringsAndNumbers(t *testing.T) {
	for _, raw := range []string{
		`{"ResponseCode":"0","ResultCode":"1032"}`,
		`{"ResponseCode":0,"ResultCode":1032}`,
	} {
		var response QueryResponse
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if response.ResponseCode != "0" || response.ResultCode != "1032" {
			t.Fatalf("unexpected Daraja codes: %+v", response)
		}
	}
}
