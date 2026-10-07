BEGIN;

-- Players and staff: accounts, sessions, email codes, sign-in throttling, legal consent and staff roles.

CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;

-- users

CREATE TABLE users (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text,
    phone_e164 text,
    display_name text NOT NULL,
    country_code character(2) DEFAULT 'KE'::bpchar NOT NULL,
    country_chosen_at timestamp with time zone,
    birth_date date,
    status text DEFAULT 'active'::text NOT NULL,
    password_hash text,
    password_changed_at timestamp with time zone,
    email_verified_at timestamp with time zone,
    registration_ip text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    terms_accepted_at timestamp with time zone,
    privacy_accepted_at timestamp with time zone,
    CONSTRAINT users_password_hash_check CHECK (((password_hash IS NULL) OR (password_hash ~~ '$argon2id$%'::text))),
    CONSTRAINT users_phone_e164_chk CHECK (((phone_e164 IS NULL) OR (phone_e164 ~ '^\+[1-9][0-9]{7,14}$'::text))),
    CONSTRAINT users_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'suspended'::text, 'deleted'::text])))
);
ALTER TABLE ONLY users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);
CREATE INDEX users_active_display_name_trgm_idx ON users USING gin (lower(display_name) gin_trgm_ops) WHERE (status = 'active'::text);
CREATE INDEX users_admin_status_idx ON users USING btree (status, created_at DESC, id);
CREATE UNIQUE INDEX users_email_unique ON users USING btree (lower(email)) WHERE (email IS NOT NULL);
CREATE UNIQUE INDEX users_phone_unique ON users USING btree (phone_e164) WHERE (phone_e164 IS NOT NULL);
CREATE INDEX users_registration_ip_idx ON users USING btree (registration_ip, created_at DESC) WHERE (registration_ip IS NOT NULL);

-- player_profiles

CREATE TABLE player_profiles (
    user_id uuid NOT NULL,
    handle text NOT NULL,
    bio text DEFAULT ''::text NOT NULL,
    avatar_object_key text,
    discoverable boolean DEFAULT false NOT NULL,
    analytics_consent_at timestamp with time zone,
    scouting_consent_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
ALTER TABLE ONLY player_profiles
    ADD CONSTRAINT player_profiles_pkey PRIMARY KEY (user_id);
CREATE INDEX player_profiles_discoverable_handle_trgm_idx ON player_profiles USING gin (lower(handle) gin_trgm_ops) WHERE (discoverable = true);
CREATE UNIQUE INDEX player_profiles_handle_unique ON player_profiles USING btree (lower(handle));

-- refresh_sessions

CREATE TABLE refresh_sessions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    token_hash bytea NOT NULL,
    device_name text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    created_ip text DEFAULT ''::text NOT NULL,
    last_used_ip text DEFAULT ''::text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    last_used_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    previous_token_hash bytea,
    previous_token_valid_until timestamp with time zone,
    CONSTRAINT refresh_sessions_previous_token_pair_chk CHECK (((previous_token_hash IS NULL) = (previous_token_valid_until IS NULL)))
);
ALTER TABLE ONLY refresh_sessions
    ADD CONSTRAINT refresh_sessions_pkey PRIMARY KEY (id);
ALTER TABLE ONLY refresh_sessions
    ADD CONSTRAINT refresh_sessions_token_hash_key UNIQUE (token_hash);
CREATE INDEX refresh_sessions_previous_token_idx ON refresh_sessions USING btree (previous_token_hash) WHERE (previous_token_hash IS NOT NULL);
CREATE INDEX refresh_sessions_revoked_idx ON refresh_sessions USING btree (revoked_at) WHERE (revoked_at IS NOT NULL);
CREATE INDEX refresh_sessions_user_active_idx ON refresh_sessions USING btree (user_id, last_used_at DESC) WHERE (revoked_at IS NULL);

-- email_otp_challenges

CREATE TABLE email_otp_challenges (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    purpose text NOT NULL,
    email text NOT NULL,
    code_hash bytea NOT NULL,
    request_ip text NOT NULL,
    attempts smallint DEFAULT 0 NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    consumed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT email_otp_challenges_attempts_check CHECK ((attempts >= 0)),
    CONSTRAINT email_otp_challenges_purpose_check CHECK ((purpose = ANY (ARRAY['email_verification'::text, 'password_reset'::text])))
);
ALTER TABLE ONLY email_otp_challenges
    ADD CONSTRAINT email_otp_challenges_pkey PRIMARY KEY (id);
