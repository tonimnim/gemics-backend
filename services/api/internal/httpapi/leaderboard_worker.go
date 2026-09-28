package httpapi

import (
	"context"
	"strings"
	"time"
)

const (
	leaderboardProjectionInterval = time.Minute
	leaderboardSnapshotRetention  = 48 * time.Hour
)

type leaderboardProjectionTarget struct {
	GameID      string
	Scope       string
	CountryCode string
}

// runLeaderboardProjector turns rating writes into immutable snapshots used by
// the public ranking API. Every API replica may run it: each target is guarded
// by the same transaction-scoped advisory key as publish_leaderboard_snapshot.
func (s *Server) runLeaderboardProjector(ctx context.Context) {
	s.publishDueLeaderboardSnapshots(ctx)
	ticker := time.NewTicker(leaderboardProjectionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.publishDueLeaderboardSnapshots(ctx)
		}
	}
}

func (s *Server) publishDueLeaderboardSnapshots(ctx context.Context) {
	if s.db == nil || s.db.Writer == nil {
		return
	}
	rows, err := s.db.Writer.Query(ctx, `
		SELECT game.id,'global'::text,''::text
		FROM games game WHERE game.active=true
		UNION ALL
		SELECT DISTINCT rating.game_id,'country'::text,player.country_code::text
		FROM player_game_ratings rating
		JOIN games game ON game.id=rating.game_id AND game.active=true
		JOIN users player ON player.id=rating.user_id AND player.status='active'
		JOIN player_profiles profile ON profile.user_id=player.id AND profile.discoverable=true
		WHERE rating.matches_played>0
		ORDER BY 1,2,3`)
	if err != nil {
		s.logger.Warn("load leaderboard projection targets", "error", err)
		return
	}
	targets := make([]leaderboardProjectionTarget, 0)
	for rows.Next() {
		var target leaderboardProjectionTarget
		if err = rows.Scan(&target.GameID, &target.Scope, &target.CountryCode); err != nil {
			rows.Close()
			s.logger.Warn("scan leaderboard projection target", "error", err)
			return
		}
		target.CountryCode = strings.ToUpper(strings.TrimSpace(target.CountryCode))
		targets = append(targets, target)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		s.logger.Warn("read leaderboard projection targets", "error", err)
		return
	}
	rows.Close()

	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		if err = s.publishLeaderboardTarget(ctx, target); err != nil {
			s.logger.Warn("publish leaderboard snapshot", "game_id", target.GameID,
				"scope", target.Scope, "country", target.CountryCode, "error", err)
		}
	}
	if _, err = s.db.Writer.Exec(ctx, `WITH ranked AS (
		SELECT id,row_number() OVER (
			PARTITION BY game_id,scope,country_code
			ORDER BY (status='ready') DESC,snapshot_at DESC,id DESC
		) AS position
		FROM leaderboard_snapshots
	)
	DELETE FROM leaderboard_snapshots snapshot
	USING ranked
	WHERE snapshot.id=ranked.id AND ranked.position>1
	  AND snapshot.created_at < now()-($1::double precision * interval '1 second')`,
		leaderboardSnapshotRetention.Seconds()); err != nil {
		s.logger.Warn("prune leaderboard snapshots", "error", err)
	}
}

func (s *Server) publishLeaderboardTarget(ctx context.Context, target leaderboardProjectionTarget) error {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	key := target.GameID + ":" + target.Scope + ":" + target.CountryCode
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,70419827))`, key).Scan(&locked); err != nil {
		return err
	}
	if !locked {
		return nil
	}

	var sourceVersion int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(rating_version),0)
		FROM player_game_ratings WHERE game_id=$1`, target.GameID).Scan(&sourceVersion); err != nil {
		return err
	}
	var latestVersion *int64
	if err = tx.QueryRow(ctx, `SELECT max(source_version) FILTER (WHERE status='ready')
		FROM leaderboard_snapshots
		WHERE game_id=$1 AND scope=$2
		  AND country_code IS NOT DISTINCT FROM NULLIF($3,'')::char(2)`,
		target.GameID, target.Scope, target.CountryCode).Scan(&latestVersion); err != nil {
		return err
	}
	if latestVersion != nil && *latestVersion >= sourceVersion {
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `SELECT publish_leaderboard_snapshot($1,$2,NULLIF($3,''))`,
		target.GameID, target.Scope, target.CountryCode); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
