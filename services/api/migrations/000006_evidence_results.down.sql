BEGIN;

DROP TABLE IF EXISTS result_confirmation_evidence;
ALTER TABLE result_confirmations DROP CONSTRAINT IF EXISTS result_confirmations_decision_payload_chk;
ALTER TABLE result_confirmations DROP COLUMN IF EXISTS reason_code;

DROP TABLE IF EXISTS result_submission_evidence;
DROP INDEX IF EXISTS result_submissions_one_pending_per_match_uidx;
ALTER TABLE result_submissions DROP CONSTRAINT IF EXISTS result_submissions_games_array_chk;
ALTER TABLE result_submissions DROP CONSTRAINT IF EXISTS result_submissions_score_upper_bound_chk;
ALTER TABLE result_submissions DROP CONSTRAINT IF EXISTS result_submissions_tiebreak_chk;
ALTER TABLE result_submissions DROP CONSTRAINT IF EXISTS result_submissions_match_version_positive_chk;
ALTER TABLE result_submissions DROP COLUMN IF EXISTS away_tiebreak_score;
ALTER TABLE result_submissions DROP COLUMN IF EXISTS home_tiebreak_score;
ALTER TABLE result_submissions DROP COLUMN IF EXISTS tiebreak_type;
ALTER TABLE result_submissions DROP COLUMN IF EXISTS match_version;

DROP TABLE IF EXISTS evidence_uploads;

COMMIT;
