package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type evidenceAccessView struct {
	ID                      string    `json:"id"`
	MediaKind               string    `json:"mediaKind"`
	MediaType               string    `json:"mediaType"`
	ByteSize                int64     `json:"byteSize"`
	VerifiedDurationSeconds *float64  `json:"verifiedDurationSeconds,omitempty"`
	DownloadURL             string    `json:"downloadUrl"`
	ExpiresAt               time.Time `json:"expiresAt"`
}

type gameScoreInput struct {
	HomeScore int `json:"homeScore"`
	AwayScore int `json:"awayScore"`
}

type tiebreakScoreInput struct {
	Type      string `json:"type"`
	HomeScore int    `json:"homeScore"`
	AwayScore int    `json:"awayScore"`
}

type ratingChangeView struct {
	PlayerID string `json:"playerId"`
	GameID   string `json:"gameId"`
	Before   int    `json:"before"`
	After    int    `json:"after"`
	Delta    int    `json:"delta"`
}

func (s *Server) getEvidenceAccess(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	if s.evidenceStore == nil {
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Private evidence storage is not configured.")
		return
	}
	evidenceID := strings.TrimSpace(r.PathValue("id"))
	if !uuidPattern.MatchString(evidenceID) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence not found.")
		return
	}
	userID := identityFromContext(r.Context()).UserID
	// Players reach only what they uploaded; opponents and organizers never do.
	// Reviewers and admins reach bound evidence, except a match's report
	// screenshots when they play in, captain in or organize that match (D27),
	// and the unbound uploads behind an evidence_unavailable review (T16) under
	// the same conflict rule. The role list must equal the platformRoleCan
	// grants of platformResultReviewManage.
	query := `SELECT evidence.object_key,evidence.media_kind,evidence.media_type,evidence.byte_size,
			evidence.verified_duration_seconds
		FROM evidence_uploads evidence
		WHERE evidence.id=$1 AND evidence.status='completed' AND evidence.media_kind='image'
		  AND (evidence.owner_user_id=$2
			OR (EXISTS (
				SELECT 1 FROM platform_staff_roles staff JOIN users player ON player.id=staff.user_id
				WHERE staff.user_id=$2 AND staff.revoked_at IS NULL AND player.status='active'
				  AND staff.role IN ('reviewer','operator','admin'))
			  AND ((evidence.bound_kind IS NOT NULL AND NOT EXISTS (
					SELECT 1 FROM match_result_reports report
					JOIN matches bound_match ON bound_match.id=report.match_id
					WHERE evidence.bound_kind='match_result_report' AND report.id=evidence.bound_id
					  AND ` + resultReviewConflictClause("bound_match", "$2") + `))
				OR ` + reviewWindowUploadClause("evidence", "$2") + `)))`
	var objectKey, mediaKind, mediaType string
	var byteSize int64
	var verifiedDuration *float64
	// Authorization state (membership and role revocation) must be read from the
	// writer. A lagging replica could otherwise mint a signed URL after access
	// has been revoked.
	err := s.db.Writer.QueryRow(r.Context(), query, evidenceID, userID).
		Scan(&objectKey, &mediaKind, &mediaType, &byteSize, &verifiedDuration)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "evidence_not_found", "Evidence not found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to authorize the evidence.")
		return
	}
	intent, err := s.evidenceStore.PresignGet(r.Context(), objectKey, s.config.StoragePresignTTL)
	if err != nil {
		s.logger.Error("presign evidence download", "request_id", r.Header.Get("X-Request-ID"), "evidence_id", evidenceID, "error", err)
		writeError(w, http.StatusServiceUnavailable, "evidence_storage_unavailable", "Unable to prepare the private evidence view.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": evidenceAccessView{
		ID: evidenceID, MediaKind: mediaKind, MediaType: mediaType, ByteSize: byteSize,
		VerifiedDurationSeconds: verifiedDuration, DownloadURL: intent.URL, ExpiresAt: intent.ExpiresAt,
	}})
}

// resultRatingInput is a confirmed score between the two entry captains, the
// only outcome that moves Elo. Forfeits and removals never reach it.
type resultRatingInput struct {
	GameID                     string
	HomePlayerID, AwayPlayerID string
	HomeScore, AwayScore       int
	Tiebreak                   *tiebreakScoreInput
}

