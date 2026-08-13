BEGIN;

CREATE UNIQUE INDEX competition_stages_id_competition_uidx
    ON competition_stages (id, competition_id);
CREATE UNIQUE INDEX competition_entries_id_competition_uidx
    ON competition_entries (id, competition_id);
CREATE UNIQUE INDEX IF NOT EXISTS game_accounts_id_user_uidx
    ON game_accounts (id, user_id);

ALTER TABLE entry_members ADD COLUMN competition_id uuid;
UPDATE entry_members member
SET competition_id = entry.competition_id
FROM competition_entries entry
WHERE entry.id = member.entry_id;
ALTER TABLE entry_members ALTER COLUMN competition_id SET NOT NULL;
ALTER TABLE entry_members
    ADD CONSTRAINT entry_members_entry_competition_fk
        FOREIGN KEY (entry_id, competition_id)
        REFERENCES competition_entries (id, competition_id) NOT VALID,
    ADD CONSTRAINT entry_members_game_account_owner_fk
        FOREIGN KEY (game_account_id, user_id)
        REFERENCES game_accounts (id, user_id) NOT VALID;
CREATE UNIQUE INDEX entry_members_one_player_per_competition_uidx
    ON entry_members (competition_id, user_id)
    WHERE roster_role IN ('starter', 'substitute');

ALTER TABLE matches
    ADD CONSTRAINT matches_stage_competition_fk
        FOREIGN KEY (stage_id, competition_id)
        REFERENCES competition_stages (id, competition_id) NOT VALID,
    ADD CONSTRAINT matches_home_competition_fk
        FOREIGN KEY (home_entry_id, competition_id)
        REFERENCES competition_entries (id, competition_id) NOT VALID,
    ADD CONSTRAINT matches_away_competition_fk
        FOREIGN KEY (away_entry_id, competition_id)
        REFERENCES competition_entries (id, competition_id) NOT VALID,
    ADD CONSTRAINT matches_winner_competition_fk
        FOREIGN KEY (winner_entry_id, competition_id)
        REFERENCES competition_entries (id, competition_id) NOT VALID,
    ADD CONSTRAINT matches_round_positive_chk CHECK (round_number > 0) NOT VALID,
    ADD CONSTRAINT matches_number_positive_chk CHECK (match_number > 0) NOT VALID,
    ADD CONSTRAINT matches_version_positive_chk CHECK (version > 0) NOT VALID,
    ADD CONSTRAINT matches_winner_participant_chk CHECK (
        winner_entry_id IS NULL
        OR winner_entry_id IS NOT DISTINCT FROM home_entry_id
        OR winner_entry_id IS NOT DISTINCT FROM away_entry_id
    ) NOT VALID;

ALTER TABLE competition_entries
    ADD CONSTRAINT competition_entries_seed_positive_chk
        CHECK (seed IS NULL OR seed > 0) NOT VALID;
CREATE UNIQUE INDEX competition_entries_seed_uidx
    ON competition_entries (competition_id, seed) WHERE seed IS NOT NULL;

CREATE UNIQUE INDEX result_submissions_id_match_uidx
    ON result_submissions (id, match_id);
ALTER TABLE result_submissions
    ADD CONSTRAINT result_submissions_supersedes_same_match_fk
        FOREIGN KEY (supersedes_id, match_id)
        REFERENCES result_submissions (id, match_id) NOT VALID,
    ADD CONSTRAINT result_submissions_not_self_superseding_chk
        CHECK (supersedes_id IS NULL OR supersedes_id <> id) NOT VALID;
ALTER TABLE disputes
    ADD CONSTRAINT disputes_submission_match_fk
        FOREIGN KEY (submission_id, match_id)
        REFERENCES result_submissions (id, match_id) NOT VALID;
CREATE UNIQUE INDEX result_submissions_one_confirmed_per_match_uidx
    ON result_submissions (match_id) WHERE status = 'confirmed';
CREATE UNIQUE INDEX disputes_one_active_per_match_uidx
    ON disputes (match_id) WHERE status IN ('open', 'under_review');

WITH ranked AS (
    SELECT id, row_number() OVER (PARTITION BY lower(email) ORDER BY created_at DESC, id DESC) AS position
    FROM email_otp_challenges WHERE consumed_at IS NULL
)
UPDATE email_otp_challenges challenge SET consumed_at=now()
FROM ranked WHERE ranked.id=challenge.id AND ranked.position > 1;
CREATE UNIQUE INDEX email_otp_one_active_per_email_uidx
    ON email_otp_challenges (lower(email)) WHERE consumed_at IS NULL;

ALTER TABLE refresh_sessions
    ADD COLUMN previous_token_hash bytea,
    ADD COLUMN previous_token_valid_until timestamptz,
    ADD CONSTRAINT refresh_sessions_previous_token_pair_chk CHECK (
        (previous_token_hash IS NULL) = (previous_token_valid_until IS NULL)
    ) NOT VALID;
CREATE INDEX refresh_sessions_previous_token_idx
    ON refresh_sessions (previous_token_hash)
    WHERE previous_token_hash IS NOT NULL;

CREATE INDEX organization_members_user_idx
    ON organization_members (user_id, organization_id) INCLUDE (role);
CREATE INDEX competitions_org_status_starts_idx
    ON competitions (organization_id, status, starts_at DESC, id);
CREATE INDEX competitions_public_starts_idx
    ON competitions (starts_at, id)
    WHERE status IN ('published', 'registration_open', 'check_in', 'running');
CREATE INDEX competition_entries_comp_status_created_idx
    ON competition_entries (competition_id, status, created_at DESC, id);
CREATE INDEX competition_entries_captain_created_idx
    ON competition_entries (captain_user_id, created_at DESC, id);
CREATE INDEX disputes_assignee_queue_idx
    ON disputes (assigned_to, status, opened_at, id)
    WHERE status IN ('open', 'under_review');
CREATE INDEX users_admin_status_idx ON users (status, created_at DESC, id);
CREATE INDEX organizations_admin_status_idx ON organizations (status, created_at DESC, id);
CREATE INDEX email_otp_challenges_expiry_idx ON email_otp_challenges (expires_at);
CREATE INDEX refresh_sessions_revoked_idx
    ON refresh_sessions (revoked_at) WHERE revoked_at IS NOT NULL;
CREATE INDEX audit_events_retention_idx ON audit_events (occurred_at, id);
CREATE INDEX outbox_events_processed_idx
    ON outbox_events (processed_at, id) WHERE processed_at IS NOT NULL;

ALTER TABLE entry_members VALIDATE CONSTRAINT entry_members_entry_competition_fk;
ALTER TABLE entry_members VALIDATE CONSTRAINT entry_members_game_account_owner_fk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_stage_competition_fk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_home_competition_fk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_away_competition_fk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_winner_competition_fk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_round_positive_chk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_number_positive_chk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_version_positive_chk;
ALTER TABLE matches VALIDATE CONSTRAINT matches_winner_participant_chk;
ALTER TABLE competition_entries VALIDATE CONSTRAINT competition_entries_seed_positive_chk;
ALTER TABLE result_submissions VALIDATE CONSTRAINT result_submissions_supersedes_same_match_fk;
ALTER TABLE result_submissions VALIDATE CONSTRAINT result_submissions_not_self_superseding_chk;
ALTER TABLE disputes VALIDATE CONSTRAINT disputes_submission_match_fk;
ALTER TABLE refresh_sessions VALIDATE CONSTRAINT refresh_sessions_previous_token_pair_chk;

COMMIT;
