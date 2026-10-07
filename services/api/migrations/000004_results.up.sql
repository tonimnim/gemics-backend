BEGIN;

-- Screenshot evidence, blind score reports, Gamics result reviews, strikes and game account verification.

-- evidence_uploads

CREATE TABLE evidence_uploads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_user_id uuid NOT NULL,
    storage_provider text DEFAULT 's3'::text NOT NULL,
    object_key text NOT NULL,
    media_type text NOT NULL,
    byte_size bigint NOT NULL,
    checksum_sha256 bytea NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    upload_expires_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    provider_etag text,
    bound_kind text,
    bound_id uuid,
    bound_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    media_kind text DEFAULT 'image'::text NOT NULL,
    declared_duration_seconds numeric(8,3),
    verified_duration_seconds numeric(8,3),
    processing_error_code text,
    processed_at timestamp with time zone,
    CONSTRAINT evidence_uploads_bound_kind_v3_chk CHECK ((bound_kind = ANY (ARRAY['result_submission'::text, 'result_dispute'::text, 'dispute_case'::text, 'game_account_verification'::text, 'match_result_report'::text]))),
    CONSTRAINT evidence_uploads_byte_size_v2_chk CHECK (((byte_size > 0) AND (byte_size <= 262144000))),
    CONSTRAINT evidence_uploads_check CHECK (((bound_kind IS NULL) = (bound_id IS NULL))),
    CONSTRAINT evidence_uploads_check1 CHECK (((bound_id IS NULL) = (bound_at IS NULL))),
    CONSTRAINT evidence_uploads_check2 CHECK (((status <> 'completed'::text) OR (completed_at IS NOT NULL))),
    CONSTRAINT evidence_uploads_check3 CHECK (((bound_id IS NULL) OR (status = 'completed'::text))),
    CONSTRAINT evidence_uploads_checksum_sha256_check CHECK ((octet_length(checksum_sha256) = 32)),
    CONSTRAINT evidence_uploads_image_only_intake_chk CHECK (((media_kind = 'image'::text) OR (status = ANY (ARRAY['completed'::text, 'failed'::text, 'rejected'::text, 'expired'::text])))),
    CONSTRAINT evidence_uploads_media_duration_chk CHECK ((((media_kind = 'image'::text) AND (media_type ~~ 'image/%'::text) AND (declared_duration_seconds IS NULL) AND (verified_duration_seconds IS NULL)) OR ((media_kind = 'video'::text) AND (media_type ~~ 'video/%'::text) AND (declared_duration_seconds > (0)::numeric) AND (declared_duration_seconds <= (120)::numeric) AND ((verified_duration_seconds IS NULL) OR (verified_duration_seconds > (0)::numeric))))),
    CONSTRAINT evidence_uploads_media_kind_chk CHECK ((media_kind = ANY (ARRAY['image'::text, 'video'::text]))),
    CONSTRAINT evidence_uploads_media_type_v2_chk CHECK ((media_type = ANY (ARRAY['image/jpeg'::text, 'image/png'::text, 'image/heic'::text, 'image/heif'::text, 'video/mp4'::text, 'video/quicktime'::text, 'video/webm'::text]))),
    CONSTRAINT evidence_uploads_processed_state_chk CHECK ((((status = ANY (ARRAY['completed'::text, 'rejected'::text])) AND (processed_at IS NOT NULL)) OR (status <> ALL (ARRAY['completed'::text, 'rejected'::text])))),
    CONSTRAINT evidence_uploads_processing_error_code_chk CHECK (((processing_error_code IS NULL) OR (processing_error_code ~ '^[a-z][a-z0-9_]{0,63}$'::text))),
    CONSTRAINT evidence_uploads_processing_state_chk CHECK (((status <> 'processing'::text) OR ((completed_at IS NOT NULL) AND (processed_at IS NULL)))),
    CONSTRAINT evidence_uploads_status_v2_chk CHECK ((status = ANY (ARRAY['pending'::text, 'processing'::text, 'completed'::text, 'failed'::text, 'rejected'::text, 'expired'::text]))),
    CONSTRAINT evidence_uploads_storage_provider_check CHECK ((storage_provider = 's3'::text)),
    CONSTRAINT evidence_uploads_verified_video_ready_chk CHECK (((status <> 'completed'::text) OR (media_kind <> 'video'::text) OR ((verified_duration_seconds > (0)::numeric) AND (verified_duration_seconds <= (120)::numeric))))
);
ALTER TABLE ONLY evidence_uploads
    ADD CONSTRAINT evidence_uploads_object_key_key UNIQUE (object_key);
