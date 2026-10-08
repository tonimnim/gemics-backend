package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gamics-io/gamics/services/api/internal/storage"
)

const (
	screenshotReadPoll     = 5 * time.Second
	screenshotReadMaxBytes = 25 << 20
	// screenshotMaxOutageRetries bounds how long a screenshot waits through a
	// reader outage: with backoff capped at 30 minutes, about a day.
	screenshotMaxOutageRetries = 52
	// screenshotBacklog is how old a queued screenshot may be and still be
	// read; older ones (for example queued while reading was off) are closed.
	screenshotBacklog = "14 days"
	// screenshotBusyRetry is how soon a screenshot is retried after the reader
	// shed load; jitter spreads replicas out.
	screenshotBusyRetry = 10 * time.Second
	// screenshotLearnMinConfidence is the confidence a reading needs before its
	// team names are learned from a staff decision.
	screenshotLearnMinConfidence = 0.8
	// screenshotSweepBatch bounds the reviews re-evaluated per tick.
	screenshotSweepBatch    = 20
	screenshotDeciderPrefix = "screenshot-reader:"
)

// objectReader is the part of the storage provider that downloads an object.
type objectReader interface {
	Read(ctx context.Context, objectKey string, maxBytes int64) ([]byte, error)
}

// queueScreenshotReadings asks the reader to read each screenshot bound to a
// final report, in the transaction that binds them.
func queueScreenshotReadings(ctx context.Context, tx pgx.Tx, reportID string, evidenceIDs []string) error {
	if len(evidenceIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO screenshot_readings(evidence_id,match_id,report_id)
		SELECT evidence.id,report.match_id,report.id
		FROM match_result_reports report CROSS JOIN unnest($2::uuid[]) evidence(id)
		WHERE report.id=$1
		ON CONFLICT (evidence_id) DO NOTHING`, reportID, evidenceIDs)
	return err
}

// screenshotReaderDeps returns the reader client and object store, or false
// when reading is not configured.
func (s *Server) screenshotReaderDeps() (visionClient, objectReader, bool) {
	if s.db == nil || s.config.VisionURL == "" || s.evidenceStore == nil {
		return visionClient{}, nil, false
	}
	store, ok := s.evidenceStore.(objectReader)
	if !ok {
		return visionClient{}, nil, false
	}
	client := visionClient{baseURL: s.config.VisionURL, token: s.config.VisionToken,
		http: &http.Client{Timeout: s.config.VisionTimeout}}
	return client, store, true
}

// runScreenshotReader reads queued screenshots. Every replica may run it:
// each claim takes a fresh token and a lease longer than one read, and every
// write names the token, so a late result from an expired claim is dropped.
func (s *Server) runScreenshotReader(ctx context.Context) {
	client, store, ok := s.screenshotReaderDeps()
	if !ok || !s.config.VisionWorker {
		return
	}
	s.logger.Info("screenshot reader enabled", "url", s.config.VisionURL, "autoDecide", s.config.VisionAutoDecide)
	ticker := time.NewTicker(screenshotReadPoll)
	defer ticker.Stop()
	var wasHealthy atomic.Bool
	wasHealthy.Store(true)
	for {
		s.screenshotReaderTick(ctx, client, store, &wasHealthy)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) screenshotReaderTick(ctx context.Context, client visionClient, store objectReader,
	wasHealthy *atomic.Bool) {
	if _, err := s.db.Writer.Exec(ctx, `UPDATE screenshot_readings SET status='failed',claim_token=NULL,
		last_error='Not read: queued too long ago.',updated_at=now()
		WHERE status='queued' AND created_at<now()-$1::interval`, screenshotBacklog); err != nil && ctx.Err() == nil {
		s.logger.Warn("close old screenshot readings", "error", err)
	}
	healthy := client.healthy(ctx)
	if healthy != wasHealthy.Swap(healthy) {
		if healthy {
			s.logger.Info("screenshot reader is back")
		} else {
			s.logger.Warn("screenshot reader is unavailable; screenshots wait until it is back")
		}
	}
	if healthy {
		// Claim only as many screenshots as can be read at once, and keep
		// going while every slot was filled and answered.
		for ctx.Err() == nil && s.readDueScreenshots(ctx, client, store) {
		}
	}
	if s.config.VisionAutoDecide {
		s.sweepScreenshotReviews(ctx)
	}
}

type screenshotJob struct {
	EvidenceID string
	MatchID    string
	ObjectKey  string
	MediaType  string
	Token      string
	Outages    int
}

// readDueScreenshots claims up to one screenshot per free slot and reads them
// all at once. It reports whether to claim more: only when every slot was
// filled and every screenshot got a final answer. A busy or restarting reader
// fails each claim in milliseconds, so draining on would walk the whole
// backlog past the health check; the next tick asks /healthz first.
func (s *Server) readDueScreenshots(ctx context.Context, client visionClient, store objectReader) bool {
	slots := max(1, s.config.VisionConcurrency)
	lease := s.config.VisionTimeout + time.Minute
	rows, err := s.db.Writer.Query(ctx, `WITH due AS (
			SELECT evidence_id FROM screenshot_readings
			WHERE status='queued' AND available_at<=now()
			ORDER BY available_at,evidence_id LIMIT $1 FOR UPDATE SKIP LOCKED)
		UPDATE screenshot_readings reading SET attempts=reading.attempts+1,claim_token=gen_random_uuid(),
			available_at=now()+make_interval(secs=>$2),updated_at=now()
		FROM due,evidence_uploads evidence
		WHERE reading.evidence_id=due.evidence_id AND evidence.id=reading.evidence_id
		RETURNING reading.evidence_id::text,reading.match_id::text,evidence.object_key,evidence.media_type,
			reading.claim_token::text,reading.outage_retries`, slots, lease.Seconds())
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("claim screenshots to read", "error", err)
		}
		return false
	}
	jobs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (screenshotJob, error) {
		var job screenshotJob
		err := row.Scan(&job.EvidenceID, &job.MatchID, &job.ObjectKey, &job.MediaType, &job.Token, &job.Outages)
		return job, err
	})
	if err != nil {
		s.logger.Warn("claim screenshots to read", "error", err)
		return false
	}
	var wait sync.WaitGroup
	var answered atomic.Int64
	for _, job := range jobs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if s.readScreenshot(ctx, client, store, job) {
				answered.Add(1)
			}
		}()
	}
	wait.Wait()
	return len(jobs) == slots && answered.Load() == int64(slots)
}

// readScreenshot reads one claimed screenshot. It reports whether the
// screenshot got a final answer: a reading, or a failure for good.
func (s *Server) readScreenshot(ctx context.Context, client visionClient, store objectReader, job screenshotJob) bool {
	image, err := store.Read(ctx, job.ObjectKey, screenshotReadMaxBytes)
	if err != nil {
		return s.failScreenshotReading(ctx, job, &visionError{Retryable: !errors.Is(err, storage.ErrNotFound) &&
			!errors.Is(err, storage.ErrObjectTooLarge), Message: "screenshot download failed"})
	}
	reading, err := client.read(ctx, image, job.MediaType, job.EvidenceID, screenshotDeciderPrefix+job.EvidenceID)
	if err != nil {
		var readerErr *visionError
		if !errors.As(err, &readerErr) {
			readerErr = &visionError{Retryable: true, Message: "reader failed"}
		}
		return s.failScreenshotReading(ctx, job, readerErr)
	}
	stored, err := s.storeScreenshotReading(ctx, job, reading)
	if err != nil {
		// A value the database rejects will be rejected again; anything else
		// is the database being unavailable.
		var pgErr *pgconn.PgError
		deterministic := errors.As(err, &pgErr) && (strings.HasPrefix(pgErr.Code, "22") || strings.HasPrefix(pgErr.Code, "23"))
		s.logger.Warn("store screenshot reading", "evidence_id", job.EvidenceID, "error", err)
		return s.failScreenshotReading(ctx, job, &visionError{Retryable: !deterministic, Message: "reading could not be stored"})
	}
	if stored && s.config.VisionAutoDecide {
		s.autoDecideFromScreenshots(ctx, job.MatchID)
	}
	return true
}

// failScreenshotReading gives up on an image the reader can never read, and
// waits out a reader outage with backoff for up to about a day. A failed
// reading leaves the review with staff. It reports whether it gave up.
func (s *Server) failScreenshotReading(ctx context.Context, job screenshotJob, readerErr *visionError) bool {
	message := truncateRunes(readerErr.Message, 500)
	if readerErr.Busy {
		// Shed load is not an outage: try again in seconds, spread out.
		delay := screenshotBusyRetry + time.Duration(rand.Int64N(int64(screenshotBusyRetry)))
		if _, err := s.db.Writer.Exec(ctx, `UPDATE screenshot_readings SET last_error=$3,claim_token=NULL,
			available_at=now()+make_interval(secs=>$4),updated_at=now()
			WHERE evidence_id=$1 AND claim_token=$2::uuid`, job.EvidenceID, job.Token, message, delay.Seconds()); err != nil {
			s.logger.Warn("reschedule screenshot", "evidence_id", job.EvidenceID, "error", err)
		}
		return false
	}
	if !readerErr.Retryable || job.Outages+1 >= screenshotMaxOutageRetries {
		if _, err := s.db.Writer.Exec(ctx, `UPDATE screenshot_readings SET status='failed',last_error=$3,
			claim_token=NULL,evaluated_at=NULL,updated_at=now()
			WHERE evidence_id=$1 AND claim_token=$2::uuid`, job.EvidenceID, job.Token, message); err != nil {
			s.logger.Warn("record screenshot failure", "evidence_id", job.EvidenceID, "error", err)
		}
		s.logger.Info("screenshot unreadable", "evidence_id", job.EvidenceID, "reason", message)
		return true
	}
	backoff := min(30*time.Second<<min(job.Outages, 10), 30*time.Minute)
	if _, err := s.db.Writer.Exec(ctx, `UPDATE screenshot_readings SET last_error=$3,outage_retries=outage_retries+1,
		claim_token=NULL,available_at=now()+make_interval(secs=>$4),updated_at=now()
		WHERE evidence_id=$1 AND claim_token=$2::uuid`, job.EvidenceID, job.Token, message, backoff.Seconds()); err != nil {
		s.logger.Warn("reschedule screenshot", "evidence_id", job.EvidenceID, "error", err)
	}
	return false
}

// storeScreenshotReading saves a reading under its claim and flags it when
// the same full stats table (proof) or the same image hash (a hint) was
// uploaded earlier for another match. Only the later upload is flagged: the
// earlier one is the original. Both lookups are exact and indexed, so the
// cost stays flat however many screenshots are stored.
func (s *Server) storeScreenshotReading(ctx context.Context, job screenshotJob, reading screenshotReading) (bool, error) {
	stats := map[string][2]int{}
	for key, value := range reading.Stats {
		stats[key] = value
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return false, err
	}
	fingerprint := screenshotStatsFingerprint(reading)
	var reusedMatchID, reuseKind *string
	err = s.db.Writer.QueryRow(ctx, `WITH mine AS (SELECT created_at FROM evidence_uploads WHERE id=$1)
		SELECT match_id::text,kind FROM (
			(SELECT other.match_id,'stats' AS kind,1 AS preference FROM screenshot_readings other
			 JOIN evidence_uploads upload ON upload.id=other.evidence_id
			 WHERE $4::text IS NOT NULL AND other.stats_fingerprint=$4 AND other.match_id<>$2
			   AND upload.created_at<(SELECT created_at FROM mine) LIMIT 1)
			UNION ALL
			(SELECT other.match_id,'image',2 FROM screenshot_readings other
			 JOIN evidence_uploads upload ON upload.id=other.evidence_id
			 WHERE $3::text IS NOT NULL AND other.image_hash=$3 AND other.match_id<>$2
			   AND upload.created_at<(SELECT created_at FROM mine) LIMIT 1)) reused
		ORDER BY preference LIMIT 1`, job.EvidenceID, job.MatchID, reading.ImageHash, fingerprint).
		Scan(&reusedMatchID, &reuseKind)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	var leftTeam, rightTeam *string
	var leftScore, rightScore *int
	if reading.Screen == "match_result" {
		leftTeam, rightTeam = &reading.LeftTeam, &reading.RightTeam
		leftScore, rightScore = &reading.LeftScore, &reading.RightScore
	}
	command, err := s.db.Writer.Exec(ctx, `UPDATE screenshot_readings SET status='read',screen=$3,confidence=$4,
		full_time=$5,left_team=$6,left_score=$7,right_team=$8,right_score=$9,left_penalties=$10,right_penalties=$11,
		stats=$12,image_hash=$13,stats_fingerprint=$14,reused_match_id=$15,reuse_kind=$16,engine=$17,model_version=$18,
		last_error=NULL,outage_retries=0,claim_token=NULL,evaluated_at=NULL,read_at=now(),available_at=now(),
		updated_at=now()
		WHERE evidence_id=$1 AND claim_token=$2::uuid`,
		job.EvidenceID, job.Token, reading.Screen, reading.Confidence, reading.FullTime, leftTeam, leftScore, rightTeam,
		rightScore, reading.LeftPenalties, reading.RightPenalties, statsJSON, reading.ImageHash, fingerprint,
		reusedMatchID, reuseKind, reading.Engine, reading.ModelVersion)
	if err != nil {
		return false, err
	}
	// No row means the claim expired or a reviewer asked for a new reading;
	// whoever holds the newer claim stores its own result.
	return command.RowsAffected() == 1, nil
}

// screenshotContext is everything the evaluation needs about one match.
type screenshotContext struct {
	HomeEntryID, AwayEntryID string
	HomeUserID, AwayUserID   string
	// HomeNames and AwayNames are the learned team names only.
	HomeNames, AwayNames []string
	HomeClaim, AwayClaim *claimScore
	BestOf               int
	Readings             []*evaluatedReading
}

func (c screenshotContext) evaluationInput(minConfidence float64) screenshotEvaluationInput {
	return screenshotEvaluationInput{Readings: c.Readings, HomeNames: c.HomeNames, AwayNames: c.AwayNames,
		HomeClaim: c.HomeClaim, AwayClaim: c.AwayClaim, BestOf: c.BestOf, MinConfidence: minConfidence}
}

func loadScreenshotContext(ctx context.Context, queryer rowsQueryer, matchID string) (screenshotContext, error) {
	var result screenshotContext
	if err := queryer.QueryRow(ctx, `SELECT m.home_entry_id::text,m.away_entry_id::text,
		home.captain_user_id::text,away.captain_user_id::text,stage.best_of
		FROM matches m
		JOIN competition_stages stage ON stage.id=m.stage_id
		JOIN competition_entries home ON home.id=m.home_entry_id
		JOIN competition_entries away ON away.id=m.away_entry_id
		WHERE m.id=$1`, matchID).Scan(&result.HomeEntryID, &result.AwayEntryID, &result.HomeUserID,
		&result.AwayUserID, &result.BestOf); err != nil {
		return result, err
	}
	// Only learned team names identify a side. Display names, usernames,
	// entry names and in-game names are all set by the players themselves.
	rows, err := queryer.Query(ctx, `SELECT user_id::text,name FROM player_team_names
		WHERE user_id=ANY($1::uuid[]) ORDER BY user_id,name_key`, []string{result.HomeUserID, result.AwayUserID})
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var userID, name string
		if err = rows.Scan(&userID, &name); err != nil {
			rows.Close()
			return result, err
		}
		if userID == result.HomeUserID {
			result.HomeNames = append(result.HomeNames, name)
		} else {
			result.AwayNames = append(result.AwayNames, name)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	// Each entry's current claim: its final report, or its first one.
	rows, err = queryer.Query(ctx, `SELECT DISTINCT ON (entry_id) entry_id::text,home_score,away_score,
		home_tiebreak_score,away_tiebreak_score,jsonb_array_length(game_results)
		FROM match_result_reports WHERE match_id=$1
		ORDER BY entry_id,(kind='final') DESC,reported_at DESC`, matchID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var entryID string
		var claim claimScore
		var games int
		if err = rows.Scan(&entryID, &claim.Score.HomeScore, &claim.Score.AwayScore, &claim.Score.HomePenalties,
			&claim.Score.AwayPenalties, &games); err != nil {
			rows.Close()
			return result, err
		}
		claim.Series = games > 1
		if entryID == result.HomeEntryID {
			result.HomeClaim = &claim
		} else if entryID == result.AwayEntryID {
			result.AwayClaim = &claim
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	rows, err = queryer.Query(ctx, `SELECT reading.evidence_id::text,report.entry_id::text,reading.status,
		reading.screen,reading.confidence::float8,reading.full_time,reading.left_team,reading.left_score,
		reading.right_team,reading.right_score,reading.left_penalties,reading.right_penalties,reading.stats,
		reading.image_hash,reading.reused_match_id::text,reading.reuse_kind,reading.last_error,reading.engine,
		reading.model_version
		FROM screenshot_readings reading JOIN match_result_reports report ON report.id=reading.report_id
		WHERE reading.match_id=$1 ORDER BY report.entry_id,reading.created_at,reading.evidence_id`, matchID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item evaluatedReading
		var entryID string
		var screen, leftTeam, rightTeam, engine, modelVersion *string
		var confidence *float64
		var leftScore, rightScore *int
		var stats []byte
		reading := screenshotReading{}
		if err = rows.Scan(&item.EvidenceID, &entryID, &item.Status, &screen, &confidence, &reading.FullTime,
			&leftTeam, &leftScore, &rightTeam, &rightScore, &reading.LeftPenalties, &reading.RightPenalties, &stats,
			&reading.ImageHash, &item.ReusedMatchID, &item.ReuseKind, &item.LastError, &engine, &modelVersion); err != nil {
			return result, err
		}
		item.UploadedBySide = "away"
		if entryID == result.HomeEntryID {
			item.UploadedBySide = "home"
		}
		if item.Status == "read" && screen != nil {
			reading.Screen = *screen
			reading.Confidence = valueOr(confidence, 0)
			reading.LeftTeam, reading.RightTeam = valueOrEmpty(leftTeam), valueOrEmpty(rightTeam)
			reading.LeftScore, reading.RightScore = valueOr(leftScore, 0), valueOr(rightScore, 0)
			reading.Engine, reading.ModelVersion = valueOrEmpty(engine), valueOrEmpty(modelVersion)
			if err = json.Unmarshal(stats, &reading.Stats); err != nil {
				return result, err
			}
			item.Reading = &reading
		}
		result.Readings = append(result.Readings, &item)
	}
	return result, rows.Err()
}

func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}

// sweepScreenshotReviews re-evaluates open disputes whose screenshots are all
// read but were not evaluated since, so a decision lost to a restart or a
// transient error is still made.
func (s *Server) sweepScreenshotReviews(ctx context.Context) {
	rows, err := s.db.Writer.Query(ctx, `SELECT DISTINCT review.match_id::text FROM match_result_reviews review
		JOIN screenshot_readings reading ON reading.match_id=review.match_id
		WHERE review.status='queued' AND review.reason='reports_differ' AND reading.evaluated_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM screenshot_readings pending
			WHERE pending.match_id=review.match_id AND pending.status='queued')
		LIMIT $1`, screenshotSweepBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("find screenshot reviews to evaluate", "error", err)
		}
		return
	}
	matchIDs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return
	}
	for _, matchID := range matchIDs {
		s.autoDecideFromScreenshots(ctx, matchID)
	}
}

// autoDecideFromScreenshots lets the reader decide a disputed result when the
// evaluation allows it. It decides through the same path staff use, as a
// system decider that can neither correct a score nor record a strike, and it
// never teaches itself team names.
func (s *Server) autoDecideFromScreenshots(ctx context.Context, matchID string) {
	tx, err := s.db.Writer.Begin(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("auto-decide from screenshots", "match_id", matchID, "error", err)
		}
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var reviewID string
	var version int
	err = tx.QueryRow(ctx, `SELECT id::text,version FROM match_result_reviews
		WHERE match_id=$1 AND status='queued' AND reason='reports_differ'`, matchID).Scan(&reviewID, &version)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			s.logger.Warn("load review for screenshots", "match_id", matchID, "error", err)
		}
		return
	}
	screenshots, err := loadScreenshotContext(ctx, tx, matchID)
	if err != nil {
		s.logger.Warn("load screenshots", "match_id", matchID, "error", err)
		return
	}
	evaluation := evaluateScreenshots(screenshots.evaluationInput(s.config.VisionAutoMinConfidence))
	if evaluation.Verdict == verdictPending {
		return
	}
	markEvaluated := func() {
		if _, err := s.db.Writer.Exec(ctx, `UPDATE screenshot_readings SET evaluated_at=now()
			WHERE match_id=$1 AND evaluated_at IS NULL`, matchID); err != nil && ctx.Err() == nil {
			s.logger.Warn("mark screenshots evaluated", "match_id", matchID, "error", err)
		}
	}
	if evaluation.Decision == "" || evaluation.Score == nil {
		_ = tx.Rollback(ctx)
		markEvaluated()
		return
	}
	note := fmt.Sprintf("Both players' screenshots read %s automatically, matching the %s player's claim.",
		formatScreenshotScore(*evaluation.Score), evaluation.SupportedSide)
	outcome, err := decideResultReviewInTx(ctx, tx, reviewID,
		reviewActor{Kind: "system", DeciderRef: screenshotDeciderPrefix + reviewID},
		reviewDecisionInput{ExpectedVersion: version, Decision: evaluation.Decision, Note: note})
	if err != nil {
		var rejection *reviewDecisionRejection
		if errors.As(err, &rejection) {
			_ = tx.Rollback(ctx)
			markEvaluated()
		} else {
			s.logger.Warn("auto-decide from screenshots", "review_id", reviewID, "error", err)
		}
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE screenshot_readings SET evaluated_at=now() WHERE match_id=$1`, matchID); err != nil {
		s.logger.Warn("auto-decide from screenshots", "review_id", reviewID, "error", err)
		return
	}
	if err = tx.Commit(ctx); err != nil {
		s.logger.Warn("auto-decide from screenshots", "review_id", reviewID, "error", err)
		return
	}
	s.logger.Info("review decided from screenshots", "review_id", reviewID, "decision", evaluation.Decision)
	s.invalidateCompetitionCachesContext(context.WithoutCancel(ctx), outcome.CompetitionID)
}

