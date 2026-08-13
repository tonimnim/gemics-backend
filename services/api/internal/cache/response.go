package cache

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

type ResponseState string

const (
	StateHit    ResponseState = "HIT"
	StateMiss   ResponseState = "MISS"
	StateStale  ResponseState = "STALE"
	StateBypass ResponseState = "BYPASS"
)

type Policy struct {
	FreshFor     time.Duration
	KeepFor      time.Duration
	LoadTimeout  time.Duration
	LockFor      time.Duration
	WaitFor      time.Duration
	MaxBodyBytes int
}

type Response struct {
	Body  []byte
	ETag  string
	State ResponseState
}

type Loader func(context.Context) ([]byte, error)

type ResponseCache struct {
	client       *redis.Client
	namespace    string
	readTimeout  time.Duration
	writeTimeout time.Duration
	loadGate     chan struct{}
	now          func() time.Time
	group        singleflight.Group
	rootCtx      context.Context
	cancel       context.CancelFunc
	lifecycleMu  sync.Mutex
	refreshing   map[string]bool
	closed       bool
	wg           sync.WaitGroup
}

type responseEnvelope struct {
	Body       []byte `json:"body"`
	ETag       string `json:"etag"`
	FreshUntil int64  `json:"freshUntil"`
}

var releaseLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0`)

func NewResponseCache(client *redis.Client, namespace string, maxConcurrentLoads int) *ResponseCache {
	if maxConcurrentLoads < 1 {
		maxConcurrentLoads = 1
	}
	rootCtx, cancel := context.WithCancel(context.Background())
	return &ResponseCache{
		client: client, namespace: namespace, readTimeout: 50 * time.Millisecond,
		writeTimeout: 100 * time.Millisecond, loadGate: make(chan struct{}, maxConcurrentLoads),
		now: time.Now, rootCtx: rootCtx, cancel: cancel, refreshing: make(map[string]bool),
	}
}

func (cache *ResponseCache) Close() {
	if cache == nil {
		return
	}
	cache.lifecycleMu.Lock()
	if cache.closed {
		cache.lifecycleMu.Unlock()
		return
	}
	cache.closed = true
	cache.cancel()
	cache.lifecycleMu.Unlock()
	cache.wg.Wait()
}

func (cache *ResponseCache) GetOrLoad(ctx context.Context, logicalKey string, policy Policy, loader Loader) (Response, error) {
	policy = normalizePolicy(policy)
	key := cache.bodyKey(logicalKey)
	entry, found, cacheErr := cache.get(ctx, key, policy.MaxBodyBytes)
	if found {
		if cache.now().UnixMilli() < entry.FreshUntil {
			return entry.response(StateHit), nil
		}
		cache.refreshInBackground(key, policy, loader)
		return entry.response(StateStale), nil
	}

	channel := cache.group.DoChan(key, func() (any, error) {
		loadCtx, cancel := context.WithTimeout(cache.rootCtx, policy.LoadTimeout)
		defer cancel()
		return cache.load(loadCtx, key, policy, loader, cacheErr != nil)
	})
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case result := <-channel:
		if result.Err != nil {
			return Response{}, result.Err
		}
		return result.Val.(Response), nil
	}
}

func (cache *ResponseCache) refreshInBackground(key string, policy Policy, loader Loader) {
	cache.lifecycleMu.Lock()
	if cache.closed || cache.refreshing[key] {
		cache.lifecycleMu.Unlock()
		return
	}
	cache.refreshing[key] = true
	cache.wg.Add(1)
	cache.lifecycleMu.Unlock()
	go func() {
		defer func() {
			cache.lifecycleMu.Lock()
			delete(cache.refreshing, key)
			cache.lifecycleMu.Unlock()
			cache.wg.Done()
		}()
		_, _, _ = cache.group.Do(key, func() (any, error) {
			ctx, cancel := context.WithTimeout(cache.rootCtx, policy.LoadTimeout)
			defer cancel()
			return cache.load(ctx, key, policy, loader, false)
		})
	}()
}

func (cache *ResponseCache) load(ctx context.Context, key string, policy Policy, loader Loader, bypass bool) (Response, error) {
	if !bypass {
		if current, found, _ := cache.get(ctx, key, policy.MaxBodyBytes); found && cache.now().UnixMilli() < current.FreshUntil {
			return current.response(StateHit), nil
		}
	}

	lockToken := randomToken()
	haveLock := false
	if cache.client != nil && !bypass {
		lockCtx, cancel := context.WithTimeout(ctx, cache.writeTimeout)
		haveLock, _ = cache.client.SetNX(lockCtx, cache.lockKey(key), lockToken, policy.LockFor).Result()
		cancel()
		if !haveLock {
			if waited, found := cache.waitForFill(ctx, key, policy); found {
				return waited, nil
			}
		}
	}
	if haveLock {
		defer cache.releaseLock(key, lockToken)
	}

	select {
	case cache.loadGate <- struct{}{}:
		defer func() { <-cache.loadGate }()
	case <-ctx.Done():
		return Response{}, ctx.Err()
	}
	body, err := loader(ctx)
	if err != nil {
		return Response{}, err
	}
	if len(body) > policy.MaxBodyBytes {
		return Response{Body: append([]byte(nil), body...), ETag: WeakETag(body), State: StateBypass}, nil
	}
	entry := responseEnvelope{Body: append([]byte(nil), body...), ETag: WeakETag(body)}
	entry.FreshUntil = cache.now().Add(jitter(policy.FreshFor)).UnixMilli()
	if cache.client != nil && !bypass {
		cache.set(ctx, key, entry, policy.KeepFor)
	}
	state := StateMiss
	if bypass || cache.client == nil {
		state = StateBypass
	}
	return entry.response(state), nil
}

func (cache *ResponseCache) waitForFill(ctx context.Context, key string, policy Policy) (Response, bool) {
	deadline := cache.now().Add(policy.WaitFor)
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for cache.now().Before(deadline) {
		select {
		case <-ctx.Done():
			return Response{}, false
		case <-ticker.C:
			if entry, found, _ := cache.get(ctx, key, policy.MaxBodyBytes); found {
				state := StateHit
				if cache.now().UnixMilli() >= entry.FreshUntil {
					state = StateStale
				}
				return entry.response(state), true
			}
		}
	}
	return Response{}, false
}

func (cache *ResponseCache) get(parent context.Context, key string, maxBytes int) (responseEnvelope, bool, error) {
	if cache == nil || cache.client == nil {
		return responseEnvelope{}, false, errors.New("cache unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, cache.readTimeout)
	defer cancel()
	raw, err := cache.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return responseEnvelope{}, false, nil
	}
	if err != nil {
		return responseEnvelope{}, false, err
	}
	var entry responseEnvelope
	if len(raw) > maxBytes*2 || json.Unmarshal(raw, &entry) != nil || len(entry.Body) > maxBytes || entry.ETag == "" || entry.FreshUntil <= 0 {
		deleteCtx, deleteCancel := context.WithTimeout(context.Background(), cache.writeTimeout)
		_ = cache.client.Del(deleteCtx, key).Err()
		deleteCancel()
		return responseEnvelope{}, false, nil
	}
	return entry, true, nil
}

func (cache *ResponseCache) set(parent context.Context, key string, entry responseEnvelope, ttl time.Duration) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(parent, cache.writeTimeout)
	defer cancel()
	_ = cache.client.Set(ctx, key, raw, ttl).Err()
}

func (cache *ResponseCache) releaseLock(bodyKey, token string) {
	if cache.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cache.writeTimeout)
	defer cancel()
	_ = releaseLockScript.Run(ctx, cache.client, []string{cache.lockKey(bodyKey)}, token).Err()
}

func (cache *ResponseCache) bodyKey(logical string) string {
	return cache.namespace + ":rc:{" + logical + "}:body:rep-v1"
}

func (cache *ResponseCache) lockKey(bodyKey string) string { return bodyKey + ":lock" }

func (entry responseEnvelope) response(state ResponseState) Response {
	return Response{Body: append([]byte(nil), entry.Body...), ETag: entry.ETag, State: state}
}

func normalizePolicy(policy Policy) Policy {
	if policy.FreshFor <= 0 {
		policy.FreshFor = time.Minute
	}
	if policy.KeepFor <= policy.FreshFor {
		policy.KeepFor = policy.FreshFor * 2
	}
	if policy.LoadTimeout <= 0 {
		policy.LoadTimeout = 2 * time.Second
	}
	if policy.LockFor <= policy.LoadTimeout {
		policy.LockFor = policy.LoadTimeout + time.Second
	}
	if policy.WaitFor <= 0 {
		policy.WaitFor = 500 * time.Millisecond
	}
	if policy.MaxBodyBytes <= 0 {
		policy.MaxBodyBytes = 1 << 20
	}
	return policy
}

func WeakETag(body []byte) string {
	digest := sha256.Sum256(body)
	return `W/"` + base64.RawURLEncoding.EncodeToString(digest[:]) + `"`
}

func jitter(value time.Duration) time.Duration {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return value
	}
	var number uint64
	for _, part := range raw {
		number = number<<8 | uint64(part)
	}
	// Uniformly apply -10% through +10% without using shared pseudo-random state.
	fraction := float64(number%20001)/100000 - 0.1
	return time.Duration(float64(value) * (1 + fraction))
}

func randomToken() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}
