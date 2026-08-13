package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	gamicscache "github.com/gamics-io/gamics/services/api/internal/cache"
	"github.com/jackc/pgx/v5/pgxpool"
)

type gameCatalogItem struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Publisher           string   `json:"publisher"`
	Platforms           []string `json:"platforms"`
	ResultMode          string   `json:"resultMode"`
	OfficialIntegration bool     `json:"officialIntegration"`
}

type gameCatalogResponse struct {
	Data []gameCatalogItem `json:"data"`
}

var errWriterFallbackBusy = errors.New("writer fallback is saturated")

func (s *Server) games(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		body, _ := json.Marshal(gameCatalogResponse{Data: []gameCatalogItem{{
			ID: "efootball-mobile", Name: "eFootball Mobile", Publisher: "Konami Digital Entertainment",
			Platforms: []string{"android", "ios"}, ResultMode: "participant_confirmation",
		}}})
		s.writePublicCatalog(w, r, gamicscache.Response{Body: body, ETag: gamicscache.WeakETag(body), State: gamicscache.StateBypass})
		return
	}

	response, err := s.responses.GetOrLoad(r.Context(), "games-active", gamicscache.Policy{
		FreshFor: s.config.GameCatalogCacheTTL, KeepFor: 2 * s.config.GameCatalogCacheTTL,
		LoadTimeout: 2 * time.Second, LockFor: 3 * time.Second, WaitFor: 600 * time.Millisecond,
		MaxBodyBytes: 1 << 20,
	}, func(ctx context.Context) ([]byte, error) {
		data, err := s.loadGames(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(gameCatalogResponse{Data: data})
	})
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Games are temporarily unavailable.")
		return
	}
	s.writePublicCatalog(w, r, response)
}

func (s *Server) loadGames(ctx context.Context) ([]gameCatalogItem, error) {
	if time.Now().UnixNano() >= s.readerUnavailableUntil.Load() {
		lag, lagErr := s.db.ReaderLag(ctx)
		if lagErr == nil && lag <= s.config.DatabaseMaxReplicaLag {
			if data, err := s.queryGames(ctx, s.db.Reader); err == nil {
				return data, nil
			} else {
				s.logger.Warn("reader query failed; using writer circuit", "operation", "list_games", "error", err)
			}
		} else if lagErr != nil {
			s.logger.Warn("reader health failed; using writer circuit", "operation", "list_games", "error", lagErr)
		}
		s.readerUnavailableUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
	}
	select {
	case s.writerFallback <- struct{}{}:
		defer func() { <-s.writerFallback }()
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		return nil, errWriterFallbackBusy
	}
	return s.queryGames(ctx, s.db.Writer)
}

func (s *Server) queryGames(ctx context.Context, pool *pgxpool.Pool) ([]gameCatalogItem, error) {
	rows, err := pool.Query(ctx, `
        SELECT id, name, publisher, supported_platforms, result_mode
        FROM games WHERE active = true ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	data := make([]gameCatalogItem, 0)
	for rows.Next() {
		var item gameCatalogItem
		if err := rows.Scan(&item.ID, &item.Name, &item.Publisher, &item.Platforms, &item.ResultMode); err != nil {
			return nil, err
		}
		data = append(data, item)
	}
	return data, rows.Err()
}

func (s *Server) writePublicCatalog(w http.ResponseWriter, r *http.Request, response gamicscache.Response) {
	w.Header().Set("Cache-Control", "public, max-age=300, s-maxage=3600, stale-while-revalidate=86400, stale-if-error=86400")
	w.Header().Set("ETag", response.ETag)
	w.Header().Set("X-Cache", string(response.State))
	if etagMatches(r.Header.Get("If-None-Match"), response.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response.Body)
}

func etagMatches(header, current string) bool {
	current = strings.TrimPrefix(strings.TrimSpace(current), "W/")
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == current {
			return candidate != "" && current != ""
		}
	}
	return false
}