// learnTeamNamesSafely learns team names in a savepoint, so a failure never
// undoes the staff decision it follows.
func learnTeamNamesSafely(ctx context.Context, tx pgx.Tx, matchID string, s *Server) {
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return
	}
	if err = learnTeamNames(ctx, savepoint, matchID); err != nil {
		_ = savepoint.Rollback(ctx)
		if s != nil {
			s.logger.Warn("learn team names", "match_id", matchID, "error", err)
		}
		return
	}
	_ = savepoint.Commit(ctx)
}

// learnTeamNames records whose team was whose, after a staff decision only.
// Every result screen must come from both players, carry exactly the same
// team names (no OCR tolerance), have no flag, and fit the decided result one
// way round; a draw without penalties fits both ways and teaches nothing. A
// player who edits the names on their own screenshot therefore teaches
// nothing, because the opponent's real screenshot disagrees.
func learnTeamNames(ctx context.Context, tx pgx.Tx, matchID string) error {
	var result screenshotScore
	err := tx.QueryRow(ctx, `SELECT home_score,away_score,home_tiebreak_score,away_tiebreak_score
		FROM result_submissions WHERE match_id=$1 AND status='confirmed'
		ORDER BY submitted_at DESC,id DESC LIMIT 1`, matchID).
		Scan(&result.HomeScore, &result.AwayScore, &result.HomePenalties, &result.AwayPenalties)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	screenshots, err := loadScreenshotContext(ctx, tx, matchID)
	if err != nil {
		return err
	}
	if screenshots.BestOf > 1 {
		return nil
	}
	// Evaluate with no names known, only to get each reading's flags.
	evaluateScreenshots(screenshotEvaluationInput{Readings: screenshots.Readings,
		MinConfidence: screenshotLearnMinConfidence})
	var screens []screenshotReading
	sides := map[string]bool{}
	for _, item := range screenshots.Readings {
		if item.Reading == nil || item.Reading.Screen != "match_result" {
			continue
		}
		for _, flag := range item.Flags {
			if flag != "teams_unknown" && !slices.Contains(screenshotHints, flag) {
				return nil
			}
		}
		screens = append(screens, *item.Reading)
		sides[item.UploadedBySide] = true
	}
	if !sides["home"] || !sides["away"] {
		return nil
	}
	first := screens[0]
	leftKey, rightKey := teamNameKey(first.LeftTeam), teamNameKey(first.RightTeam)
	if leftKey == rightKey || len([]rune(leftKey)) < 2 || len([]rune(rightKey)) < 2 {
		return nil
	}
	for _, screen := range screens[1:] {
		if teamNameKey(screen.LeftTeam) != leftKey || teamNameKey(screen.RightTeam) != rightKey ||
			screen.LeftScore != first.LeftScore || screen.RightScore != first.RightScore ||
			!equalOptionalInt(screen.LeftPenalties, first.LeftPenalties) ||
			!equalOptionalInt(screen.RightPenalties, first.RightPenalties) {
			return nil
		}
	}
	homeLeft, _ := first.inMatchOrientation(orientationHomeLeft)
	homeRight, _ := first.inMatchOrientation(orientationHomeRight)
	var homeName, awayName string
	switch {
	case homeLeft.equals(result) && !homeRight.equals(result):
		homeName, awayName = first.LeftTeam, first.RightTeam
	case homeRight.equals(result) && !homeLeft.equals(result):
		homeName, awayName = first.RightTeam, first.LeftTeam
	default:
		return nil
	}
	for _, learned := range []struct{ userID, name string }{
		{screenshots.HomeUserID, homeName}, {screenshots.AwayUserID, awayName},
	} {
		if _, err = tx.Exec(ctx, `INSERT INTO player_team_names(user_id,name_key,name) VALUES ($1,$2,$3)
			ON CONFLICT (user_id,name_key) DO UPDATE SET name=EXCLUDED.name,
				confirmations=player_team_names.confirmations+1,last_confirmed_at=now()`,
			learned.userID, teamNameKey(learned.name), learned.name); err != nil {
			return err
		}
	}
	return nil
}