ALTER TABLE ONLY evidence_uploads
    ADD CONSTRAINT evidence_uploads_pkey PRIMARY KEY (id);
CREATE INDEX evidence_uploads_bound_idx ON evidence_uploads USING btree (bound_kind, bound_id) WHERE (bound_id IS NOT NULL);
CREATE INDEX evidence_uploads_owner_created_idx ON evidence_uploads USING btree (owner_user_id, created_at DESC, id);
CREATE INDEX evidence_uploads_pending_expiry_idx ON evidence_uploads USING btree (upload_expires_at, id) WHERE (status = 'pending'::text);
CREATE INDEX evidence_uploads_processing_idx ON evidence_uploads USING btree (created_at, id) WHERE (status = 'processing'::text);

-- evidence_media_processing_jobs

CREATE TABLE evidence_media_processing_jobs (
    evidence_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    attempts integer DEFAULT 0 NOT NULL,
    available_at timestamp with time zone DEFAULT now() NOT NULL,
    locked_at timestamp with time zone,
    locked_by text,
    last_error text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    finished_at timestamp with time zone,
    lease_recoveries integer DEFAULT 0 NOT NULL,
    CONSTRAINT evidence_media_processing_jobs_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT evidence_media_processing_jobs_check CHECK (((locked_at IS NULL) = (locked_by IS NULL))),
    CONSTRAINT evidence_media_processing_jobs_check1 CHECK (((status <> ALL (ARRAY['succeeded'::text, 'rejected'::text, 'failed'::text])) OR (finished_at IS NOT NULL))),
    CONSTRAINT evidence_media_processing_jobs_last_error_class_chk CHECK (((last_error IS NULL) OR (last_error ~ '^[a-z][a-z0-9_]{0,63}$'::text))),
    CONSTRAINT evidence_media_processing_jobs_lease_recoveries_check CHECK ((lease_recoveries >= 0)),
    CONSTRAINT evidence_media_processing_jobs_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'running'::text, 'succeeded'::text, 'rejected'::text, 'failed'::text])))
);
ALTER TABLE ONLY evidence_media_processing_jobs
    ADD CONSTRAINT evidence_media_processing_jobs_pkey PRIMARY KEY (evidence_id);
CREATE INDEX evidence_media_processing_queue_idx ON evidence_media_processing_jobs USING btree (available_at, evidence_id) WHERE (status = ANY (ARRAY['queued'::text, 'failed'::text]));
CREATE INDEX evidence_media_processing_running_lease_idx ON evidence_media_processing_jobs USING btree (locked_at, evidence_id) WHERE (status = 'running'::text);

-- profile_media_uploads

CREATE TABLE profile_media_uploads (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    object_key text NOT NULL,
    media_type text NOT NULL,
    byte_size bigint NOT NULL,
    checksum_sha256 bytea NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    upload_expires_at timestamp with time zone NOT NULL,
    completed_at timestamp with time zone,
    attached_at timestamp with time zone,
    provider_etag text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT profile_media_uploads_byte_size_check CHECK (((byte_size > 0) AND (byte_size <= 10485760))),
    CONSTRAINT profile_media_uploads_check CHECK (((status <> ALL (ARRAY['completed'::text, 'attached'::text])) OR (completed_at IS NOT NULL))),
    CONSTRAINT profile_media_uploads_check1 CHECK (((status <> 'attached'::text) OR (attached_at IS NOT NULL))),
    CONSTRAINT profile_media_uploads_checksum_sha256_check CHECK ((octet_length(checksum_sha256) = 32)),
    CONSTRAINT profile_media_uploads_media_type_check CHECK ((media_type = ANY (ARRAY['image/jpeg'::text, 'image/png'::text, 'image/webp'::text]))),
    CONSTRAINT profile_media_uploads_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'completed'::text, 'attached'::text, 'failed'::text, 'expired'::text])))
);
ALTER TABLE ONLY profile_media_uploads
    ADD CONSTRAINT profile_media_uploads_object_key_key UNIQUE (object_key);
ALTER TABLE ONLY profile_media_uploads
    ADD CONSTRAINT profile_media_uploads_pkey PRIMARY KEY (id);