func applyConfirmedResultRatings(ctx context.Context, tx pgx.Tx, result resultRatingInput, occurredAt time.Time) ([]ratingChangeView, error) {
	playerIDs := []string{result.HomePlayerID, result.AwayPlayerID}
	sort.Strings(playerIDs)
	for _, playerID := range playerIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO player_game_ratings(user_id,game_id)
			VALUES ($1,$2) ON CONFLICT (user_id,game_id) DO NOTHING`, playerID, result.GameID); err != nil {
			return nil, err
		}
	}

	rows, err := tx.Query(ctx, `SELECT user_id::text,rating
		FROM player_game_ratings
		WHERE game_id=$1 AND user_id IN ($2::uuid,$3::uuid)
		ORDER BY user_id FOR UPDATE`, result.GameID, result.HomePlayerID, result.AwayPlayerID)
	if err != nil {
		return nil, err
	}
	ratings := make(map[string]int, 2)
	for rows.Next() {
		var playerID string
		var rating int
		if err = rows.Scan(&playerID, &rating); err != nil {
			rows.Close()
			return nil, err
		}
		ratings[playerID] = rating
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(ratings) != 2 {
		return nil, errors.New("rating rows are unavailable")
	}

	homeScore := 0.5
	homeWin, homeDraw, homeLoss := 0, 1, 0
	awayWin, awayDraw, awayLoss := 0, 1, 0
	homeWon, awayWon := resultWinner(result.HomeScore, result.AwayScore, result.Tiebreak)
	if homeWon {
		homeScore = 1
		homeWin, homeDraw, homeLoss = 1, 0, 0
		awayWin, awayDraw, awayLoss = 0, 0, 1
	} else if awayWon {
		homeScore = 0
		homeWin, homeDraw, homeLoss = 0, 0, 1
		awayWin, awayDraw, awayLoss = 1, 0, 0
	}
	homeBefore := ratings[result.HomePlayerID]
	awayBefore := ratings[result.AwayPlayerID]
	expectedHome := 1 / (1 + math.Pow(10, float64(awayBefore-homeBefore)/400))
	homeDelta := int(math.Round(32 * (homeScore - expectedHome)))
	awayDelta := -homeDelta
	homeAfter := min(10000, max(0, homeBefore+homeDelta))
	awayAfter := min(10000, max(0, awayBefore+awayDelta))

	type update struct {
		playerID            string
		after               int
		wins, draws, losses int
	}
	updates := []update{
		{playerID: result.HomePlayerID, after: homeAfter, wins: homeWin, draws: homeDraw, losses: homeLoss},
		{playerID: result.AwayPlayerID, after: awayAfter, wins: awayWin, draws: awayDraw, losses: awayLoss},
	}
	for _, item := range updates {
		tag, updateErr := tx.Exec(ctx, `UPDATE player_game_ratings
			SET rating=$1,matches_played=matches_played+1,wins=wins+$2,draws=draws+$3,losses=losses+$4,
				rating_version=nextval('player_game_rating_change_seq'),last_match_at=$5,updated_at=now()
			WHERE user_id=$6 AND game_id=$7`, item.after, item.wins, item.draws, item.losses, occurredAt, item.playerID, result.GameID)
		if updateErr != nil {
			return nil, updateErr
		}
		if tag.RowsAffected() != 1 {
			return nil, errors.New("rating update did not affect exactly one player")
		}
	}
	return []ratingChangeView{
		{PlayerID: result.HomePlayerID, GameID: result.GameID, Before: homeBefore, After: homeAfter, Delta: homeAfter - homeBefore},
		{PlayerID: result.AwayPlayerID, GameID: result.GameID, Before: awayBefore, After: awayAfter, Delta: awayAfter - awayBefore},
	}, nil
}

// lockCompletedEvidence is the only evidence locker. It locks the owner's
// uploads in id order and returns pgx.ErrNoRows unless every id is found once,
// processed, an image of an allowed media type and not yet bound.
func lockCompletedEvidence(ctx context.Context, tx pgx.Tx, evidenceIDs []string, ownerID string, allowedMediaTypes []string) error {
	ids := slices.Clone(evidenceIDs)
	slices.Sort(ids)
	if len(slices.Compact(slices.Clone(ids))) != len(ids) {
		return pgx.ErrNoRows
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text,status,media_kind,media_type,bound_id::text FROM evidence_uploads
		WHERE id = ANY($1::text[]::uuid[]) AND owner_user_id=$2 ORDER BY id FOR UPDATE`, ids, ownerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	usable := 0
	for rows.Next() {
		var evidenceID, status, mediaKind, mediaType string
		var boundID *string
		if err = rows.Scan(&evidenceID, &status, &mediaKind, &mediaType, &boundID); err != nil {
			return err
		}
		if status == "completed" && mediaKind == "image" && slices.Contains(allowedMediaTypes, mediaType) && boundID == nil {
			usable++
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if usable != len(ids) {
		return pgx.ErrNoRows
	}
	return nil
}

// validateScorePolicy checks a claim against the stage: 1..bestOf games that
// sum to the totals, round-robin draws allowed, and elimination ties decided
// by a penalty shootout.
func validateScorePolicy(claim scoreClaim, bestOf int, format string) string {
	if len(claim.Games) < 1 || len(claim.Games) > bestOf || bestOf < 1 {
		return "The game breakdown does not match this stage's best-of rule."
	}
	homeTotal, awayTotal := 0, 0
	for _, game := range claim.Games {
		if game.HomeScore < 0 || game.HomeScore > 99 || game.AwayScore < 0 || game.AwayScore > 99 {
			return "Every game score must be between 0 and 99."
		}
		homeTotal += game.HomeScore
		awayTotal += game.AwayScore
	}
	if homeTotal != claim.HomeScore || awayTotal != claim.AwayScore {
		return "The final score must equal the submitted game-score totals."
	}
	if claim.HomeScore == claim.AwayScore {
		if format == "round_robin" {
			if claim.Tiebreak != nil {
				return "Round-robin draws cannot include a penalty tiebreak."
			}
			return ""
		}
		if claim.Tiebreak == nil || claim.Tiebreak.Type != "penalties" || claim.Tiebreak.HomeScore < 0 || claim.Tiebreak.HomeScore > 99 ||
			claim.Tiebreak.AwayScore < 0 || claim.Tiebreak.AwayScore > 99 || claim.Tiebreak.HomeScore == claim.Tiebreak.AwayScore {
			return "This elimination match requires a valid penalty-shootout winner."
		}
		return ""
	}
	if claim.Tiebreak != nil {
		return "A penalty tiebreak is only valid when the final score is tied."
	}
	return ""
}

func resultWinner(homeScore, awayScore int, tiebreak *tiebreakScoreInput) (bool, bool) {
	if homeScore > awayScore {
		return true, false
	}
	if awayScore > homeScore {
		return false, true
	}
	if tiebreak == nil {
		return false, false
	}
	return tiebreak.HomeScore > tiebreak.AwayScore, tiebreak.AwayScore > tiebreak.HomeScore
}

func validUUIDList(values []string) bool {
	for _, value := range values {
		if !uuidPattern.MatchString(strings.TrimSpace(value)) {
			return false
		}
	}
	return true
}

func normalizeUUIDList(values []string) {
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
}

func readIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) < 8 || len(key) > 128 {
		writeError(w, http.StatusBadRequest, "idempotency_key_required", "Provide an Idempotency-Key between 8 and 128 characters.")
		return "", false
	}
	for _, character := range key {
		if character < 33 || character > 126 {
			writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "Idempotency-Key must use printable ASCII without spaces.")
			return "", false
		}
	}
	return key, true
}

