BEGIN;

DROP TABLE IF EXISTS player_team_names CASCADE;
DROP TABLE IF EXISTS screenshot_readings CASCADE;
DROP TABLE IF EXISTS game_account_verification_evidence CASCADE;
DROP TABLE IF EXISTS game_account_verification_requests CASCADE;
DROP TABLE IF EXISTS player_strikes CASCADE;
DROP TABLE IF EXISTS match_result_reviews CASCADE;
DROP TABLE IF EXISTS match_result_report_evidence CASCADE;
DROP TABLE IF EXISTS match_result_reports CASCADE;
DROP TABLE IF EXISTS match_result_verifications CASCADE;
DROP TABLE IF EXISTS result_submissions CASCADE;
DROP TABLE IF EXISTS profile_media_uploads CASCADE;
DROP TABLE IF EXISTS evidence_media_processing_jobs CASCADE;
DROP TABLE IF EXISTS evidence_uploads CASCADE;

COMMIT;
