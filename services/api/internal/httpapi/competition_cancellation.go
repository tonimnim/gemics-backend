package httpapi

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// cancelCompetitionMatches ends every live match of a competition being
// cancelled (T17), in the cancel transaction and under the competition gate the
// caller already holds. Nobody is removed, struck or rated, progression does
// not run and no per-match push is sent: the competition itself is over, and
// its cancellation already drives refunds. Open verifications resolve and
// queued reviews close, so no worker or reviewer acts on the matches later.
func cancelCompetitionMatches(ctx context.Context, tx pgx.Tx, organizationID, competitionID, actorID,
	requestID string) error {
	// Terminal timestamps come from the database clock read after the gate,
	// like every other result transition.
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&now); err != nil {
		return err
	}
	now = now.UTC()
	matchIDs, err := lockLiveCompetitionMatches(ctx, tx, competitionID)
	if err != nil || len(matchIDs) == 0 {
		return err
	}
	command, err := tx.Exec(ctx, `UPDATE matches SET state='cancelled',winner_entry_id=NULL,
		completion_reason='competition_cancelled',completed_at=$2,version=version+1,updated_at=now()
		WHERE competition_id=$1 AND id = ANY($3::text[]::uuid[])`, competitionID, now, matchIDs)
	if err != nil {
		return err
	}
	if command.RowsAffected() != int64(len(matchIDs)) {
		return fmt.Errorf("%w: %d of %d locked matches were cancelled", errMatchResolutionChanged,
			command.RowsAffected(), len(matchIDs))
	}
	// Only a live match has an open verification, so the locked ids cover them
	// all and keep the update on the primary key.
	verifications, err := tx.Exec(ctx, `UPDATE match_result_verifications SET phase='resolved',
		resolution='competition_cancelled',resolved_at=$2,version=version+1,updated_at=now()
		WHERE competition_id=$1 AND match_id = ANY($3::text[]::uuid[]) AND phase<>'resolved'`,
		competitionID, now, matchIDs)
	if err != nil {
		return err
	}
	reviews, err := tx.Exec(ctx, `UPDATE match_result_reviews SET status='closed',decided_at=$2,
		version=version+1,updated_at=now()
		WHERE competition_id=$1 AND status='queued'`, competitionID, now)
	if err != nil {
		return err
	}
	return appendAuditContext(ctx, tx, requestID, organizationID, actorID, "competition.matches_cancelled",
		"competition", competitionID, nil, map[string]any{
			"matchIds": matchIDs, "verificationsResolved": verifications.RowsAffected(),
			"reviewsClosed": reviews.RowsAffected(),
		})
}

// lockLiveCompetitionMatches locks, in id order, every match that has not
// reached a terminal state.
func lockLiveCompetitionMatches(ctx context.Context, tx pgx.Tx, competitionID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text FROM matches WHERE competition_id=$1
		AND state IN ('pending','ready','in_progress','awaiting_confirmation','disputed')
		ORDER BY id FOR UPDATE`, competitionID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
