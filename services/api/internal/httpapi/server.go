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
	"runtime/debug"
	"slices"
	"strings"
	"time"

	gamicsauth "github.com/gamics-io/gamics/services/api/internal/auth"
	"github.com/gamics-io/gamics/services/api/internal/config"
	"github.com/gamics-io/gamics/services/api/internal/database"
	gamicsmail "github.com/gamics-io/gamics/services/api/internal/mail"
	"github.com/redis/go-redis/v9"
)

type Dependencies struct {
	Database *database.Cluster
	Redis    *redis.Client
	Mailer   gamicsmail.Sender
}

type Server struct {
	http    *http.Server
	config  config.Config
	logger  *slog.Logger
	version string
	db      *database.Cluster
	redis   *redis.Client
	mailer  gamicsmail.Sender
	tokens  *gamicsauth.TokenManager
}

func New(cfg config.Config, logger *slog.Logger, version string, dependencies ...Dependencies) *Server {
	var deps Dependencies
	if len(dependencies) > 0 {
		deps = dependencies[0]
	}
	s := &Server{
		config: cfg, logger: logger, version: version,
		db: deps.Database, redis: deps.Redis, mailer: deps.Mailer,
		tokens: gamicsauth.NewTokenManager(cfg.AccessTokenSecret, cfg.AccessTokenTTL),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("GET /v1/games", s.games)
	mux.HandleFunc("POST /v1/auth/otp/request", s.requestOTP)
	mux.HandleFunc("POST /v1/auth/otp/verify", s.verifyOTP)
	mux.HandleFunc("POST /v1/auth/refresh", s.refreshSession)
	mux.Handle("POST /v1/auth/logout", s.requireAuth(http.HandlerFunc(s.logout)))
	mux.Handle("GET /v1/me", s.requireAuth(http.HandlerFunc(s.getMe)))
	mux.Handle("PATCH /v1/me", s.requireAuth(http.HandlerFunc(s.patchMe)))
	mux.Handle("PUT /v1/me/profile", s.requireAuth(http.HandlerFunc(s.putProfile)))
	mux.Handle("GET /v1/me/game-accounts", s.requireAuth(http.HandlerFunc(s.listGameAccounts)))
	mux.Handle("POST /v1/me/game-accounts", s.requireAuth(http.HandlerFunc(s.createGameAccount)))
	mux.Handle("PATCH /v1/me/game-accounts/{id}", s.requireAuth(http.HandlerFunc(s.patchGameAccount)))

	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.middleware(mux),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       60 * time.Second,
	}
	return s
}

func (s *Server) Run(ctx context.Context) error {
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.config.ShutdownTimeout)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "gamics-api", "version": s.version})
}

func (s *Server) ready(w http.ResponseWriter, _ *http.Request) {
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
		if err := s.db.PingReader(ctx); err != nil {
			components["postgresReader"] = "degraded_writer_fallback"
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
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready", "components": components})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready", "components": components})
}

func (s *Server) games(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": []map[string]any{{
			"id": "efootball-mobile", "name": "eFootball Mobile", "publisher": "Konami Digital Entertainment",
			"platforms": []string{"android", "ios"}, "resultMode": "participant_confirmation", "officialIntegration": false,
		}}})
		return
	}
	data, err := s.queryGames(r.Context(), s.db.Reader)
	if err != nil {
		s.logger.Warn("reader query failed; falling back to writer", "operation", "list_games", "error", err)
		data, err = s.queryGames(r.Context(), s.db.Writer)
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Games are temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (s *Server) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = randomID()
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

		origin := r.Header.Get("Origin")
		if origin != "" && slices.Contains(s.config.AllowedOrigins, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Idempotency-Key, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		defer func() {
			if recovered := recover(); recovered != nil {
				s.logger.Error("panic recovered", "request_id", requestID, "error", recovered, "stack", strings.TrimSpace(string(debug.Stack())))
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal_error", "requestId": requestID})
			}
			s.logger.Info("request", "request_id", requestID, "method", r.Method, "path", r.URL.Path, "duration_ms", time.Since(started).Milliseconds())
		}()

		ctx, cancel := context.WithTimeout(r.Context(), s.config.RequestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
