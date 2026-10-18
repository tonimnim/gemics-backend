package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

var (
	publicGameIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)
	publicUUIDPattern   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
)

type publicPage struct {
	NextCursor *string `json:"nextCursor"`
	HasMore    bool    `json:"hasMore"`
}

type compactPublicPlayer struct {
	Rank          *int    `json:"rank"`
	PlayerID      string  `json:"playerId"`
	Handle        string  `json:"handle"`
	DisplayName   string  `json:"displayName"`
	AvatarURL     *string `json:"avatarUrl"`
	CountryCode   string  `json:"countryCode"`
	Rating        *int    `json:"rating"`
	MatchesPlayed int     `json:"matchesPlayed"`
	RankMovement  *int    `json:"rankMovement"`
}

type rankingsResponse struct {
	Data       []compactPublicPlayer `json:"data"`
	Page       publicPage            `json:"page"`
	SnapshotAt *time.Time            `json:"snapshotAt"`
}

type playersResponse struct {
	Data       []compactPublicPlayer `json:"data"`
	Page       publicPage            `json:"page"`
	SnapshotAt time.Time             `json:"snapshotAt"`
}

type rankingsOptions struct {
	GameID      string
	Scope       string
	CountryCode string
	Limit       int
	Cursor      *publicCursor
	// Around is set by around=me: return Span places either side of the
	// signed-in caller (AroundUserID) instead of a page from the top.
	Around       bool
	Span         int
	AroundUserID string
}

type playersOptions struct {
	GameID      string
	CountryCode string
	Query       string
	Limit       int
	SnapshotAt  time.Time
	Cursor      *publicCursor
}

type historyOptions struct {
	Limit      int
	SnapshotAt time.Time
	Cursor     *publicCursor
}

func (s *Server) rankings(w http.ResponseWriter, r *http.Request) {
	markPrivacyAwarePublicResponse(w)
	now := time.Now().UTC()
	options, err := s.parseRankingsOptions(r, now)
	if err != nil {
		s.writePublicQueryError(w, err)
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	if options.Around {
		viewerID, ok := s.optionalViewer(w, r)
		if !ok {
			return
		}
		if viewerID == "" {
			writeError(w, http.StatusUnauthorized, "authentication_required", "Sign in to see your place on the ladder.")
			return
		}
		options.AroundUserID = viewerID
	}

	var result rankingsResponse
	err = s.withPublicRead(r.Context(), "list_rankings", func(reader rowsQueryer) error {
		loaded, queryErr := s.queryRankings(r.Context(), reader, options, now)
		result = loaded
		return queryErr
	})
	if errors.Is(err, errPublicCursorGone) {
		writeError(w, http.StatusGone, "cursor_expired", "The leaderboard snapshot is no longer available. Restart from the first page.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Rankings are temporarily unavailable.")
		return
	}
	s.writePrivacyAwarePublicJSON(w, http.StatusOK, result)
}

func (s *Server) players(w http.ResponseWriter, r *http.Request) {
	markPrivacyAwarePublicResponse(w)
	now := time.Now().UTC()
	options, err := s.parsePlayersOptions(r, now)
	if err != nil {
		s.writePublicQueryError(w, err)
		return
	}
	if !s.requireDatabase(w) {
		return
	}

	var result playersResponse
	err = s.withPublicRead(r.Context(), "search_players", func(reader rowsQueryer) error {
		loaded, queryErr := s.queryPlayers(r.Context(), reader, options, now)
		result = loaded
		return queryErr
	})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Player search is temporarily unavailable.")
		return
	}
	s.writePrivacyAwarePublicJSON(w, http.StatusOK, result)
}