CREATE INDEX profile_media_uploads_pending_expiry_idx ON profile_media_uploads USING btree (upload_expires_at, id) WHERE (status = 'pending'::text);
CREATE INDEX profile_media_uploads_user_created_idx ON profile_media_uploads USING btree (user_id, created_at DESC, id);

-- result_submissions

CREATE TABLE result_submissions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    match_id uuid NOT NULL,
    submitted_by uuid NOT NULL,
    home_score integer NOT NULL,
    away_score integer NOT NULL,
    game_results jsonb DEFAULT '[]'::jsonb NOT NULL,
    evidence_objects jsonb DEFAULT '[]'::jsonb NOT NULL,
    status text DEFAULT 'pending_confirmation'::text NOT NULL,
    supersedes_id uuid,
    submitted_at timestamp with time zone DEFAULT now() NOT NULL,
    decided_at timestamp with time zone,
    decided_by uuid,
    match_version integer DEFAULT 1 NOT NULL,
    tiebreak_type text,
    home_tiebreak_score integer,
    away_tiebreak_score integer,
    origin text DEFAULT 'legacy'::text NOT NULL,
    CONSTRAINT result_submissions_away_score_check CHECK ((away_score >= 0)),
    CONSTRAINT result_submissions_canonical_status_chk CHECK ((status <> ALL (ARRAY['pending_confirmation'::text, 'disputed'::text]))),
    CONSTRAINT result_submissions_games_array_chk CHECK (((jsonb_typeof(game_results) = 'array'::text) AND ((jsonb_array_length(game_results) >= 1) AND (jsonb_array_length(game_results) <= 99)))),
    CONSTRAINT result_submissions_home_score_check CHECK ((home_score >= 0)),
    CONSTRAINT result_submissions_match_version_positive_chk CHECK ((match_version > 0)),
    CONSTRAINT result_submissions_not_self_superseding_chk CHECK (((supersedes_id IS NULL) OR (supersedes_id <> id))),
    CONSTRAINT result_submissions_origin_chk CHECK ((origin = ANY (ARRAY['legacy'::text, 'agreed_reports'::text, 'platform_review'::text]))),
    CONSTRAINT result_submissions_score_upper_bound_chk CHECK (((home_score <= 99) AND (away_score <= 99))),
    CONSTRAINT result_submissions_status_check CHECK ((status = ANY (ARRAY['pending_confirmation'::text, 'confirmed'::text, 'disputed'::text, 'rejected'::text, 'superseded'::text]))),
    CONSTRAINT result_submissions_tiebreak_chk CHECK ((((tiebreak_type IS NULL) AND (home_tiebreak_score IS NULL) AND (away_tiebreak_score IS NULL)) OR ((tiebreak_type = 'penalties'::text) AND (home_score = away_score) AND (home_tiebreak_score IS NOT NULL) AND (away_tiebreak_score IS NOT NULL) AND ((home_tiebreak_score >= 0) AND (home_tiebreak_score <= 99)) AND ((away_tiebreak_score >= 0) AND (away_tiebreak_score <= 99)) AND (home_tiebreak_score <> away_tiebreak_score))))
);
ALTER TABLE ONLY result_submissions
    ADD CONSTRAINT result_submissions_pkey PRIMARY KEY (id);
CREATE UNIQUE INDEX result_submissions_id_match_uidx ON result_submissions USING btree (id, match_id);
CREATE INDEX result_submissions_match_idx ON result_submissions USING btree (match_id, submitted_at DESC);
CREATE UNIQUE INDEX result_submissions_one_confirmed_per_match_uidx ON result_submissions USING btree (match_id) WHERE (status = 'confirmed'::text);
CREATE UNIQUE INDEX result_submissions_one_pending_per_match_uidx ON result_submissions USING btree (match_id) WHERE (status = 'pending_confirmation'::text);

-- match_result_verifications

