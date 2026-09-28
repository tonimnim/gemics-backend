package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Both keys use one user hash tag, making the atomic reservation compatible
// with a Redis cluster. A rejected reservation consumes neither counter. The
// evidence and avatar budgets share this script under separate key prefixes.
var evidenceBudgetScript = redis.NewScript(`
local count = tonumber(redis.call('GET', KEYS[1]) or '0')
local bytes = tonumber(redis.call('GET', KEYS[2]) or '0')
if count + 1 > tonumber(ARGV[1]) then
  return math.max(1, redis.call('TTL', KEYS[1]))
end
if bytes + tonumber(ARGV[2]) > tonumber(ARGV[3]) then
  return math.max(1, redis.call('TTL', KEYS[2]))
end
if redis.call('INCR', KEYS[1]) == 1 then redis.call('EXPIRE', KEYS[1], 600) end
local total = redis.call('INCRBY', KEYS[2], ARGV[2])
if total == tonumber(ARGV[2]) then redis.call('EXPIRE', KEYS[2], 86400) end
return 0`)

// uploadBudget is one per-user presigned-upload allowance: countLimit intents
// per 10 minutes and byteLimit declared bytes per day. Every check fails
// closed with unavailableCode when Redis cannot answer.
type uploadBudget struct {
	keyPrefix       string
	countLimit      int
	byteLimit       int64
	limitedCode     string
	unavailableCode string
}

func (s *Server) evidenceUploadBudget() uploadBudget {
	return uploadBudget{
		keyPrefix: "evidence", countLimit: s.config.EvidenceUploadLimit, byteLimit: s.config.EvidenceDailyBytes,
		limitedCode: "evidence_rate_limited", unavailableCode: "evidence_limits_unavailable",
	}
}

func (s *Server) avatarUploadBudget() uploadBudget {
	return uploadBudget{
		keyPrefix: "avatar", countLimit: s.config.AvatarUploadLimit, byteLimit: s.config.AvatarDailyBytes,
		limitedCode: "avatar_rate_limited", unavailableCode: "avatar_limits_unavailable",
	}
}

func (s *Server) allowEvidenceUpload(w http.ResponseWriter, r *http.Request, byteSize int64) bool {
	return s.reserveUploadBudget(w, r, s.evidenceUploadBudget(), byteSize)
}

func (s *Server) allowEvidenceAction(w http.ResponseWriter, r *http.Request) bool {
	return s.limitUploadActions(w, r, s.evidenceUploadBudget(), "Too many evidence requests. Retry shortly.")
}

func (s *Server) allowAvatarUpload(w http.ResponseWriter, r *http.Request, byteSize int64) bool {
	return s.reserveUploadBudget(w, r, s.avatarUploadBudget(), byteSize)
}

// allowAvatarAction limits avatar completions, each of which costs a storage
// Stat, at the same per-minute rate as evidence requests.
func (s *Server) allowAvatarAction(w http.ResponseWriter, r *http.Request) bool {
	return s.limitUploadActions(w, r, s.avatarUploadBudget(), "Too many avatar requests. Retry shortly.")
}

func (s *Server) reserveUploadBudget(w http.ResponseWriter, r *http.Request, budget uploadBudget, byteSize int64) bool {
	if s.redis == nil {
		return uploadLimitsUnavailable(w, budget.unavailableCode)
	}
	userKey := s.uploadBudgetKey(r, budget)
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	retry, err := evidenceBudgetScript.Run(ctx, s.redis,
		[]string{userKey + "intents", userKey + "bytes"},
		budget.countLimit, byteSize, budget.byteLimit).Int64()
	if err != nil {
		return uploadLimitsUnavailable(w, budget.unavailableCode)
	}
	if retry > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(retry, 10))
		writeError(w, http.StatusTooManyRequests, budget.limitedCode, "Upload allowance reached. Retry after the indicated delay.")
		return false
	}
	return true
}

func (s *Server) limitUploadActions(w http.ResponseWriter, r *http.Request, budget uploadBudget, message string) bool {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	allowed, err := redisFixedWindow(ctx, s.redis, s.uploadBudgetKey(r, budget)+"actions",
		s.config.EvidenceActionLimit, time.Minute)
	if err != nil {
		return uploadLimitsUnavailable(w, budget.unavailableCode)
	}
	if !allowed {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, budget.limitedCode, message)
		return false
	}
	return true
}

// uploadBudgetKey hash-tags the caller's user ID so every key of one budget
// lands in the same Redis cluster slot.
func (s *Server) uploadBudgetKey(r *http.Request, budget uploadBudget) string {
	return s.securityKey(budget.keyPrefix + ":{" + identityFromContext(r.Context()).UserID + "}:")
}

func uploadLimitsUnavailable(w http.ResponseWriter, code string) bool {
	w.Header().Set("Retry-After", "5")
	writeError(w, http.StatusServiceUnavailable, code, "Upload protection is temporarily unavailable. Retry shortly.")
	return false
}
