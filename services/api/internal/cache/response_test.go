package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestResponseCacheCoalescesLocalMisses(t *testing.T) {
	cache := NewResponseCache(nil, "test", 4)
	defer cache.Close()
	var loads atomic.Int32
	loaderStarted := make(chan struct{})
	releaseLoader := make(chan struct{})
	var once sync.Once
	loader := func(ctx context.Context) ([]byte, error) {
		loads.Add(1)
		once.Do(func() { close(loaderStarted) })
		select {
		case <-releaseLoader:
			return []byte(`{"data":[{"id":"efootball-mobile"}]}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	const callers = 100
	responses := make(chan Response, callers)
	errors := make(chan error, callers)
	for range callers {
		go func() {
			response, err := cache.GetOrLoad(t.Context(), "games", Policy{LoadTimeout: time.Second}, loader)
			responses <- response
			errors <- err
		}()
	}
	<-loaderStarted
	time.Sleep(25 * time.Millisecond)
	close(releaseLoader)
	for range callers {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
		response := <-responses
		if response.State != StateBypass || response.ETag == "" || len(response.Body) == 0 {
			t.Fatalf("unexpected response: %+v", response)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("expected one coalesced load, got %d", loads.Load())
	}
}

func TestResponseCacheReturnsOversizedSuccessfulBodyUncached(t *testing.T) {
	cache := NewResponseCache(nil, "test", 1)
	defer cache.Close()
	response, err := cache.GetOrLoad(t.Context(), "large", Policy{MaxBodyBytes: 2}, func(context.Context) ([]byte, error) {
		return []byte("large"), nil
	})
	if err != nil || string(response.Body) != "large" || response.State != StateBypass {
		t.Fatalf("unexpected response: %+v, %v", response, err)
	}
}

func TestWeakETagAndJitter(t *testing.T) {
	if WeakETag([]byte("same")) != WeakETag([]byte("same")) || WeakETag([]byte("same")) == WeakETag([]byte("different")) {
		t.Fatal("ETags must be stable and content-addressed")
	}
	base := time.Hour
	for range 1000 {
		value := jitter(base)
		if value < 54*time.Minute || value > 66*time.Minute {
			t.Fatalf("jitter outside +/-10%%: %s", value)
		}
	}
}