CREATE TABLE match_result_verifications (
    match_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    phase text NOT NULL,
    first_report_entry_id uuid NOT NULL,
    first_reported_at timestamp with time zone NOT NULL,
    report_window_seconds integer NOT NULL,
    reminder_lead_seconds integer NOT NULL,
    response_window_seconds integer NOT NULL,
    report_deadline_at timestamp with time zone NOT NULL,
    reminder_at timestamp with time zone NOT NULL,
    reminder_sent_at timestamp with time zone,
    mismatch_at timestamp with time zone,
    response_deadline_at timestamp with time zone,
    resolution text,
    resolved_at timestamp with time zone,
    canonical_submission_id uuid,
    version integer DEFAULT 1 NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_result_verifications_mismatch_pair_chk CHECK ((((mismatch_at IS NULL) = (response_deadline_at IS NULL)) AND ((response_deadline_at IS NULL) OR (response_deadline_at > mismatch_at)))),
    CONSTRAINT match_result_verifications_phase_check CHECK ((phase = ANY (ARRAY['awaiting_second_report'::text, 'awaiting_responses'::text, 'in_review'::text, 'resolved'::text]))),
    CONSTRAINT match_result_verifications_phase_chk CHECK ((((phase = 'awaiting_second_report'::text) AND (mismatch_at IS NULL) AND (resolution IS NULL)) OR ((phase = ANY (ARRAY['awaiting_responses'::text, 'in_review'::text])) AND (mismatch_at IS NOT NULL) AND (resolution IS NULL)) OR ((phase = 'resolved'::text) AND (resolution IS NOT NULL)))),
    CONSTRAINT match_result_verifications_reminder_lead_chk CHECK ((reminder_lead_seconds <= (report_window_seconds - 60))),
    CONSTRAINT match_result_verifications_reminder_lead_seconds_check CHECK ((reminder_lead_seconds >= 60)),
    CONSTRAINT match_result_verifications_report_window_chk CHECK (((first_reported_at < reminder_at) AND (reminder_at < report_deadline_at))),
    CONSTRAINT match_result_verifications_report_window_seconds_check CHECK (((report_window_seconds >= 300) AND (report_window_seconds <= 3600))),
    CONSTRAINT match_result_verifications_resolution_check CHECK ((resolution = ANY (ARRAY['agreed'::text, 'report_timeout'::text, 'response_timeout'::text, 'platform_review'::text, 'competition_cancelled'::text]))),
    CONSTRAINT match_result_verifications_resolved_at_chk CHECK (((resolution IS NULL) = (resolved_at IS NULL))),
    CONSTRAINT match_result_verifications_response_window_seconds_check CHECK (((response_window_seconds >= 300) AND (response_window_seconds <= 3600))),
    CONSTRAINT match_result_verifications_version_check CHECK ((version > 0))
);
ALTER TABLE ONLY match_result_verifications
    ADD CONSTRAINT match_result_verifications_pkey PRIMARY KEY (match_id);
CREATE INDEX match_result_verifications_reminder_idx ON match_result_verifications USING btree (reminder_at, match_id) WHERE ((phase = 'awaiting_second_report'::text) AND (reminder_sent_at IS NULL));
CREATE INDEX match_result_verifications_report_deadline_idx ON match_result_verifications USING btree (report_deadline_at, match_id) WHERE (phase = 'awaiting_second_report'::text);
CREATE INDEX match_result_verifications_response_deadline_idx ON match_result_verifications USING btree (response_deadline_at, match_id) WHERE (phase = 'awaiting_responses'::text);

-- match_result_reports

CREATE TABLE match_result_reports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    match_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    reported_by uuid NOT NULL,
    kind text NOT NULL,
    home_score integer NOT NULL,
    away_score integer NOT NULL,
    tiebreak_type text,
    home_tiebreak_score integer,
    away_tiebreak_score integer,
    game_results jsonb NOT NULL,
    match_version integer NOT NULL,
    reported_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_result_reports_away_score_check CHECK (((away_score >= 0) AND (away_score <= 99))),
    CONSTRAINT match_result_reports_games_array_chk CHECK (((jsonb_typeof(game_results) = 'array'::text) AND ((jsonb_array_length(game_results) >= 1) AND (jsonb_array_length(game_results) <= 99)))),
    CONSTRAINT match_result_reports_home_score_check CHECK (((home_score >= 0) AND (home_score <= 99))),
    CONSTRAINT match_result_reports_kind_check CHECK ((kind = ANY (ARRAY['initial'::text, 'final'::text]))),
    CONSTRAINT match_result_reports_match_version_check CHECK ((match_version > 0)),
    CONSTRAINT match_result_reports_tiebreak_chk CHECK ((((tiebreak_type IS NULL) AND (home_tiebreak_score IS NULL) AND (away_tiebreak_score IS NULL)) OR ((tiebreak_type = 'penalties'::text) AND (home_score = away_score) AND (home_tiebreak_score IS NOT NULL) AND (away_tiebreak_score IS NOT NULL) AND ((home_tiebreak_score >= 0) AND (home_tiebreak_score <= 99)) AND ((away_tiebreak_score >= 0) AND (away_tiebreak_score <= 99)) AND (home_tiebreak_score <> away_tiebreak_score))))
);
ALTER TABLE ONLY match_result_reports
    ADD CONSTRAINT match_result_reports_match_id_entry_id_kind_key UNIQUE (match_id, entry_id, kind);
