package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var errPublicCursorGone = errors.New("public cursor snapshot is unavailable")

// rowsQueryer is satisfied by both pgxpool.Pool and pgx.Tx, so a read can run
// on a pool or inside the transaction that just wrote the rows it returns.
type rowsQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type publicPlayerRecord struct {
	MatchesPlayed int `json:"matchesPlayed"`
	Wins          int `json:"wins"`
	Draws         int `json:"draws"`
	Losses        int `json:"losses"`
}

type publicGameRating struct {
	GameID        string     `json:"gameId"`
	Rating        int        `json:"rating"`
	MatchesPlayed int        `json:"matchesPlayed"`
	Wins          int        `json:"wins"`
	Draws         int        `json:"draws"`
	Losses        int        `json:"losses"`
	GlobalRank    *int       `json:"globalRank"`
	CountryRank   *int       `json:"countryRank"`
	RankMovement  *int       `json:"rankMovement"`
	LastMatchAt   *time.Time `json:"lastMatchAt"`
}

type publicGameAccount struct {
	GameID             string  `json:"gameId"`
	Platform           string  `json:"platform"`
	InGameName         string  `json:"inGameName"`
	VerificationStatus string  `json:"verificationStatus"`
	VerificationMethod *string `json:"verificationMethod"`
	PublisherVerified  bool    `json:"publisherVerified"`
}

type publicPlayerProfile struct {
	PlayerID     string              `json:"playerId"`
	Handle       string              `json:"handle"`
	DisplayName  string              `json:"displayName"`
	Bio          string              `json:"bio"`
	AvatarURL    *string             `json:"avatarUrl"`
	CountryCode  string              `json:"countryCode"`
	JoinedAt     time.Time           `json:"joinedAt"`
	Record       publicPlayerRecord  `json:"record"`
	Ratings      []publicGameRating  `json:"ratings"`
	GameAccounts []publicGameAccount `json:"gameAccounts"`
}

func (s *Server) withPublicRead(ctx context.Context, operation string, query func(rowsQueryer) error) error {
	reader := s.db.Reader
	writer := s.db.Writer
	if reader == nil {
		reader = writer
	}
	err := query(reader)
	if err == nil || reader == writer {
		return err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		s.logger.Warn("reader query failed; falling back to writer", "operation", operation, "error", err)
	}
	select {
	case s.writerFallback <- struct{}{}:
		defer func() { <-s.writerFallback }()
	case <-ctx.Done():
		return ctx.Err()
	default:
		return errWriterFallbackBusy
	}
	return query(writer)
}

