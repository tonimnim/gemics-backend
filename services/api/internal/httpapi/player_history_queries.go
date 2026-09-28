package httpapi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

type publicOpponent struct {
	PlayerID    string  `json:"playerId"`
	Handle      string  `json:"handle"`
	DisplayName string  `json:"displayName"`
	AvatarURL   *string `json:"avatarUrl"`
}

type publicMatchScore struct {
	Player   int                  `json:"player"`
	Opponent int                  `json:"opponent"`
	Home     int                  `json:"home"`
	Away     int                  `json:"away"`
	Tiebreak *publicTiebreakScore `json:"tiebreak,omitempty"`
}

type publicTiebreakScore struct {
	Type string `json:"type"`
	Home int    `json:"home"`
	Away int    `json:"away"`
}

type publicCompetitionSummary struct {
	CompetitionID string `json:"competitionId"`
	Name          string `json:"name"`
}

type publicMatchHistoryItem struct {
	MatchID           string                   `json:"matchId"`
	PlayedAt          time.Time                `json:"playedAt"`
	Opponent          *publicOpponent          `json:"opponent"`
	Score             publicMatchScore         `json:"score"`
	Outcome           string                   `json:"outcome"`
	Competition       publicCompetitionSummary `json:"competition"`
	Stage             string                   `json:"stage"`
	RoundNumber       int                      `json:"roundNumber"`
	VerificationState string                   `json:"verificationState"`
}

type publicMatchesResponse struct {
	Data       []publicMatchHistoryItem `json:"data"`
	Page       publicPage               `json:"page"`
	SnapshotAt time.Time                `json:"snapshotAt"`
}

type publicCompetitionHistoryItem struct {
	CompetitionID string             `json:"competitionId"`
	Name          string             `json:"name"`
	Format        string             `json:"format"`
	Status        string             `json:"status"`
	StartedAt     time.Time          `json:"startedAt"`
	Placement     *int               `json:"placement"`
	Entrants      int                `json:"entrants"`
	MaxEntries    int                `json:"maxEntries"`
	Record        publicPlayerRecord `json:"record"`
}

type publicCompetitionsResponse struct {
	Data       []publicCompetitionHistoryItem `json:"data"`
	Page       publicPage                     `json:"page"`
	SnapshotAt time.Time                      `json:"snapshotAt"`
}

