package httpapi

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/redis/go-redis/v9"
)

// evidenceLimitsRedis connects to the test Redis, skipping without one.
func evidenceLimitsRedis(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("GAMICS_TEST_REDIS_URL")
	if raw == "" {
		t.Skip("GAMICS_TEST_REDIS_URL not configured")
	}
	options, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal("invalid test Redis URL")
	}
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	if err = client.Ping(t.Context()).Err(); err != nil {
		t.Fatal("test Redis unavailable")
	}
	return client
}

func TestEvidenceBudgetConcurrentReservations(t *testing.T) {
	client := evidenceLimitsRedis(t)
	ctx := context.Background()
	for _, test := range []struct {
		name                         string
		count, size, budget, allowed int
	}{
		{"count", 10, 1, 100, 10}, {"bytes", 100, 2, 6, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "test:evidence:{" + rand.Text() + "}:"
			keys := []string{prefix + "count", prefix + "bytes"}
			defer client.Del(ctx, keys...)
			var group sync.WaitGroup
			var accepted atomic.Int64
			for range 64 {
				group.Add(1)
				go func() {
					defer group.Done()
					retry, err := evidenceBudgetScript.Run(ctx, client, keys, test.count, test.size, test.budget).Int64()
					if err != nil {
						t.Error(err)
					} else if retry == 0 {
						accepted.Add(1)
					}
				}()
			}
			group.Wait()
			if accepted.Load() != int64(test.allowed) {
				t.Fatalf("budget race: got %d want %d", accepted.Load(), test.allowed)
			}
			bytes, err := client.Get(ctx, keys[1]).Int64()
			if err != nil || bytes != int64(test.allowed*test.size) {
				t.Fatalf("rejected reservation consumed bytes: %d %v", bytes, err)
			}
			for _, key := range keys {
				if ttl, err := client.TTL(ctx, key).Result(); err != nil || ttl <= 0 {
					t.Fatalf("budget lacks expiry: %v %v", ttl, err)
				}
			}
		})
	}
}

func TestIntegrationAvatarBudgetIndependentOfEvidence(t *testing.T) {
	client := evidenceLimitsRedis(t)
	s := &Server{redis: client, config: config.Config{
		CacheNamespace:    "test:" + rand.Text(),
		AvatarUploadLimit: 2, AvatarDailyBytes: 3 << 20,
		EvidenceUploadLimit: 30, EvidenceDailyBytes: 64 << 20, EvidenceActionLimit: 2,
	}}
	userID := "20000000-0000-4000-8000-000000000001"
	request := httptest.NewRequest(http.MethodPost, "/v1/me/avatar/uploads", nil)
	request = request.WithContext(context.WithValue(request.Context(), identityContextKey{}, identity{UserID: userID}))
	t.Cleanup(func() {
		var keys []string
		for _, budget := range []uploadBudget{s.evidenceUploadBudget(), s.avatarUploadBudget()} {
			prefix := s.uploadBudgetKey(request, budget)
			keys = append(keys, prefix+"intents", prefix+"bytes", prefix+"actions")
		}
		// Only this test's randomly namespaced keys.
		if err := client.Del(context.Background(), keys...).Err(); err != nil {
			t.Error(err)
		}
	})
	reserveAvatar := func(byteSize int64) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		s.allowAvatarUpload(recorder, request, byteSize)
		return recorder
	}

	if w := reserveAvatar(2 << 20); w.Code != http.StatusOK {
		t.Fatalf("first avatar intent refused: %d", w.Code)
	}
	w := reserveAvatar(2 << 20)
	assertMediaError(t, w, http.StatusTooManyRequests, "avatar_rate_limited")
	assertEvidenceLimitsRetryAfter(t, w)
	if w = reserveAvatar(1 << 20); w.Code != http.StatusOK {
		t.Fatalf("a refused byte reservation consumed the avatar budget: %d", w.Code)
	}
	w = reserveAvatar(1)
	assertMediaError(t, w, http.StatusTooManyRequests, "avatar_rate_limited")
	if !s.allowEvidenceUpload(httptest.NewRecorder(), request, 10<<20) {
		t.Fatal("an exhausted avatar budget blocked evidence uploads")
	}

	for range 2 {
		if !s.allowAvatarAction(httptest.NewRecorder(), request) {
			t.Fatal("avatar completion refused within the action limit")
		}
	}
	w = httptest.NewRecorder()
	if s.allowAvatarAction(w, request) {
		t.Fatal("avatar completion exceeded the action limit")
	}
	assertMediaError(t, w, http.StatusTooManyRequests, "avatar_rate_limited")
	if !s.allowEvidenceAction(httptest.NewRecorder(), request) {
		t.Fatal("avatar completions spent the evidence action limit")
	}
}

func assertEvidenceLimitsRetryAfter(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	seconds, err := strconv.Atoi(recorder.Header().Get("Retry-After"))
	if err != nil || seconds < 1 {
		t.Fatalf("rate-limited response lacks a positive Retry-After: %q", recorder.Header().Get("Retry-After"))
	}
}