ALTER TABLE ONLY match_result_reports
    ADD CONSTRAINT match_result_reports_pkey PRIMARY KEY (id);

-- match_result_report_evidence

CREATE TABLE match_result_report_evidence (
    report_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    "position" smallint NOT NULL,
    CONSTRAINT match_result_report_evidence_position_check CHECK ((("position" >= 0) AND ("position" <= 2)))
);
ALTER TABLE ONLY match_result_report_evidence
    ADD CONSTRAINT match_result_report_evidence_evidence_id_key UNIQUE (evidence_id);
ALTER TABLE ONLY match_result_report_evidence
    ADD CONSTRAINT match_result_report_evidence_pkey PRIMARY KEY (report_id, evidence_id);
ALTER TABLE ONLY match_result_report_evidence
    ADD CONSTRAINT match_result_report_evidence_report_id_position_key UNIQUE (report_id, "position");

-- match_result_reviews

CREATE TABLE match_result_reviews (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    match_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    status text DEFAULT 'queued'::text NOT NULL,
    reason text NOT NULL,
    queued_at timestamp with time zone DEFAULT now() NOT NULL,
    decision text,
    corrected_home_score integer,
    corrected_away_score integer,
    corrected_tiebreak_type text,
    corrected_home_tiebreak_score integer,
    corrected_away_tiebreak_score integer,
    corrected_game_results jsonb,
    note text DEFAULT ''::text NOT NULL,
    decider_kind text,
    decided_by uuid,
    decider_ref text,
    decided_at timestamp with time zone,
    version integer DEFAULT 1 NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT match_result_reviews_corrected_away_score_check CHECK (((corrected_away_score >= 0) AND (corrected_away_score <= 99))),
    CONSTRAINT match_result_reviews_corrected_chk CHECK ((((decision IS DISTINCT FROM 'corrected_score'::text) AND (corrected_home_score IS NULL) AND (corrected_away_score IS NULL) AND (corrected_game_results IS NULL) AND (corrected_tiebreak_type IS NULL) AND (corrected_home_tiebreak_score IS NULL) AND (corrected_away_tiebreak_score IS NULL)) OR ((decision = 'corrected_score'::text) AND (corrected_home_score IS NOT NULL) AND (corrected_away_score IS NOT NULL) AND (corrected_game_results IS NOT NULL) AND (jsonb_typeof(corrected_game_results) = 'array'::text) AND ((jsonb_array_length(corrected_game_results) >= 1) AND (jsonb_array_length(corrected_game_results) <= 99))))),
    CONSTRAINT match_result_reviews_corrected_home_score_check CHECK (((corrected_home_score >= 0) AND (corrected_home_score <= 99))),
    CONSTRAINT match_result_reviews_corrected_tiebreak_chk CHECK ((((corrected_tiebreak_type IS NULL) AND (corrected_home_tiebreak_score IS NULL) AND (corrected_away_tiebreak_score IS NULL)) OR ((corrected_tiebreak_type = 'penalties'::text) AND (corrected_home_score = corrected_away_score) AND (corrected_home_tiebreak_score IS NOT NULL) AND (corrected_away_tiebreak_score IS NOT NULL) AND ((corrected_home_tiebreak_score >= 0) AND (corrected_home_tiebreak_score <= 99)) AND ((corrected_away_tiebreak_score >= 0) AND (corrected_away_tiebreak_score <= 99)) AND (corrected_home_tiebreak_score <> corrected_away_tiebreak_score)))),
    CONSTRAINT match_result_reviews_decider_chk CHECK (((decider_kind IS NULL) OR ((decider_kind = 'staff'::text) AND (decided_by IS NOT NULL)) OR ((decider_kind = 'system'::text) AND (decided_by IS NULL) AND (decider_ref IS NOT NULL) AND (decision <> 'corrected_score'::text)))),
    CONSTRAINT match_result_reviews_decider_kind_check CHECK ((decider_kind = ANY (ARRAY['staff'::text, 'system'::text]))),
    CONSTRAINT match_result_reviews_decider_ref_check CHECK (((decider_ref IS NULL) OR ((char_length(decider_ref) >= 1) AND (char_length(decider_ref) <= 128)))),
    CONSTRAINT match_result_reviews_decision_check CHECK ((decision = ANY (ARRAY['accept_home'::text, 'accept_away'::text, 'corrected_score'::text, 'remove_both'::text]))),
    CONSTRAINT match_result_reviews_decision_state_chk CHECK ((((status = 'queued'::text) AND (decision IS NULL) AND (decider_kind IS NULL) AND (decided_by IS NULL) AND (decider_ref IS NULL) AND (decided_at IS NULL)) OR ((status = 'decided'::text) AND (decision IS NOT NULL) AND (decider_kind IS NOT NULL) AND (decided_at IS NOT NULL)) OR ((status = 'closed'::text) AND (decision IS NULL) AND (decider_kind IS NULL) AND (decided_by IS NULL) AND (decider_ref IS NULL) AND (decided_at IS NOT NULL)))),
    CONSTRAINT match_result_reviews_note_check CHECK ((char_length(note) <= 2000)),
    CONSTRAINT match_result_reviews_reason_check CHECK ((reason = ANY (ARRAY['reports_differ'::text, 'evidence_unavailable'::text]))),
    CONSTRAINT match_result_reviews_status_check CHECK ((status = ANY (ARRAY['queued'::text, 'decided'::text, 'closed'::text]))),
    CONSTRAINT match_result_reviews_version_check CHECK ((version > 0))
);
ALTER TABLE ONLY match_result_reviews
    ADD CONSTRAINT match_result_reviews_match_id_key UNIQUE (match_id);