func (s *Server) queryPublicPlayerMatches(ctx context.Context, reader rowsQueryer, playerID string, options historyOptions, now time.Time) (publicMatchesResponse, error) {
	result := publicMatchesResponse{Data: []publicMatchHistoryItem{}, Page: publicPage{}, SnapshotAt: options.SnapshotAt}
	if err := requireDiscoverablePlayer(ctx, reader, playerID); err != nil {
		return result, err
	}
	var lastTime any
	var lastID any
	if options.Cursor != nil {
		lastTime = cursorTime(options.Cursor.SortTime)
		lastID = options.Cursor.ID
	}
	rows, err := reader.Query(ctx, `WITH player_matches AS (
        SELECT match.id,match.competition_id,match.stage_id,match.round_number,
          coalesce(match.completed_at,submission.decided_at,submission.submitted_at) AS played_at,
          submission.home_score,submission.away_score,submission.tiebreak_type,
          submission.home_tiebreak_score,submission.away_tiebreak_score,
          match.home_entry_id=membership.entry_id AS player_is_home,
          CASE WHEN match.home_entry_id=membership.entry_id
            THEN match.away_entry_id ELSE match.home_entry_id END AS opponent_entry_id
        FROM entry_members membership
        JOIN matches match
          ON (match.home_entry_id=membership.entry_id OR match.away_entry_id=membership.entry_id)
        JOIN result_submissions submission
          ON submission.match_id=match.id AND submission.status='confirmed'
        WHERE membership.user_id=$1
          AND membership.roster_role IN ('starter','substitute')
          AND match.state IN ('completed','forfeit')
      )
	      SELECT history.id,history.played_at,history.home_score,history.away_score,
	        history.tiebreak_type,history.home_tiebreak_score,history.away_tiebreak_score,history.player_is_home,
        competition.id,competition.name,stage.name,history.round_number,
	        opponent.id,opponent.handle,opponent.display_name,opponent.has_avatar
      FROM player_matches history
      JOIN competitions competition ON competition.id=history.competition_id
      JOIN competition_stages stage ON stage.id=history.stage_id
      LEFT JOIN LATERAL (
	        SELECT opponent_player.id,opponent_player.display_name,opponent_public.handle,
	          (opponent_public.avatar_object_key IS NOT NULL) AS has_avatar
        FROM entry_members opponent_membership
        JOIN users opponent_player
          ON opponent_player.id=opponent_membership.user_id AND opponent_player.status='active'
        JOIN player_profiles opponent_public
          ON opponent_public.user_id=opponent_player.id AND opponent_public.discoverable=true
        WHERE opponent_membership.entry_id=history.opponent_entry_id
          AND opponent_membership.roster_role IN ('starter','substitute')
        ORDER BY CASE opponent_membership.roster_role WHEN 'starter' THEN 0 ELSE 1 END,
          opponent_player.id
        LIMIT 1
      ) opponent ON true
      WHERE history.played_at<=$2
        AND ($3::timestamptz IS NULL OR (history.played_at,history.id)<($3,$4::uuid))
      ORDER BY history.played_at DESC,history.id DESC
      LIMIT $5`, playerID, options.SnapshotAt, lastTime, lastID, options.Limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item publicMatchHistoryItem
		var homeScore, awayScore int
		var tiebreakType *string
		var homeTiebreakScore, awayTiebreakScore *int
		var playerIsHome bool
		var opponentID, opponentHandle, opponentDisplayName *string
		var opponentHasAvatar *bool
		if err := rows.Scan(&item.MatchID, &item.PlayedAt, &homeScore, &awayScore,
			&tiebreakType, &homeTiebreakScore, &awayTiebreakScore, &playerIsHome,
			&item.Competition.CompetitionID, &item.Competition.Name, &item.Stage, &item.RoundNumber,
			&opponentID, &opponentHandle, &opponentDisplayName, &opponentHasAvatar); err != nil {
			return result, err
		}
		item.Score.Home = homeScore
		item.Score.Away = awayScore
		var tiebreak *tiebreakScoreInput
		if tiebreakType != nil && homeTiebreakScore != nil && awayTiebreakScore != nil {
			tiebreak = &tiebreakScoreInput{Type: *tiebreakType, HomeScore: *homeTiebreakScore, AwayScore: *awayTiebreakScore}
			item.Score.Tiebreak = &publicTiebreakScore{Type: *tiebreakType, Home: *homeTiebreakScore, Away: *awayTiebreakScore}
		}
		if playerIsHome {
			item.Score.Player, item.Score.Opponent = homeScore, awayScore
		} else {
			item.Score.Player, item.Score.Opponent = awayScore, homeScore
		}
		homeWon, awayWon := resultWinner(homeScore, awayScore, tiebreak)
		playerWon := playerIsHome && homeWon || !playerIsHome && awayWon
		playerLost := playerIsHome && awayWon || !playerIsHome && homeWon
		switch {
		case playerWon:
			item.Outcome = "win"
		case playerLost:
			item.Outcome = "loss"
		default:
			item.Outcome = "draw"
		}
		if opponentID != nil && opponentHandle != nil && opponentDisplayName != nil {
			item.Opponent = &publicOpponent{PlayerID: *opponentID, Handle: *opponentHandle, DisplayName: *opponentDisplayName,
				AvatarURL: publicPlayerAvatarReference(*opponentID, opponentHasAvatar != nil && *opponentHasAvatar)}
		}
		item.VerificationState = "confirmed"
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
			Kind: "player-matches", ExpiresAt: expiresAt, SnapshotAt: options.SnapshotAt.UnixNano(),
			Query: playerID, SortTime: last.PlayedAt.UnixNano(), ID: last.MatchID,
		}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			return result, encodeErr
		}
		result.Page.NextCursor = stringPointer(next)
	}
	result.Page.HasMore = hasMore
	return result, nil
}

