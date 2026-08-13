BEGIN;

CREATE UNIQUE INDEX IF NOT EXISTS game_accounts_id_user_uidx
    ON game_accounts (id, user_id);

CREATE TABLE payment_intents (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id),
    competition_id uuid NOT NULL REFERENCES competitions(id),
    game_account_id uuid NOT NULL,
    entry_id uuid UNIQUE REFERENCES competition_entries(id),
    entry_display_name text NOT NULL CHECK (char_length(entry_display_name) BETWEEN 2 AND 80),
    provider text NOT NULL DEFAULT 'mpesa' CHECK (provider = 'mpesa'),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    currency char(3) NOT NULL DEFAULT 'KES' CHECK (currency = 'KES'),
    phone_e164 text NOT NULL CHECK (phone_e164 ~ '^254[17][0-9]{8}$'),
    request_ip text NOT NULL,
    idempotency_key text NOT NULL CHECK (char_length(idempotency_key) BETWEEN 8 AND 128),
    request_hash text NOT NULL CHECK (request_hash ~ '^[0-9a-f]{64}$'),
    status text NOT NULL DEFAULT 'initiating'
        CHECK (status IN ('initiating','pending','callback_received','succeeded','failed','review')),
    merchant_request_id text,
    checkout_request_id text UNIQUE,
    provider_receipt text UNIQUE,
    provider_result_code text,
    provider_result_description text,
    provider_transaction_at timestamptz,
    provider_request_started_at timestamptz,
    callback_payload bytea,
    query_attempts integer NOT NULL DEFAULT 0 CHECK (query_attempts >= 0),
    last_query_at timestamptz,
    next_query_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    UNIQUE (user_id, idempotency_key),
    CONSTRAINT payment_intents_game_account_owner_fk
        FOREIGN KEY (game_account_id, user_id) REFERENCES game_accounts (id, user_id),
    CONSTRAINT payment_intents_terminal_time_chk CHECK (
        status NOT IN ('succeeded', 'failed') OR completed_at IS NOT NULL
    )
);
CREATE UNIQUE INDEX payment_intents_one_active_per_user_competition_uidx
    ON payment_intents (user_id, competition_id)
    WHERE status IN ('initiating','pending','callback_received','review','succeeded');
CREATE INDEX payment_intents_user_created_idx ON payment_intents (user_id, created_at DESC);
CREATE INDEX payment_intents_reconciliation_idx ON payment_intents (status, next_query_at, updated_at)
    WHERE status IN ('initiating','pending','callback_received','review');
CREATE INDEX payment_intents_competition_status_idx ON payment_intents (competition_id, status);
CREATE INDEX payment_intents_phone_created_idx ON payment_intents (phone_e164, created_at DESC);
CREATE INDEX payment_intents_ip_created_idx ON payment_intents (request_ip, created_at DESC);
CREATE INDEX payment_intents_succeeded_at_idx ON payment_intents (completed_at DESC)
    WHERE status = 'succeeded';

CREATE TABLE payment_callback_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    checkout_request_id text,
    merchant_request_id text,
    request_id text,
    payload_sha256 text NOT NULL CHECK (payload_sha256 ~ '^[0-9a-f]{64}$'),
    payload bytea NOT NULL,
    received_at timestamptz NOT NULL DEFAULT now(),
    processed_at timestamptz,
    processing_error text,
    UNIQUE (checkout_request_id, payload_sha256)
);
CREATE INDEX payment_callback_events_checkout_idx
    ON payment_callback_events (checkout_request_id, received_at DESC);
CREATE INDEX payment_callback_events_unprocessed_idx
    ON payment_callback_events (received_at, id) WHERE processed_at IS NULL;
CREATE INDEX payment_callback_events_received_idx ON payment_callback_events (received_at, id);

COMMIT;