func (s *Server) publicPlayer(w http.ResponseWriter, r *http.Request) {
	markPrivacyAwarePublicResponse(w)
	playerID := strings.TrimSpace(r.PathValue("id"))
	if !publicUUIDPattern.MatchString(playerID) {
		writeError(w, http.StatusBadRequest, "invalid_player_id", "Player ID must be a UUID.")
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	// Players always see their own profile and history, public or not.
	viewerID, ok := s.optionalViewer(w, r)
	if !ok {
		return
	}

	var result publicPlayerProfile
	err := s.withPublicRead(r.Context(), "get_public_player", func(reader rowsQueryer) error {
		loaded, queryErr := s.queryPublicPlayer(r.Context(), reader, playerID, viewerID)
		result = loaded
		return queryErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "The player profile is temporarily unavailable.")
		return
	}
	s.writePrivacyAwarePublicJSON(w, http.StatusOK, result)
}

func (s *Server) publicPlayerMatches(w http.ResponseWriter, r *http.Request) {
	markPrivacyAwarePublicResponse(w)
	playerID := strings.TrimSpace(r.PathValue("id"))
	if !publicUUIDPattern.MatchString(playerID) {
		writeError(w, http.StatusBadRequest, "invalid_player_id", "Player ID must be a UUID.")
		return
	}
	now := time.Now().UTC()
	options, err := s.parseHistoryOptions(r, "player-matches", playerID, now)
	if err != nil {
		s.writePublicQueryError(w, err)
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	// Players always see their own profile and history, public or not.
	viewerID, ok := s.optionalViewer(w, r)
	if !ok {
		return
	}

	var result publicMatchesResponse
	err = s.withPublicRead(r.Context(), "list_public_player_matches", func(reader rowsQueryer) error {
		loaded, queryErr := s.queryPublicPlayerMatches(r.Context(), reader, playerID, viewerID, options, now)
		result = loaded
		return queryErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Match history is temporarily unavailable.")
		return
	}
	s.writePrivacyAwarePublicJSON(w, http.StatusOK, result)
}

// publicPlayerRatingHistory serves the points behind a player's rating line,
// under the same visibility as their profile.
func (s *Server) publicPlayerRatingHistory(w http.ResponseWriter, r *http.Request) {
	markPrivacyAwarePublicResponse(w)
	playerID := strings.TrimSpace(r.PathValue("id"))
	if !publicUUIDPattern.MatchString(playerID) {
		writeError(w, http.StatusBadRequest, "invalid_player_id", "Player ID must be a UUID.")
		return
	}
	query := r.URL.Query()
	gameID := strings.ToLower(strings.TrimSpace(query.Get("gameId")))
	if gameID == "" {
		gameID = registrationGameID
	}
	if !publicGameIDPattern.MatchString(gameID) {
		s.writePublicQueryError(w, errors.New("invalid game id"))
		return
	}
	days := 30
	if raw := strings.TrimSpace(query.Get("days")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 7 || value > 365 {
			s.writePublicQueryError(w, errors.New("days must be between 7 and 365"))
			return
		}
		days = value
	}
	if !s.requireDatabase(w) {
		return
	}
	viewerID, ok := s.optionalViewer(w, r)
	if !ok {
		return
	}
	var points []ratingHistoryPoint
	err := s.withPublicRead(r.Context(), "player_rating_history", func(reader rowsQueryer) error {
		if visibleErr := requireVisiblePlayer(r.Context(), reader, playerID, viewerID); visibleErr != nil {
			return visibleErr
		}
		loaded, queryErr := queryRatingHistory(r.Context(), reader, playerID, gameID, days)
		points = loaded
		return queryErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Rating history is temporarily unavailable.")
		return
	}
	s.writePrivacyAwarePublicJSON(w, http.StatusOK, map[string]any{"gameId": gameID, "days": days, "data": points})
}

func (s *Server) publicPlayerCompetitions(w http.ResponseWriter, r *http.Request) {
	markPrivacyAwarePublicResponse(w)
	playerID := strings.TrimSpace(r.PathValue("id"))
	if !publicUUIDPattern.MatchString(playerID) {
		writeError(w, http.StatusBadRequest, "invalid_player_id", "Player ID must be a UUID.")
		return
	}
	now := time.Now().UTC()
	options, err := s.parseHistoryOptions(r, "player-competitions", playerID, now)
	if err != nil {
		s.writePublicQueryError(w, err)
		return
	}
	if !s.requireDatabase(w) {
		return
	}
	// Players always see their own profile and history, public or not.
	viewerID, ok := s.optionalViewer(w, r)
	if !ok {
		return
	}

	var result publicCompetitionsResponse
	err = s.withPublicRead(r.Context(), "list_public_player_competitions", func(reader rowsQueryer) error {
		loaded, queryErr := s.queryPublicPlayerCompetitions(r.Context(), reader, playerID, viewerID, options, now)
		result = loaded
		return queryErr
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "Player not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Competition history is temporarily unavailable.")
		return
	}
	s.writePrivacyAwarePublicJSON(w, http.StatusOK, result)
}

func (s *Server) parseRankingsOptions(r *http.Request, now time.Time) (rankingsOptions, error) {
	query := r.URL.Query()
	options := rankingsOptions{
		GameID: strings.ToLower(strings.TrimSpace(query.Get("gameId"))),
		Scope:  strings.ToLower(strings.TrimSpace(query.Get("scope"))),
	}
	if options.GameID == "" {
		options.GameID = "efootball-mobile"
	}
	if options.Scope == "" {
		options.Scope = "country"
	}
	if !publicGameIDPattern.MatchString(options.GameID) {
		return rankingsOptions{}, errors.New("invalid game id")
	}
	if options.Scope != "country" && options.Scope != "global" {
		return rankingsOptions{}, errors.New("scope must be country or global")
	}
	options.CountryCode = strings.ToUpper(strings.TrimSpace(query.Get("country")))
	if options.Scope == "country" {
		if options.CountryCode == "" {
			options.CountryCode = "KE"
		}
		if !countryPattern.MatchString(options.CountryCode) {
			return rankingsOptions{}, errors.New("country must be a two-letter ISO code")
		}
	} else if options.CountryCode != "" {
		return rankingsOptions{}, errors.New("country is only valid for country rankings")
	}
	limit, err := parsePublicLimit(query.Get("limit"))
	if err != nil {
		return rankingsOptions{}, err
	}
	options.Limit = limit
	if around := strings.TrimSpace(query.Get("around")); around != "" {
		if around != "me" {
			return rankingsOptions{}, errors.New("around must be me")
		}
		if strings.TrimSpace(query.Get("cursor")) != "" {
			return rankingsOptions{}, errors.New("around cannot be combined with a cursor")
		}
		options.Around, options.Span = true, 2
		if raw := strings.TrimSpace(query.Get("span")); raw != "" {
			span, convErr := strconv.Atoi(raw)
			if convErr != nil || span < 1 || span > 10 {
				return rankingsOptions{}, errors.New("span must be between 1 and 10")
			}
			options.Span = span
		}
	} else if strings.TrimSpace(query.Get("span")) != "" {
		return rankingsOptions{}, errors.New("span is only valid with around=me")
	}
	if rawCursor := strings.TrimSpace(query.Get("cursor")); rawCursor != "" {
		cursor, decodeErr := decodePublicCursor(rawCursor, "rankings", s.config.AccessTokenSecret, now)
		if decodeErr != nil {
			return rankingsOptions{}, decodeErr
		}
		if cursor.GameID != options.GameID || cursor.Scope != options.Scope || cursor.CountryCode != options.CountryCode || cursor.SnapshotID == "" || cursor.Rank < 1 {
			return rankingsOptions{}, errInvalidPublicCursor
		}
		options.Cursor = &cursor
	}
	return options, nil
}

func (s *Server) parsePlayersOptions(r *http.Request, now time.Time) (playersOptions, error) {
	query := r.URL.Query()
	options := playersOptions{
		GameID:      strings.ToLower(strings.TrimSpace(query.Get("gameId"))),
		CountryCode: strings.ToUpper(strings.TrimSpace(query.Get("country"))),
		Query:       strings.ToLower(strings.TrimSpace(query.Get("q"))),
		SnapshotAt:  now,
	}
	if options.GameID == "" {
		options.GameID = "efootball-mobile"
	}
	if !publicGameIDPattern.MatchString(options.GameID) {
		return playersOptions{}, errors.New("invalid game id")
	}
	if options.CountryCode != "" && !countryPattern.MatchString(options.CountryCode) {
		return playersOptions{}, errors.New("country must be a two-letter ISO code")
	}
	queryLength := utf8.RuneCountInString(options.Query)
	if queryLength == 1 || queryLength > 80 {
		return playersOptions{}, errors.New("q must be empty or between 2 and 80 characters")
	}
	limit, err := parsePublicLimit(query.Get("limit"))
	if err != nil {
		return playersOptions{}, err
	}
	options.Limit = limit
	if rawCursor := strings.TrimSpace(query.Get("cursor")); rawCursor != "" {
		cursor, decodeErr := decodePublicCursor(rawCursor, "players", s.config.AccessTokenSecret, now)
		if decodeErr != nil {
			return playersOptions{}, decodeErr
		}
		if cursor.GameID != options.GameID || cursor.CountryCode != options.CountryCode || cursor.Query != options.Query || cursor.SnapshotAt <= 0 || cursor.Handle == "" || !publicUUIDPattern.MatchString(cursor.ID) {
			return playersOptions{}, errInvalidPublicCursor
		}
		options.Cursor = &cursor
		options.SnapshotAt = cursorTime(cursor.SnapshotAt)
	}
	return options, nil
}

func (s *Server) parseHistoryOptions(r *http.Request, kind, playerID string, now time.Time) (historyOptions, error) {
	limit, err := parsePublicLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return historyOptions{}, err
	}
	options := historyOptions{Limit: limit, SnapshotAt: now}
	if rawCursor := strings.TrimSpace(r.URL.Query().Get("cursor")); rawCursor != "" {
		cursor, decodeErr := decodePublicCursor(rawCursor, kind, s.config.AccessTokenSecret, now)
		if decodeErr != nil {
			return historyOptions{}, decodeErr
		}
		if cursor.Query != playerID || cursor.SnapshotAt <= 0 || cursor.SortTime <= 0 || !publicUUIDPattern.MatchString(cursor.ID) {
			return historyOptions{}, errInvalidPublicCursor
		}
		options.Cursor = &cursor
		options.SnapshotAt = cursorTime(cursor.SnapshotAt)
	}
	return options, nil
}

func (s *Server) writePublicQueryError(w http.ResponseWriter, err error) {
	if errors.Is(err, errExpiredPublicCursor) {
		writeError(w, http.StatusGone, "cursor_expired", "The cursor has expired. Restart from the first page.")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_query", err.Error())
}

func (s *Server) writePrivacyAwarePublicJSON(w http.ResponseWriter, status int, value any) {
	markPrivacyAwarePublicResponse(w)
	writeJSON(w, status, value)
}

func markPrivacyAwarePublicResponse(w http.ResponseWriter) {
	// Discoverability and suspension changes must take effect immediately. Shared
	// response caching is deliberately disabled until event-driven purge exists.
	w.Header().Set("Cache-Control", "private, no-store")
}
