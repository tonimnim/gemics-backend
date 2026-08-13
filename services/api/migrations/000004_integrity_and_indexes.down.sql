BEGIN;

ALTER TABLE disputes DROP CONSTRAINT IF EXISTS disputes_submission_match_fk;
ALTER TABLE result_submissions
    DROP CONSTRAINT IF EXISTS result_submissions_supersedes_same_match_fk,
    DROP CONSTRAINT IF EXISTS result_submissions_not_self_superseding_chk;
ALTER TABLE competition_entries DROP CONSTRAINT IF EXISTS competition_entries_seed_positive_chk;
ALTER TABLE matches
    DROP CONSTRAINT IF EXISTS matches_stage_competition_fk,
    DROP CONSTRAINT IF EXISTS matches_home_competition_fk,
    DROP CONSTRAINT IF EXISTS matches_away_competition_fk,
    DROP CONSTRAINT IF EXISTS matches_winner_competition_fk,
    DROP CONSTRAINT IF EXISTS matches_round_positive_chk,
    DROP CONSTRAINT IF EXISTS matches_number_positive_chk,
    DROP CONSTRAINT IF EXISTS matches_version_positive_chk,
    DROP CONSTRAINT IF EXISTS matches_winner_participant_chk;
ALTER TABLE entry_members
    DROP CONSTRAINT IF EXISTS entry_members_entry_competition_fk,
    DROP CONSTRAINT IF EXISTS entry_members_game_account_owner_fk;
ALTER TABLE refresh_sessions DROP CONSTRAINT IF EXISTS refresh_sessions_previous_token_pair_chk;

DROP INDEX IF EXISTS outbox_events_processed_idx;
DROP INDEX IF EXISTS audit_events_retention_idx;
DROP INDEX IF EXISTS refresh_sessions_revoked_idx;
DROP INDEX IF EXISTS refresh_sessions_previous_token_idx;
DROP INDEX IF EXISTS email_otp_challenges_expiry_idx;
DROP INDEX IF EXISTS organizations_admin_status_idx;
DROP INDEX IF EXISTS users_admin_status_idx;
DROP INDEX IF EXISTS disputes_assignee_queue_idx;
DROP INDEX IF EXISTS competition_entries_captain_created_idx;
DROP INDEX IF EXISTS competition_entries_comp_status_created_idx;
DROP INDEX IF EXISTS competitions_public_starts_idx;
DROP INDEX IF EXISTS competitions_org_status_starts_idx;
DROP INDEX IF EXISTS organization_members_user_idx;
DROP INDEX IF EXISTS email_otp_one_active_per_email_uidx;
DROP INDEX IF EXISTS disputes_one_active_per_match_uidx;
DROP INDEX IF EXISTS result_submissions_one_confirmed_per_match_uidx;
DROP INDEX IF EXISTS result_submissions_id_match_uidx;
DROP INDEX IF EXISTS competition_entries_seed_uidx;
DROP INDEX IF EXISTS entry_members_one_player_per_competition_uidx;
DROP INDEX IF EXISTS competition_entries_id_competition_uidx;
DROP INDEX IF EXISTS competition_stages_id_competition_uidx;
ALTER TABLE refresh_sessions
    DROP COLUMN IF EXISTS previous_token_valid_until,
    DROP COLUMN IF EXISTS previous_token_hash;
ALTER TABLE entry_members DROP COLUMN IF EXISTS competition_id;

COMMIT;