ALTER TABLE ONLY match_result_reviews
    ADD CONSTRAINT match_result_reviews_pkey PRIMARY KEY (id);
CREATE INDEX match_result_reviews_competition_idx ON match_result_reviews USING btree (competition_id, queued_at, id);
CREATE INDEX match_result_reviews_decided_idx ON match_result_reviews USING btree (decided_at DESC, id DESC) WHERE (status = 'decided'::text);
CREATE INDEX match_result_reviews_queue_idx ON match_result_reviews USING btree (queued_at, id) WHERE (status = 'queued'::text);

-- player_strikes

CREATE TABLE player_strikes (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    match_id uuid NOT NULL,
    review_id uuid NOT NULL,
    reason_code text NOT NULL,
    note text DEFAULT ''::text NOT NULL,
    created_by_kind text NOT NULL,
    created_by uuid,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    revoked_by uuid,
    revoke_reason text,
    CONSTRAINT player_strikes_created_by_kind_check CHECK ((created_by_kind = 'staff'::text)),
    CONSTRAINT player_strikes_creator_chk CHECK (((created_by_kind = 'staff'::text) = (created_by IS NOT NULL))),
    CONSTRAINT player_strikes_note_check CHECK ((char_length(note) <= 2000)),
    CONSTRAINT player_strikes_reason_code_check CHECK ((reason_code = 'false_result_report'::text)),
    CONSTRAINT player_strikes_revocation_chk CHECK ((((revoked_at IS NULL) AND (revoked_by IS NULL) AND (revoke_reason IS NULL)) OR ((revoked_at IS NOT NULL) AND (revoked_by IS NOT NULL) AND (revoke_reason IS NOT NULL) AND ((char_length(revoke_reason) >= 10) AND (char_length(revoke_reason) <= 500)))))
);
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_pkey PRIMARY KEY (id);
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_review_id_user_id_key UNIQUE (review_id, user_id);
CREATE INDEX player_strikes_active_user_idx ON player_strikes USING btree (user_id) WHERE (revoked_at IS NULL);
CREATE INDEX player_strikes_feed_idx ON player_strikes USING btree (created_at DESC, id DESC);
CREATE INDEX player_strikes_user_history_idx ON player_strikes USING btree (user_id, created_at DESC, id DESC);

-- game_account_verification_requests

