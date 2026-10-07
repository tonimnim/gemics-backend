package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	gamicscache "github.com/gamics-io/gamics/services/api/internal/cache"
	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	gamicsmail "github.com/gamics-io/gamics/services/api/internal/mail"
	"github.com/gamics-io/gamics/services/api/internal/mpesa"
	"github.com/gamics-io/gamics/services/api/internal/storage"
	"github.com/redis/go-redis/v9"
)

type Dependencies struct {
	Database      *database.Cluster
	Redis         *redis.Client
	CacheRedis    *redis.Client
	Mailer        gamicsmail.Sender
	MPesa         mpesa.Provider
	EvidenceStore storage.Provider
}

type Server struct {
	http                   *http.Server
	config                 config.Config
	logger                 *slog.Logger
	version                string
	db                     *database.Cluster
	redis                  *redis.Client
	cacheRedis             *redis.Client
	responses              *gamicscache.ResponseCache
	mailer                 gamicsmail.Sender
	mpesa                  mpesa.Provider
	evidenceStore          storage.Provider
	tokens                 *gamicsauth.TokenManager
	trustedProxies         []netip.Prefix
	readerUnavailableUntil atomic.Int64
	writerFallback         chan struct{}
	draining               atomic.Bool
}

func New(cfg config.Config, logger *slog.Logger, version string, dependencies ...Dependencies) *Server {
	var deps Dependencies
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}
	s := &Server{
		config: cfg, logger: logger, version: version,
		db: deps.Database, redis: deps.Redis, cacheRedis: deps.CacheRedis, mailer: deps.Mailer, mpesa: deps.MPesa,
		evidenceStore:  deps.EvidenceStore,
		tokens:         gamicsauth.NewTokenManager(cfg.AccessTokenSecret, cfg.AccessTokenTTL),
		writerFallback: make(chan struct{}, max(1, cfg.DatabaseFallbackMax)),
	}
	for _, raw := range cfg.TrustedProxyCIDRs {
		if prefix, err := netip.ParsePrefix(raw); err == nil {
			s.trustedProxies = append(s.trustedProxies, prefix)
		}
	}
	s.responses = gamicscache.NewResponseCache(deps.CacheRedis, cfg.CacheNamespace, cfg.DatabaseFallbackMax)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /v1/games", s.games)
	mux.HandleFunc("GET /v1/competitions", s.listCompetitions)
	mux.HandleFunc("GET /v1/competitions/{id}", s.getCompetition)
	mux.HandleFunc("GET /v1/competitions/{id}/bracket", s.getCompetitionBracket)
	mux.HandleFunc("GET /v1/competitions/{id}/standings", s.getCompetitionStandings)
	mux.HandleFunc("POST /v1/auth/register", s.register)
	mux.HandleFunc("POST /v1/auth/login", s.login)
	mux.HandleFunc("POST /v1/auth/password-reset/request", s.requestPasswordReset)
	mux.HandleFunc("POST /v1/auth/password-reset/confirm", s.confirmPasswordReset)
	mux.HandleFunc("POST /v1/auth/refresh", s.refreshSession)
	mux.Handle("POST /v1/auth/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	mux.Handle("GET /v1/me", s.requireAuth(http.HandlerFunc(s.getMe)))
	mux.Handle("PATCH /v1/me", s.requireAuth(http.HandlerFunc(s.patchMe)))
	mux.Handle("PUT /v1/me/profile", s.requireAuth(http.HandlerFunc(s.putProfile)))
	mux.Handle("POST /v1/me/password", s.requireAuth(http.HandlerFunc(s.changePassword)))
	mux.Handle("POST /v1/me/email", s.requireAuth(http.HandlerFunc(s.requestEmailVerification)))
	mux.Handle("POST /v1/me/email/verify", s.requireAuth(http.HandlerFunc(s.verifyEmail)))
	mux.Handle("PUT /v1/me/phone", s.requireAuth(http.HandlerFunc(s.putPhone)))
	mux.Handle("DELETE /v1/me/phone", s.requireAuth(http.HandlerFunc(s.deletePhone)))
	mux.Handle("GET /v1/me/game-accounts", s.requireAuth(http.HandlerFunc(s.listGameAccounts)))
	mux.Handle("POST /v1/me/game-accounts", s.requireAuth(http.HandlerFunc(s.createGameAccount)))
	mux.Handle("PATCH /v1/me/game-accounts/{id}", s.requireAuth(http.HandlerFunc(s.patchGameAccount)))
	mux.Handle("GET /v1/me/registrations", s.requireAuth(http.HandlerFunc(s.listMyRegistrations)))
	mux.Handle("POST /v1/competitions/{id}/registrations", s.requireAuth(http.HandlerFunc(s.createFreeRegistration)))
	mux.Handle("DELETE /v1/competitions/{id}/registrations/me", s.requireAuth(http.HandlerFunc(s.withdrawRegistration)))
	mux.Handle("GET /v1/me/matches", s.requireAuth(http.HandlerFunc(s.listMyMatches)))
	mux.Handle("GET /v1/matches/{matchId}", s.requireAuth(http.HandlerFunc(s.getMatch)))
	mux.Handle("POST /v1/matches/{matchId}/check-ins", s.requireAuth(http.HandlerFunc(s.checkInMatch)))
	mux.Handle("POST /v1/payments/mpesa/stk-push", s.requireAuth(http.HandlerFunc(s.initiateMPesa)))
	mux.Handle("GET /v1/payments/{id}", s.requireAuth(http.HandlerFunc(s.getPayment)))
	// Daraja drops callbacks whose URL contains "mpesa", "safaricom", "exec",
	// "cmd", "sql" or "query", so this path avoids every one of them.
	mux.HandleFunc("POST /v1/payments/callbacks/stk/{token}", s.mpesaCallback)
	mux.Handle("POST /v1/evidence/uploads", s.requireAuth(http.HandlerFunc(s.createEvidenceUpload)))
	mux.Handle("POST /v1/evidence/uploads/{id}/complete", s.requireAuth(http.HandlerFunc(s.completeEvidenceUpload)))
	mux.Handle("GET /v1/evidence/{id}", s.requireAuth(http.HandlerFunc(s.getEvidenceAccess)))
	s.registerPlayerDiscoveryRoutes(mux)
	s.registerMatchResultRoutes(mux)
	s.registerOrganizerRoutes(mux)
	s.registerOrganizerDrawRoutes(mux)
	s.registerIdentityNotificationRoutes(mux)
	s.registerPaymentLifecycleRoutes(mux)
	s.registerPaymentReviewRoutes(mux)
	s.registerResultReviewRoutes(mux)
	s.registerGameAccountVerificationRoutes(mux)
	s.registerCompetitionPolicyRoutes(mux)

	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.middleware(mux),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderBytes,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
	defer s.responses.Close()
	workerCtx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	if s.db != nil && s.mpesa != nil {
		go s.runPaymentReconciler(workerCtx)
	}
	if s.db != nil {
		go s.runMatchNoShowWorker(workerCtx)
		go s.runMatchResultVerificationWorker(workerCtx)
		go s.runLeaderboardProjector(workerCtx)
		go s.runNotificationPipeline(workerCtx)
	}
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("api listening", "address", s.http.Addr, "environment", s.config.Environment, "version", s.version)
		errCh <- s.http.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		s.draining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "gamics-api", "version": s.version})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "draining"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	components := map[string]string{}
	ready := true
	if s.db != nil {
		if err := s.db.PingWriter(ctx); err != nil {
			components["postgresWriter"] = "unavailable"
			ready = false
		} else {
			components["postgresWriter"] = "ready"
		}
		if lag, err := s.db.ReaderLag(ctx); err != nil {
			components["postgresReader"] = "degraded_writer_fallback"
		} else if lag > s.config.DatabaseMaxReplicaLag {
			components["postgresReader"] = "lagging_writer_fallback"
		} else {
			components["postgresReader"] = "ready"
		}
	}
	if s.redis != nil {
		if err := s.redis.Ping(ctx).Err(); err != nil {
			components["redis"] = "degraded_database_fallback"
		} else {
			components["redis"] = "ready"
		}
	}
	if s.cacheRedis != nil && s.cacheRedis != s.redis {
		if err := s.cacheRedis.Ping(ctx).Err(); err != nil {
			components["redisCache"] = "degraded_database_fallback"
		} else {
			components["redisCache"] = "ready"
		}
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "components": components})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "components": components})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if !validRequestID(requestID) {
			requestID = randomID()
		}
		r.Header.Set("X-Request-ID", requestID)
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if sensitivePath(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store, private")
		}

		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.config.AllowedOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		recorder := &responseMetricsWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic recovered", "request_id", requestID, "error", recovered, "stack", strings.TrimSpace(string(debug.Stack())))
				writeJSON(recorder, http.StatusInternalServerError, map[string]any{"error": "internal_error", "requestId": requestID})
			}
			s.logger.Info("request", "request_id", requestID, "method", r.Method, "path", safeLogPath(r.URL.Path),
				"status", recorder.status, "response_bytes", recorder.bytes, "duration_ms", time.Since(started).Milliseconds())
		}()

		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		defer cancel()
		next.ServeHTTP(recorder, r.WithContext(ctx))
	})
}

type responseMetricsWriter struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (w *responseMetricsWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseMetricsWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(body)
	w.bytes += n
	return n, err
}

func sensitivePath(path string) bool {
	return strings.HasPrefix(path, "/v1/auth/") || strings.HasPrefix(path, "/v1/me") ||
		strings.HasPrefix(path, "/v1/payments/") || strings.HasPrefix(path, "/v1/evidence/") ||
		strings.HasPrefix(path, "/v1/matches/") || strings.HasPrefix(path, "/v1/organizations") ||
		strings.HasPrefix(path, "/v1/admin/") || strings.Contains(path, "/registrations/me/")
}

func safeLogPath(path string) string {
	if strings.HasPrefix(path, "/v1/payments/callbacks/stk/") {
		return "/v1/payments/callbacks/stk/[redacted]"
	}
	return path
}

func validRequestID(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character == '-' || character == '_' || character == '.' || character >= '0' && character <= '9' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z') {
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write response", "error", err)
	}
}

func randomID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
