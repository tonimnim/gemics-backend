package auth

import (
	"testing"
	"time"
)

func TestAccessTokenRoundTrip(t *testing.T) {
	manager := NewTokenManager("01234567890123456789012345678901", 15*time.Minute)
	token, _, err := manager.Issue("user-1", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := manager.Parse(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.UserID != "user-1" || claims.SessionID != "session-1" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestRotatedRefreshTokenChangesHash(t *testing.T) {
	first, firstHash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := NewRefreshToken()
	if err != nil {
		t.Fatal(err)
	}
	if first == second || string(firstHash) == string(secondHash) {
		t.Fatal("refresh tokens must be unique")
	}
	if string(HashRefreshToken(first)) != string(firstHash) {
		t.Fatal("refresh token hash is not stable")
	}
}

func TestOTPHashIsEmailBound(t *testing.T) {
	one := HashOTP("secret", "one@example.com", "123456")
	two := HashOTP("secret", "two@example.com", "123456")
	if VerifyOTP(one, two) {
		t.Fatal("same code must not verify for another email")
	}
}