CREATE INDEX email_otp_challenges_expiry_idx ON email_otp_challenges USING btree (expires_at);
CREATE INDEX email_otp_challenges_lookup_idx ON email_otp_challenges USING btree (lower(email), created_at DESC);
CREATE INDEX email_otp_challenges_rate_idx ON email_otp_challenges USING btree (request_ip, created_at DESC);
CREATE INDEX email_otp_challenges_user_idx ON email_otp_challenges USING btree (user_id, purpose, created_at DESC);
CREATE UNIQUE INDEX email_otp_one_active_per_email_uidx ON email_otp_challenges USING btree (lower(email)) WHERE (consumed_at IS NULL);

-- login_failures

CREATE TABLE login_failures (
    id bigint NOT NULL,
    konami_key text NOT NULL,
    request_ip text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);
ALTER TABLE login_failures ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME login_failures_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);
ALTER TABLE ONLY login_failures
    ADD CONSTRAINT login_failures_pkey PRIMARY KEY (id);
CREATE INDEX login_failures_created_idx ON login_failures USING btree (created_at);
CREATE INDEX login_failures_ip_idx ON login_failures USING btree (request_ip, created_at DESC);
CREATE INDEX login_failures_key_idx ON login_failures USING btree (konami_key, created_at DESC);

-- legal_documents

CREATE TABLE legal_documents (
    document_type text NOT NULL,
    version text NOT NULL,
    content_url text NOT NULL,
    effective_at timestamp with time zone NOT NULL,
    is_current boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT legal_documents_content_url_check CHECK (((length(content_url) >= 1) AND (length(content_url) <= 1000))),
    CONSTRAINT legal_documents_document_type_check CHECK ((document_type = ANY (ARRAY['terms'::text, 'privacy'::text]))),
    CONSTRAINT legal_documents_version_check CHECK (((length(version) >= 1) AND (length(version) <= 32)))
);
ALTER TABLE ONLY legal_documents
    ADD CONSTRAINT legal_documents_pkey PRIMARY KEY (document_type, version);
CREATE UNIQUE INDEX legal_documents_one_current_uidx ON legal_documents USING btree (document_type) WHERE (is_current = true);

-- legal_acceptances

CREATE TABLE legal_acceptances (
    user_id uuid NOT NULL,
    document_type text NOT NULL,
    version text NOT NULL,
    accepted_at timestamp with time zone DEFAULT now() NOT NULL,
    accepted_ip text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL
);
ALTER TABLE ONLY legal_acceptances
    ADD CONSTRAINT legal_acceptances_pkey PRIMARY KEY (user_id, document_type, version);
CREATE INDEX legal_acceptances_user_latest_idx ON legal_acceptances USING btree (user_id, document_type, accepted_at DESC);

-- account_deletion_requests

CREATE TABLE account_deletion_requests (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    status text DEFAULT 'pending'::text NOT NULL,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    execute_after timestamp with time zone NOT NULL,
    request_ip text DEFAULT ''::text NOT NULL,
    cancelled_at timestamp with time zone,
    completed_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT account_deletion_requests_check CHECK ((execute_after >= requested_at)),
    CONSTRAINT account_deletion_requests_check1 CHECK (((status = 'cancelled'::text) = (cancelled_at IS NOT NULL))),
    CONSTRAINT account_deletion_requests_check2 CHECK (((status = 'completed'::text) = (completed_at IS NOT NULL))),
    CONSTRAINT account_deletion_requests_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'cancelled'::text, 'completed'::text])))
);
ALTER TABLE ONLY account_deletion_requests
    ADD CONSTRAINT account_deletion_requests_pkey PRIMARY KEY (id);
CREATE INDEX account_deletion_requests_execution_idx ON account_deletion_requests USING btree (execute_after, id) WHERE (status = 'pending'::text);
CREATE UNIQUE INDEX account_deletion_requests_one_pending_uidx ON account_deletion_requests USING btree (user_id) WHERE (status = 'pending'::text);

-- platform_staff_roles

