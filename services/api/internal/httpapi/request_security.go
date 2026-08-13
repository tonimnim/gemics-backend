package httpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var fixedWindowScript = redis.NewScript(`
local current = redis.call('INCR', KEYS[1])
if current == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return current`)

const maxPositiveSessionCacheTTL = 5 * time.Second

func redisFixedWindow(ctx context.Context, client *redis.Client, key string, limit int, window time.Duration) (bool, error) {
	if client == nil {
		return false, errors.New("Redis unavailable")
	}
	milliseconds := max(int64(1), window.Milliseconds())
	count, err := fixedWindowScript.Run(ctx, client, []string{key}, milliseconds).Int64()
	return count <= int64(limit), err
}

func (s *Server) sessionActive(ctx context.Context, userID, sessionID string, accessExpiresAt time.Time) (bool, error) {
	if s.redis != nil {
		legacyRevoked, legacyErr := s.redis.Exists(ctx, "auth:revoked:"+sessionID).Result()
		if legacyErr == nil && legacyRevoked > 0 {
			return false, nil
		}
		state, stateErr := s.redis.Get(ctx, s.securityKey("auth:session:"+sessionID)).Result()
		if legacyErr == nil && (stateErr == nil || errors.Is(stateErr, redis.Nil)) {
			switch state {
			case "active":
				return true, nil
			case "revoked":
				return false, nil
			}
		}
	}
	if s.db == nil {
		return false, errors.New("database unavailable")
	}
	var active bool
	err := s.db.Writer.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM refresh_sessions session
		JOIN users player ON player.id=session.user_id
		WHERE session.id=$1 AND session.user_id=$2 AND session.revoked_at IS NULL AND player.status='active'
	)`, sessionID, userID).Scan(&active)
	if err != nil || !active || s.redis == nil {
		return active, err
	}
	ttl := minDuration(s.config.AuthSessionCacheTTL, maxPositiveSessionCacheTTL, time.Until(accessExpiresAt))
	if ttl > 0 {
		_ = s.redis.Set(ctx, s.securityKey("auth:session:"+sessionID), "active", ttl).Err()
	}
	return true, nil
}

func (s *Server) securityKey(suffix string) string {
	prefix := strings.TrimSuffix(s.config.CacheNamespace, ":")
	if prefix == "" {
		prefix = "gamics:development:v1"
	}
	return prefix + ":security:" + suffix
}

func (s *Server) clientIP(r *http.Request) string {
	peer, ok := parseRequestIP(r.RemoteAddr)
	if !ok {
		return "unknown"
	}
	if !s.trustedProxy(peer) {
		return peer.String()
	}
	current := peer
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for index := len(forwarded) - 1; index >= 0 && s.trustedProxy(current); index-- {
		candidate, valid := parseRequestIP(strings.TrimSpace(forwarded[index]))
		if !valid {
			continue
		}
		current = candidate
	}
	return current.String()
}

func (s *Server) trustedProxy(address netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseRequestIP(raw string) (netip.Addr, bool) {
	raw = strings.Trim(strings.TrimSpace(raw), `"`)
	if address, err := netip.ParseAddr(raw); err == nil {
		return address.Unmap(), true
	}
	if addressPort, err := netip.ParseAddrPort(raw); err == nil {
		return addressPort.Addr().Unmap(), true
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		return netip.Addr{}, false
	}
	address, err := netip.ParseAddr(strings.Trim(host, "[]"))
	return address.Unmap(), err == nil
}

func retryAfterSeconds(window time.Duration) string {
	seconds := max(int64(1), int64((window+time.Second-1)/time.Second))
	return strconv.FormatInt(seconds, 10)
}

func minDuration(durations ...time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}
	minimum := durations[0]
	for _, candidate := range durations[1:] {
		if candidate < minimum {
			minimum = candidate
		}
	}
	return minimum
}