CREATE TABLE game_account_verification_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    game_account_id uuid NOT NULL,
    user_id uuid NOT NULL,
    method text DEFAULT 'manual_evidence'::text NOT NULL,
    status text DEFAULT 'requested'::text NOT NULL,
    player_note text DEFAULT ''::text NOT NULL,
    decision_reason text DEFAULT ''::text NOT NULL,
    publisher_verified boolean DEFAULT false NOT NULL,
    reviewed_by uuid,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    reviewed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT game_account_publisher_verified_chk CHECK (((NOT publisher_verified) OR ((method = 'publisher_api'::text) AND (status = 'approved'::text)))),
    CONSTRAINT game_account_verification_decision_chk CHECK ((((status = ANY (ARRAY['approved'::text, 'rejected'::text])) AND (reviewed_at IS NOT NULL) AND (reviewed_by IS NOT NULL)) OR (status <> ALL (ARRAY['approved'::text, 'rejected'::text])))),
    CONSTRAINT game_account_verification_rejection_reason_chk CHECK (((status <> 'rejected'::text) OR (char_length(decision_reason) > 0))),
    CONSTRAINT game_account_verification_requests_decision_reason_check CHECK ((char_length(decision_reason) <= 1000)),
    CONSTRAINT game_account_verification_requests_method_check CHECK ((method = ANY (ARRAY['manual_evidence'::text, 'publisher_api'::text]))),
    CONSTRAINT game_account_verification_requests_player_note_check CHECK ((char_length(player_note) <= 500)),
    CONSTRAINT game_account_verification_requests_status_check CHECK ((status = ANY (ARRAY['requested'::text, 'under_review'::text, 'approved'::text, 'rejected'::text, 'withdrawn'::text]))),
    CONSTRAINT game_account_verification_review_pair_chk CHECK (((reviewed_at IS NULL) = (reviewed_by IS NULL)))
);
ALTER TABLE ONLY game_account_verification_requests
    ADD CONSTRAINT game_account_verification_requests_pkey PRIMARY KEY (id);
CREATE UNIQUE INDEX game_account_verification_one_active_uidx ON game_account_verification_requests USING btree (game_account_id) WHERE (status = ANY (ARRAY['requested'::text, 'under_review'::text]));
CREATE INDEX game_account_verification_queue_idx ON game_account_verification_requests USING btree (status, requested_at, id);
CREATE INDEX game_account_verification_user_requested_idx ON game_account_verification_requests USING btree (user_id, requested_at DESC, id DESC);

-- game_account_verification_evidence

CREATE TABLE game_account_verification_evidence (
    request_id uuid NOT NULL,
    evidence_id uuid NOT NULL,
    "position" smallint NOT NULL,
    CONSTRAINT game_account_verification_evidence_position_check CHECK (("position" >= 0))
);
ALTER TABLE ONLY game_account_verification_evidence
    ADD CONSTRAINT game_account_verification_evidence_evidence_id_key UNIQUE (evidence_id);
ALTER TABLE ONLY game_account_verification_evidence
    ADD CONSTRAINT game_account_verification_evidence_pkey PRIMARY KEY (request_id, evidence_id);
ALTER TABLE ONLY game_account_verification_evidence
    ADD CONSTRAINT game_account_verification_evidence_request_id_position_key UNIQUE (request_id, "position");

-- Relationships

ALTER TABLE ONLY evidence_media_processing_jobs
    ADD CONSTRAINT evidence_media_processing_jobs_evidence_id_fkey FOREIGN KEY (evidence_id) REFERENCES evidence_uploads(id) ON DELETE CASCADE;
ALTER TABLE ONLY evidence_uploads
    ADD CONSTRAINT evidence_uploads_owner_user_id_fkey FOREIGN KEY (owner_user_id) REFERENCES users(id);
ALTER TABLE ONLY game_account_verification_evidence
    ADD CONSTRAINT game_account_verification_evidence_evidence_id_fkey FOREIGN KEY (evidence_id) REFERENCES evidence_uploads(id);
ALTER TABLE ONLY game_account_verification_evidence
    ADD CONSTRAINT game_account_verification_evidence_request_id_fkey FOREIGN KEY (request_id) REFERENCES game_account_verification_requests(id) ON DELETE CASCADE;
ALTER TABLE ONLY game_account_verification_requests
    ADD CONSTRAINT game_account_verification_owner_fk FOREIGN KEY (game_account_id, user_id) REFERENCES game_accounts(id, user_id);
ALTER TABLE ONLY game_account_verification_requests
    ADD CONSTRAINT game_account_verification_requests_game_account_id_fkey FOREIGN KEY (game_account_id) REFERENCES game_accounts(id) ON DELETE CASCADE;
