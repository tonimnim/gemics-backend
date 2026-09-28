BEGIN;

-- Every finalized match version may drive the bracket exactly once. The
-- material outcome hash makes a retry with the same outcome a replay, while a
-- second outcome for the same version is an explicit conflict rather than a
-- silent double advance. The response is stored so a replay never has to infer
-- what the first transaction changed from today's graph state.
CREATE TABLE match_progression_applications (
    source_match_id uuid NOT NULL,
    finalized_match_version integer NOT NULL CHECK (finalized_match_version > 0),
    competition_id uuid NOT NULL,
    stage_id uuid NOT NULL,
    outcome_hash bytea NOT NULL CHECK (octet_length(outcome_hash) = 32),
    outcome jsonb NOT NULL CHECK (jsonb_typeof(outcome) = 'object'),
    cause text NOT NULL CHECK (cause IN
        ('player_confirmation', 'referee', 'timeout_forfeit', 'withdrawal',
         'disqualification', 'admin_correction')),
    actor_user_id uuid REFERENCES users(id),
    application_result jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(application_result) = 'object'),
    applied_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (source_match_id, finalized_match_version),
    FOREIGN KEY (source_match_id, competition_id)
        REFERENCES matches (id, competition_id) ON DELETE CASCADE,
    FOREIGN KEY (stage_id, competition_id)
        REFERENCES competition_stages (id, competition_id) ON DELETE CASCADE
);

CREATE INDEX match_progression_applications_competition_idx
    ON match_progression_applications (competition_id, applied_at DESC, source_match_id);
CREATE INDEX match_progression_applications_stage_idx
    ON match_progression_applications (stage_id, applied_at DESC, source_match_id);

-- Round-robin matches do not have dependency edges, so their durable
-- progression event is the standings write itself.
ALTER TABLE progression_events
    DROP CONSTRAINT progression_events_kind_check,
    ADD CONSTRAINT progression_events_kind_check CHECK (kind IN
        ('slot_filled', 'slot_voided', 'match_readied', 'match_forfeited', 'match_cancelled',
         'round_released', 'stage_completed', 'placements_written', 'subgraph_unwound',
         'standings_updated'));

COMMIT;