func (s *Server) queryRankings(ctx context.Context, reader rowsQueryer, options rankingsOptions, now time.Time) (rankingsResponse, error) {
	result := rankingsResponse{Data: []compactPublicPlayer{}, Page: publicPage{}}
	var snapshotID string
	var snapshotAt time.Time
	if options.Cursor != nil {
		snapshotID = options.Cursor.SnapshotID
		err := reader.QueryRow(ctx, `SELECT id,snapshot_at
            FROM leaderboard_snapshots
            WHERE id=$1 AND game_id=$2 AND scope=$3
              AND country_code IS NOT DISTINCT FROM NULLIF($4,'')::char(2)
              AND status='ready'`, snapshotID, options.GameID, options.Scope, options.CountryCode).
			Scan(&snapshotID, &snapshotAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return result, errPublicCursorGone
		}
		if err != nil {
			return result, err
		}
	} else {
		err := reader.QueryRow(ctx, `SELECT id,snapshot_at
            FROM leaderboard_snapshots
            WHERE game_id=$1 AND scope=$2
              AND country_code IS NOT DISTINCT FROM NULLIF($3,'')::char(2)
              AND status='ready'
            ORDER BY snapshot_at DESC,id DESC LIMIT 1`, options.GameID, options.Scope, options.CountryCode).
			Scan(&snapshotID, &snapshotAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
	}

	lastRank := 0
	if options.Cursor != nil {
		lastRank = options.Cursor.Rank
	}
	rows, err := reader.Query(ctx, `SELECT row.rank,row.user_id,profile.handle,player.display_name,
		(profile.avatar_object_key IS NOT NULL),player.country_code,row.rating,row.matches_played,row.rank_movement
        FROM leaderboard_snapshot_rows row
        JOIN users player ON player.id=row.user_id AND player.status='active'
        JOIN player_profiles profile ON profile.user_id=player.id AND profile.discoverable=true
        WHERE row.snapshot_id=$1 AND row.rank>$2
        ORDER BY row.rank,row.user_id
        LIMIT $3`, snapshotID, lastRank, options.Limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item compactPublicPlayer
		var rank, rating, movement int
		var hasAvatar bool
		if err := rows.Scan(&rank, &item.PlayerID, &item.Handle, &item.DisplayName,
			&hasAvatar, &item.CountryCode, &rating, &item.MatchesPlayed, &movement); err != nil {
			return result, err
		}
		item.AvatarURL = publicPlayerAvatarReference(item.PlayerID, hasAvatar)
		item.Rank = intPointer(rank)
		item.Rating = intPointer(rating)
		item.RankMovement = intPointer(movement)
		result.Data = append(result.Data, item)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	hasMore := len(result.Data) > options.Limit
	if hasMore {
		result.Data = result.Data[:options.Limit]
		last := result.Data[len(result.Data)-1]
		expiresAt := now.Add(24 * time.Hour).Unix()
		if options.Cursor != nil {
			expiresAt = options.Cursor.ExpiresAt
		}
		next, encodeErr := encodePublicCursor(publicCursor{
			Kind: "rankings", ExpiresAt: expiresAt, SnapshotAt: snapshotAt.UnixNano(), SnapshotID: snapshotID,
			GameID: options.GameID, Scope: options.Scope, CountryCode: options.CountryCode, Rank: *last.Rank,
		}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			return result, encodeErr
		}
		result.Page.NextCursor = stringPointer(next)
	}
	result.Page.HasMore = hasMore
	result.SnapshotAt = timePointer(snapshotAt)
	return result, nil
}

func (s *Server) queryPlayers(ctx context.Context, reader rowsQueryer, options playersOptions, now time.Time) (playersResponse, error) {
	result := playersResponse{Data: []compactPublicPlayer{}, Page: publicPage{}, SnapshotAt: options.SnapshotAt}
	lastHandle := ""
	var lastID any
	if options.Cursor != nil {
		lastHandle = options.Cursor.Handle
		lastID = options.Cursor.ID
	}
	rows, err := reader.Query(ctx, `WITH latest_global AS (
        SELECT id FROM leaderboard_snapshots
        WHERE game_id=$1 AND scope='global' AND country_code IS NULL AND status='ready'
        ORDER BY snapshot_at DESC,id DESC LIMIT 1
      )
	  SELECT ranking.rank,profile.user_id,profile.handle,player.display_name,
	    (profile.avatar_object_key IS NOT NULL),player.country_code,
        coalesce(ranking.rating,rating.rating),
        coalesce(ranking.matches_played,rating.matches_played,0),ranking.rank_movement
      FROM player_profiles profile
      JOIN users player ON player.id=profile.user_id
      LEFT JOIN player_game_ratings rating ON rating.user_id=player.id AND rating.game_id=$1
      LEFT JOIN leaderboard_snapshot_rows ranking
        ON ranking.snapshot_id=(SELECT id FROM latest_global) AND ranking.user_id=player.id
      WHERE profile.discoverable=true AND player.status='active'
        AND profile.created_at<=$2 AND profile.updated_at<=$2
        AND player.created_at<=$2 AND player.updated_at<=$2
        AND ($3='' OR player.country_code=$3)
        AND EXISTS (
          SELECT 1 FROM game_accounts account
          WHERE account.user_id=player.id AND account.game_id=$1
            AND account.created_at<=$2 AND account.updated_at<=$2
        )
        AND ($4='' OR lower(profile.handle) LIKE '%'||$4||'%' ESCAPE E'\\'
          OR lower(player.display_name) LIKE '%'||$4||'%' ESCAPE E'\\'
          OR EXISTS (
            SELECT 1 FROM game_accounts searched
            WHERE searched.user_id=player.id AND searched.game_id=$1
              AND searched.created_at<=$2 AND searched.updated_at<=$2
              AND lower(searched.in_game_name) LIKE '%'||$4||'%' ESCAPE E'\\'
          ))
        AND ($5='' OR (lower(profile.handle),profile.user_id) > ($5,$6::uuid))
      ORDER BY lower(profile.handle),profile.user_id
      LIMIT $7`, options.GameID, options.SnapshotAt, options.CountryCode, escapeLike(options.Query),
		lastHandle, lastID, options.Limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item compactPublicPlayer
		var hasAvatar bool
		if err := rows.Scan(&item.Rank, &item.PlayerID, &item.Handle, &item.DisplayName, &hasAvatar, &item.CountryCode,
			&item.Rating, &item.MatchesPlayed, &item.RankMovement); err != nil {
			return result, err
		}
		item.AvatarURL = publicPlayerAvatarReference(item.PlayerID, hasAvatar)
		result.Data = append(result.Data, item)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	hasMore := len(result.Data) > options.Limit
	if hasMore {
		result.Data = result.Data[:options.Limit]
		last := result.Data[len(result.Data)-1]
		expiresAt := now.Add(30 * time.Minute).Unix()
		if options.Cursor != nil {
			expiresAt = options.Cursor.ExpiresAt
		}
		next, encodeErr := encodePublicCursor(publicCursor{
			Kind: "players", ExpiresAt: expiresAt, SnapshotAt: options.SnapshotAt.UnixNano(),
			GameID: options.GameID, CountryCode: options.CountryCode, Query: options.Query,
			Handle: lower(last.Handle), ID: last.PlayerID,
		}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			return result, encodeErr
		}
		result.Page.NextCursor = stringPointer(next)
	}
	result.Page.HasMore = hasMore
	return result, nil
}

func (s *Server) queryPublicPlayer(ctx context.Context, reader rowsQueryer, playerID string) (publicPlayerProfile, error) {
	var result publicPlayerProfile
	var hasAvatar bool
	err := reader.QueryRow(ctx, `SELECT player.id,profile.handle,player.display_name,profile.bio,
		(profile.avatar_object_key IS NOT NULL),player.country_code,player.created_at
      FROM users player
      JOIN player_profiles profile ON profile.user_id=player.id
      WHERE player.id=$1 AND player.status='active' AND profile.discoverable=true`, playerID).
		Scan(&result.PlayerID, &result.Handle, &result.DisplayName, &result.Bio, &hasAvatar, &result.CountryCode, &result.JoinedAt)
	if err != nil {
		return publicPlayerProfile{}, err
	}
	result.AvatarURL = publicPlayerAvatarReference(result.PlayerID, hasAvatar)

	result.Ratings = []publicGameRating{}
	rows, err := reader.Query(ctx, `SELECT rating.game_id,rating.rating,rating.matches_played,
        rating.wins,rating.draws,rating.losses,
        (SELECT snapshot_row.rank
          FROM leaderboard_snapshots snapshot
          JOIN leaderboard_snapshot_rows snapshot_row
            ON snapshot_row.snapshot_id=snapshot.id AND snapshot_row.user_id=rating.user_id
          WHERE snapshot.game_id=rating.game_id AND snapshot.scope='global'
            AND snapshot.country_code IS NULL AND snapshot.status='ready'
          ORDER BY snapshot.snapshot_at DESC,snapshot.id DESC LIMIT 1),
        (SELECT snapshot_row.rank
          FROM leaderboard_snapshots snapshot
          JOIN leaderboard_snapshot_rows snapshot_row
            ON snapshot_row.snapshot_id=snapshot.id AND snapshot_row.user_id=rating.user_id
          WHERE snapshot.game_id=rating.game_id AND snapshot.scope='country'
            AND snapshot.country_code=$2 AND snapshot.status='ready'
          ORDER BY snapshot.snapshot_at DESC,snapshot.id DESC LIMIT 1),
        (SELECT snapshot_row.rank_movement
          FROM leaderboard_snapshots snapshot
          JOIN leaderboard_snapshot_rows snapshot_row
            ON snapshot_row.snapshot_id=snapshot.id AND snapshot_row.user_id=rating.user_id
          WHERE snapshot.game_id=rating.game_id AND snapshot.scope='global'
            AND snapshot.country_code IS NULL AND snapshot.status='ready'
          ORDER BY snapshot.snapshot_at DESC,snapshot.id DESC LIMIT 1),
        rating.last_match_at
      FROM player_game_ratings rating
      WHERE rating.user_id=$1
      ORDER BY rating.game_id`, playerID, result.CountryCode)
	if err != nil {
		return publicPlayerProfile{}, err
	}
	for rows.Next() {
		var rating publicGameRating
		if err := rows.Scan(&rating.GameID, &rating.Rating, &rating.MatchesPlayed, &rating.Wins, &rating.Draws,
			&rating.Losses, &rating.GlobalRank, &rating.CountryRank, &rating.RankMovement, &rating.LastMatchAt); err != nil {
			rows.Close()
			return publicPlayerProfile{}, err
		}
		result.Ratings = append(result.Ratings, rating)
		result.Record.MatchesPlayed += rating.MatchesPlayed
		result.Record.Wins += rating.Wins
		result.Record.Draws += rating.Draws
		result.Record.Losses += rating.Losses
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return publicPlayerProfile{}, err
	}
	rows.Close()

	result.GameAccounts = []publicGameAccount{}
	accountRows, err := reader.Query(ctx, `SELECT game_id,platform,in_game_name,verification_status,
		verification_method,publisher_verified
      FROM game_accounts WHERE user_id=$1 ORDER BY game_id,created_at,id`, playerID)
	if err != nil {
		return publicPlayerProfile{}, err
	}
	defer accountRows.Close()
	for accountRows.Next() {
		var account publicGameAccount
		if err := accountRows.Scan(&account.GameID, &account.Platform, &account.InGameName,
			&account.VerificationStatus, &account.VerificationMethod, &account.PublisherVerified); err != nil {
			return publicPlayerProfile{}, err
		}
		result.GameAccounts = append(result.GameAccounts, account)
	}
	return result, accountRows.Err()
}

func intPointer(value int) *int              { return &value }
func stringPointer(value string) *string     { return &value }
func timePointer(value time.Time) *time.Time { return &value }
func lower(value string) string              { return strings.ToLower(value) }

// Public collections return a stable API reference instead of a short-lived
// object-store signature. The avatar endpoint rechecks discoverability on the
// writer and redirects to a freshly signed object URL.
func publicPlayerAvatarReference(playerID string, hasAvatar bool) *string {
	if !hasAvatar {
		return nil
	}
	return stringPointer("/v1/players/" + playerID + "/avatar")
}