ALTER TABLE ONLY game_account_verification_requests
    ADD CONSTRAINT game_account_verification_requests_reviewed_by_fkey FOREIGN KEY (reviewed_by) REFERENCES users(id);
ALTER TABLE ONLY game_account_verification_requests
    ADD CONSTRAINT game_account_verification_requests_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY match_result_report_evidence
    ADD CONSTRAINT match_result_report_evidence_evidence_id_fkey FOREIGN KEY (evidence_id) REFERENCES evidence_uploads(id);
ALTER TABLE ONLY match_result_report_evidence
    ADD CONSTRAINT match_result_report_evidence_report_id_fkey FOREIGN KEY (report_id) REFERENCES match_result_reports(id) ON DELETE CASCADE;
ALTER TABLE ONLY match_result_reports
    ADD CONSTRAINT match_result_reports_entry_id_competition_id_fkey FOREIGN KEY (entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY match_result_reports
    ADD CONSTRAINT match_result_reports_entry_id_reported_by_fkey FOREIGN KEY (entry_id, reported_by) REFERENCES entry_members(entry_id, user_id);
ALTER TABLE ONLY match_result_reports
    ADD CONSTRAINT match_result_reports_match_id_competition_id_fkey FOREIGN KEY (match_id, competition_id) REFERENCES matches(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_result_reports
    ADD CONSTRAINT match_result_reports_match_id_fkey FOREIGN KEY (match_id) REFERENCES match_result_verifications(match_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_result_reviews
    ADD CONSTRAINT match_result_reviews_decided_by_fkey FOREIGN KEY (decided_by) REFERENCES users(id);
ALTER TABLE ONLY match_result_reviews
    ADD CONSTRAINT match_result_reviews_match_id_competition_id_fkey FOREIGN KEY (match_id, competition_id) REFERENCES matches(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_result_reviews
    ADD CONSTRAINT match_result_reviews_match_id_fkey FOREIGN KEY (match_id) REFERENCES match_result_verifications(match_id) ON DELETE CASCADE;
ALTER TABLE ONLY match_result_verifications
    ADD CONSTRAINT match_result_verifications_canonical_submission_id_match_i_fkey FOREIGN KEY (canonical_submission_id, match_id) REFERENCES result_submissions(id, match_id);
ALTER TABLE ONLY match_result_verifications
    ADD CONSTRAINT match_result_verifications_first_report_entry_id_competiti_fkey FOREIGN KEY (first_report_entry_id, competition_id) REFERENCES competition_entries(id, competition_id);
ALTER TABLE ONLY match_result_verifications
    ADD CONSTRAINT match_result_verifications_match_id_competition_id_fkey FOREIGN KEY (match_id, competition_id) REFERENCES matches(id, competition_id) ON DELETE CASCADE;
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_created_by_fkey FOREIGN KEY (created_by) REFERENCES users(id);
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_match_id_fkey FOREIGN KEY (match_id) REFERENCES matches(id);
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_review_id_fkey FOREIGN KEY (review_id) REFERENCES match_result_reviews(id);
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_revoked_by_fkey FOREIGN KEY (revoked_by) REFERENCES users(id);
ALTER TABLE ONLY player_strikes
    ADD CONSTRAINT player_strikes_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY profile_media_uploads
    ADD CONSTRAINT profile_media_uploads_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY result_submissions
    ADD CONSTRAINT result_submissions_decided_by_fkey FOREIGN KEY (decided_by) REFERENCES users(id);
ALTER TABLE ONLY result_submissions
    ADD CONSTRAINT result_submissions_match_id_fkey FOREIGN KEY (match_id) REFERENCES matches(id);
ALTER TABLE ONLY result_submissions
    ADD CONSTRAINT result_submissions_submitted_by_fkey FOREIGN KEY (submitted_by) REFERENCES users(id);
ALTER TABLE ONLY result_submissions
    ADD CONSTRAINT result_submissions_supersedes_id_fkey FOREIGN KEY (supersedes_id) REFERENCES result_submissions(id);
ALTER TABLE ONLY result_submissions
    ADD CONSTRAINT result_submissions_supersedes_same_match_fk FOREIGN KEY (supersedes_id, match_id) REFERENCES result_submissions(id, match_id);

COMMIT;