CREATE TABLE platform_staff_roles (
    user_id uuid NOT NULL,
    role text NOT NULL,
    granted_by uuid,
    granted_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    CONSTRAINT platform_staff_roles_role_check CHECK ((role = ANY (ARRAY['support'::text, 'reviewer'::text, 'operator'::text, 'admin'::text])))
);
ALTER TABLE ONLY platform_staff_roles
    ADD CONSTRAINT platform_staff_roles_pkey PRIMARY KEY (user_id);
CREATE INDEX platform_staff_active_role_idx ON platform_staff_roles USING btree (role, user_id) WHERE (revoked_at IS NULL);

-- push_tokens

CREATE TABLE push_tokens (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    device_id text NOT NULL,
    expo_push_token text NOT NULL,
    platform text NOT NULL,
    app_version text DEFAULT ''::text NOT NULL,
    user_agent text DEFAULT ''::text NOT NULL,
    last_seen_at timestamp with time zone DEFAULT now() NOT NULL,
    revoked_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    session_id uuid,
    CONSTRAINT push_tokens_app_version_check CHECK ((length(app_version) <= 64)),
    CONSTRAINT push_tokens_device_id_check CHECK (((length(device_id) >= 1) AND (length(device_id) <= 160))),
    CONSTRAINT push_tokens_expo_push_token_check CHECK (((length(expo_push_token) >= 20) AND (length(expo_push_token) <= 512))),
    CONSTRAINT push_tokens_platform_check CHECK ((platform = ANY (ARRAY['android'::text, 'ios'::text])))
);
ALTER TABLE ONLY push_tokens
    ADD CONSTRAINT push_tokens_pkey PRIMARY KEY (id);
ALTER TABLE ONLY push_tokens
    ADD CONSTRAINT push_tokens_user_id_device_id_key UNIQUE (user_id, device_id);
CREATE UNIQUE INDEX push_tokens_active_token_uidx ON push_tokens USING btree (expo_push_token) WHERE (revoked_at IS NULL);
CREATE INDEX push_tokens_session_idx ON push_tokens USING btree (session_id);
CREATE INDEX push_tokens_user_active_idx ON push_tokens USING btree (user_id, last_seen_at DESC) WHERE (revoked_at IS NULL);

-- notification_preferences

CREATE TABLE notification_preferences (
    user_id uuid NOT NULL,
    competition_push boolean DEFAULT true NOT NULL,
    match_push boolean DEFAULT true NOT NULL,
    result_push boolean DEFAULT true NOT NULL,
    competition_email boolean DEFAULT true NOT NULL,
    match_email boolean DEFAULT true NOT NULL,
    marketing_email boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);
ALTER TABLE ONLY notification_preferences
    ADD CONSTRAINT notification_preferences_pkey PRIMARY KEY (user_id);

-- Relationships

ALTER TABLE ONLY account_deletion_requests
    ADD CONSTRAINT account_deletion_requests_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY email_otp_challenges
    ADD CONSTRAINT email_otp_challenges_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY legal_acceptances
    ADD CONSTRAINT legal_acceptances_document_type_version_fkey FOREIGN KEY (document_type, version) REFERENCES legal_documents(document_type, version);
ALTER TABLE ONLY legal_acceptances
    ADD CONSTRAINT legal_acceptances_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY notification_preferences
    ADD CONSTRAINT notification_preferences_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY platform_staff_roles
    ADD CONSTRAINT platform_staff_roles_granted_by_fkey FOREIGN KEY (granted_by) REFERENCES users(id);
ALTER TABLE ONLY platform_staff_roles
    ADD CONSTRAINT platform_staff_roles_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY player_profiles
    ADD CONSTRAINT player_profiles_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY push_tokens
    ADD CONSTRAINT push_tokens_session_id_fkey FOREIGN KEY (session_id) REFERENCES refresh_sessions(id) ON DELETE CASCADE;
ALTER TABLE ONLY push_tokens
    ADD CONSTRAINT push_tokens_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE ONLY refresh_sessions
    ADD CONSTRAINT refresh_sessions_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE;

-- Seed data

INSERT INTO legal_documents (document_type, version, content_url, effective_at, is_current) VALUES
    ('terms', '1.0', 'https://gamics.io/legal/terms', now(), true),
    ('privacy', '1.0', 'https://gamics.io/legal/privacy', now(), true);

COMMIT;