var errIdempotencyConflict = errors.New("idempotency key request mismatch")

type idempotentReplay struct {
	Status int
	Body   []byte
}

func beginIdempotentRequest(ctx context.Context, tx pgx.Tx, scope, key, requestHash string) (*idempotentReplay, error) {
	if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE scope=$1 AND key=$2 AND expires_at<=now()`, scope, key); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys(scope,key,request_hash,expires_at)
		VALUES ($1,$2,$3,now()+interval '24 hours') ON CONFLICT DO NOTHING`, scope, key, requestHash); err != nil {
		return nil, err
	}
	var storedHash string
	var responseStatus *int
	var responseBody []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,response_status,response_body FROM idempotency_keys
		WHERE scope=$1 AND key=$2 FOR UPDATE`, scope, key).Scan(&storedHash, &responseStatus, &responseBody)
	if err != nil {
		return nil, err
	}
	if storedHash != requestHash {
		return nil, errIdempotencyConflict
	}
	if responseStatus != nil && len(responseBody) > 0 {
		return &idempotentReplay{Status: *responseStatus, Body: responseBody}, nil
	}
	return nil, nil
}

func finishIdempotentRequest(ctx context.Context, tx pgx.Tx, scope, key string, status int, body []byte) error {
	_, err := tx.Exec(ctx, `UPDATE idempotency_keys SET response_status=$1,response_body=$2
		WHERE scope=$3 AND key=$4`, status, json.RawMessage(body), scope, key)
	return err
}

func hashRequest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func writeResultRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func randomUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