func (s *Server) queryPublicPlayerCompetitions(ctx context.Context, reader rowsQueryer, playerID string, options historyOptions, now time.Time) (publicCompetitionsResponse, error) {
	result := publicCompetitionsResponse{Data: []publicCompetitionHistoryItem{}, Page: publicPage{}, SnapshotAt: options.SnapshotAt}
	if err := requireDiscoverablePlayer(ctx, reader, playerID); err != nil {
		return result, err
	}
	var lastTime any
	var lastID any
	if options.Cursor != nil {
		lastTime = cursorTime(options.Cursor.SortTime)
		lastID = options.Cursor.ID
	}
	rows, err := reader.Query(ctx, `SELECT competition.id,competition.name,competition.format,competition.status,
		competition.starts_at,finish.placement,
        coalesce(entrants.total,0)::integer,competition.max_entries,
        coalesce(record.matches_played,0)::integer,coalesce(record.wins,0)::integer,
        coalesce(record.draws,0)::integer,coalesce(record.losses,0)::integer
	  FROM entry_members membership
	  JOIN competition_entries entry ON entry.id=membership.entry_id
	  JOIN competitions competition ON competition.id=entry.competition_id
	  LEFT JOIN competition_entry_placements finish
	    ON finish.competition_id=competition.id AND finish.entry_id=entry.id
      LEFT JOIN LATERAL (
        SELECT count(*) AS total FROM competition_entries counted
        WHERE counted.competition_id=competition.id AND counted.status<>'withdrawn'
      ) entrants ON true
      LEFT JOIN LATERAL (
        SELECT count(*) AS matches_played,
		  count(*) FILTER (WHERE
		    (match.home_entry_id=entry.id AND (submission.home_score>submission.away_score
		      OR (submission.home_score=submission.away_score AND submission.home_tiebreak_score>submission.away_tiebreak_score)))
		    OR (match.away_entry_id=entry.id AND (submission.away_score>submission.home_score
		      OR (submission.home_score=submission.away_score AND submission.away_tiebreak_score>submission.home_tiebreak_score)))) AS wins,
		  count(*) FILTER (WHERE submission.home_score=submission.away_score AND submission.tiebreak_type IS NULL) AS draws,
		  count(*) FILTER (WHERE
		    (match.home_entry_id=entry.id AND (submission.home_score<submission.away_score
		      OR (submission.home_score=submission.away_score AND submission.home_tiebreak_score<submission.away_tiebreak_score)))
		    OR (match.away_entry_id=entry.id AND (submission.away_score<submission.home_score
		      OR (submission.home_score=submission.away_score AND submission.away_tiebreak_score<submission.home_tiebreak_score)))) AS losses
        FROM matches match
        JOIN result_submissions submission
          ON submission.match_id=match.id AND submission.status='confirmed'
        WHERE match.competition_id=competition.id
          AND (match.home_entry_id=entry.id OR match.away_entry_id=entry.id)
          AND match.state IN ('completed','forfeit')
      ) record ON true
      WHERE membership.user_id=$1
        AND membership.roster_role IN ('starter','substitute')
        AND competition.status<>'draft'
        AND competition.starts_at<=$2
        AND ($3::timestamptz IS NULL OR (competition.starts_at,competition.id)<($3,$4::uuid))
      ORDER BY competition.starts_at DESC,competition.id DESC
      LIMIT $5`, playerID, options.SnapshotAt, lastTime, lastID, options.Limit+1)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item publicCompetitionHistoryItem
		if err := rows.Scan(&item.CompetitionID, &item.Name, &item.Format, &item.Status, &item.StartedAt,
			&item.Placement, &item.Entrants, &item.MaxEntries, &item.Record.MatchesPlayed,
			&item.Record.Wins, &item.Record.Draws, &item.Record.Losses); err != nil {
			return result, err
		}
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
			Kind: "player-competitions", ExpiresAt: expiresAt, SnapshotAt: options.SnapshotAt.UnixNano(),
			Query: playerID, SortTime: last.StartedAt.UnixNano(), ID: last.CompetitionID,
		}, s.config.AccessTokenSecret)
		if encodeErr != nil {
			return result, encodeErr
		}
		result.Page.NextCursor = stringPointer(next)
	}
	result.Page.HasMore = hasMore
	return result, nil
}

func requireDiscoverablePlayer(ctx context.Context, reader rowsQueryer, playerID string) error {
	var exists bool
	err := reader.QueryRow(ctx, `SELECT EXISTS(
      SELECT 1 FROM users player
      JOIN player_profiles profile ON profile.user_id=player.id
      WHERE player.id=$1 AND player.status='active' AND profile.discoverable=true
    )`, playerID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return pgx.ErrNoRows
	}
	return nil
}
