BEGIN;

-- M-Pesa payments, provider callbacks, refunds and exchange rates.

-- fx_rates

-- Units of a currency per US dollar on a day. Payments and refunds stay in the
-- currency they were paid in; finance reports convert them to USD at the rate
-- frozen on each row when it succeeded. An admin can enter a rate by hand
-- (source 'manual'), which a later fetch never overwrites.
CREATE TABLE fx_rates (
    currency character(3) NOT NULL,
    rate_date date NOT NULL,
    units_per_usd numeric(24,10) NOT NULL,
    source text NOT NULL,
    set_by uuid,
    fetched_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT fx_rates_not_usd_chk CHECK ((currency <> 'USD'::bpchar)),
    CONSTRAINT fx_rates_units_per_usd_check CHECK ((units_per_usd > (0)::numeric)),
    CONSTRAINT fx_rates_source_check CHECK (((char_length(source) >= 1) AND (char_length(source) <= 100))),
    CONSTRAINT fx_rates_manual_chk CHECK (((source = 'manual'::text) = (set_by IS NOT NULL)))
);
ALTER TABLE ONLY fx_rates
    ADD CONSTRAINT fx_rates_pkey PRIMARY KEY (currency, rate_date);

-- payment_intents

CREATE TABLE payment_intents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    competition_id uuid NOT NULL,
    game_account_id uuid NOT NULL,
    entry_id uuid,
    entry_display_name text NOT NULL,
    provider text DEFAULT 'mpesa'::text NOT NULL,
    amount_minor bigint NOT NULL,
    currency character(3) DEFAULT 'KES'::bpchar NOT NULL,
    phone_e164 text NOT NULL,
    request_ip text NOT NULL,
    idempotency_key text NOT NULL,
    request_hash text NOT NULL,
    status text DEFAULT 'initiating'::text NOT NULL,
    merchant_request_id text,
    checkout_request_id text,
    provider_receipt text,
    provider_result_code text,
    provider_result_description text,
    provider_transaction_at timestamp with time zone,
    provider_request_started_at timestamp with time zone,
    callback_payload bytea,
    query_attempts integer DEFAULT 0 NOT NULL,
    last_query_at timestamp with time zone,
    next_query_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    completed_at timestamp with time zone,
    amount_usd_minor bigint,
    fx_units_per_usd numeric(24,10),
    fx_rate_date date,
    CONSTRAINT payment_intents_amount_minor_check CHECK ((amount_minor > 0)),
    CONSTRAINT payment_intents_usd_chk CHECK ((((amount_usd_minor IS NULL) = (fx_units_per_usd IS NULL)) AND ((fx_units_per_usd IS NULL) = (fx_rate_date IS NULL)) AND ((amount_usd_minor IS NULL) OR (amount_usd_minor >= 0)) AND ((fx_units_per_usd IS NULL) OR (fx_units_per_usd > (0)::numeric)))),
    CONSTRAINT payment_intents_currency_check CHECK ((currency = 'KES'::bpchar)),
    CONSTRAINT payment_intents_entry_display_name_check CHECK (((char_length(entry_display_name) >= 2) AND (char_length(entry_display_name) <= 80))),
    CONSTRAINT payment_intents_idempotency_key_check CHECK (((char_length(idempotency_key) >= 8) AND (char_length(idempotency_key) <= 128))),
    CONSTRAINT payment_intents_phone_e164_check CHECK ((phone_e164 ~ '^254[17][0-9]{8}$'::text)),
    CONSTRAINT payment_intents_provider_check CHECK ((provider = 'mpesa'::text)),
    CONSTRAINT payment_intents_query_attempts_check CHECK ((query_attempts >= 0)),
    CONSTRAINT payment_intents_request_hash_check CHECK ((request_hash ~ '^[0-9a-f]{64}$'::text)),
    CONSTRAINT payment_intents_status_check CHECK ((status = ANY (ARRAY['initiating'::text, 'pending'::text, 'callback_received'::text, 'succeeded'::text, 'failed'::text, 'review'::text]))),
    CONSTRAINT payment_intents_terminal_time_chk CHECK (((status = ANY (ARRAY['succeeded'::text, 'failed'::text])) = (completed_at IS NOT NULL)))
);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_checkout_request_id_key UNIQUE (checkout_request_id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_entry_id_key UNIQUE (entry_id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_pkey PRIMARY KEY (id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_provider_receipt_key UNIQUE (provider_receipt);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_user_id_idempotency_key_key UNIQUE (user_id, idempotency_key);
CREATE INDEX payment_intents_competition_status_idx ON payment_intents USING btree (competition_id, status);
CREATE UNIQUE INDEX payment_intents_id_user_entry_uidx ON payment_intents USING btree (id, user_id, entry_id, amount_minor, currency);
CREATE INDEX payment_intents_ip_created_idx ON payment_intents USING btree (request_ip, created_at DESC);
CREATE UNIQUE INDEX payment_intents_one_active_per_user_competition_uidx ON payment_intents USING btree (user_id, competition_id) WHERE (status = ANY (ARRAY['initiating'::text, 'pending'::text, 'callback_received'::text, 'review'::text, 'succeeded'::text]));
CREATE INDEX payment_intents_phone_created_idx ON payment_intents USING btree (phone_e164, created_at DESC);
CREATE INDEX payment_intents_reconciliation_idx ON payment_intents USING btree (status, next_query_at, updated_at) WHERE (status = ANY (ARRAY['initiating'::text, 'pending'::text, 'callback_received'::text, 'review'::text]));
CREATE INDEX payment_intents_succeeded_at_idx ON payment_intents USING btree (completed_at DESC) WHERE (status = 'succeeded'::text);
CREATE INDEX payment_intents_user_created_idx ON payment_intents USING btree (user_id, created_at DESC);
CREATE INDEX payment_intents_unconverted_idx ON payment_intents USING btree (completed_at, id) WHERE ((status = 'succeeded'::text) AND (amount_usd_minor IS NULL));

-- payment_callback_events

CREATE TABLE payment_callback_events (
    id bigint NOT NULL,
    checkout_request_id text,
    merchant_request_id text,
    request_id text,
    payload_sha256 text NOT NULL,
    payload bytea NOT NULL,
    received_at timestamp with time zone DEFAULT now() NOT NULL,
    processed_at timestamp with time zone,
    processing_error text,
    CONSTRAINT payment_callback_events_payload_sha256_check CHECK ((payload_sha256 ~ '^[0-9a-f]{64}$'::text))
);
ALTER TABLE payment_callback_events ALTER COLUMN id ADD GENERATED ALWAYS AS IDENTITY (
    SEQUENCE NAME payment_callback_events_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1
);
ALTER TABLE ONLY payment_callback_events
    ADD CONSTRAINT payment_callback_events_checkout_request_id_payload_sha256_key UNIQUE (checkout_request_id, payload_sha256);
ALTER TABLE ONLY payment_callback_events
    ADD CONSTRAINT payment_callback_events_pkey PRIMARY KEY (id);
CREATE INDEX payment_callback_events_checkout_idx ON payment_callback_events USING btree (checkout_request_id, received_at DESC);
CREATE INDEX payment_callback_events_received_idx ON payment_callback_events USING btree (received_at, id);
CREATE INDEX payment_callback_events_unprocessed_idx ON payment_callback_events USING btree (received_at, id) WHERE (processed_at IS NULL);

-- payment_refunds

CREATE TABLE payment_refunds (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    payment_id uuid NOT NULL,
    user_id uuid NOT NULL,
    entry_id uuid NOT NULL,
    amount_minor bigint NOT NULL,
    currency character(3) DEFAULT 'KES'::bpchar NOT NULL,
    reason_code text NOT NULL,
    mandatory boolean DEFAULT false NOT NULL,
    player_note text DEFAULT ''::text NOT NULL,
    status text DEFAULT 'requested'::text NOT NULL,
    provider text DEFAULT 'mpesa'::text NOT NULL,
    provider_request_id text,
    provider_receipt text,
    provider_result_description text,
    reviewed_by uuid,
    requested_at timestamp with time zone DEFAULT now() NOT NULL,
    reviewed_at timestamp with time zone,
    completed_at timestamp with time zone,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    amount_usd_minor bigint,
    fx_units_per_usd numeric(24,10),
    fx_rate_date date,
    CONSTRAINT payment_refunds_amount_minor_check CHECK ((amount_minor > 0)),
    CONSTRAINT payment_refunds_usd_chk CHECK ((((amount_usd_minor IS NULL) = (fx_units_per_usd IS NULL)) AND ((fx_units_per_usd IS NULL) = (fx_rate_date IS NULL)) AND ((amount_usd_minor IS NULL) OR (amount_usd_minor >= 0)) AND ((fx_units_per_usd IS NULL) OR (fx_units_per_usd > (0)::numeric)))),
    CONSTRAINT payment_refunds_cancelled_mandatory_chk CHECK (((reason_code <> 'competition_cancelled'::text) OR mandatory)),
    CONSTRAINT payment_refunds_currency_check CHECK ((currency = 'KES'::bpchar)),
    CONSTRAINT payment_refunds_mandatory_not_rejected_chk CHECK (((NOT mandatory) OR (status <> 'rejected'::text))),
    CONSTRAINT payment_refunds_player_note_check CHECK ((char_length(player_note) <= 500)),
    CONSTRAINT payment_refunds_provider_check CHECK ((provider = 'mpesa'::text)),
    CONSTRAINT payment_refunds_provider_receipt_check CHECK (((provider_receipt IS NULL) OR ((char_length(provider_receipt) >= 1) AND (char_length(provider_receipt) <= 128)))),
    CONSTRAINT payment_refunds_provider_result_description_check CHECK (((provider_result_description IS NULL) OR (char_length(provider_result_description) <= 1000))),
    CONSTRAINT payment_refunds_reason_code_check CHECK ((reason_code = ANY (ARRAY['player_withdrawal'::text, 'competition_cancelled'::text, 'duplicate_payment'::text, 'operations_adjustment'::text]))),
    CONSTRAINT payment_refunds_receipt_state_chk CHECK (((status = 'succeeded'::text) = (provider_receipt IS NOT NULL))),
    CONSTRAINT payment_refunds_review_pair_chk CHECK (((reviewed_by IS NULL) = (reviewed_at IS NULL))),
    CONSTRAINT payment_refunds_status_check CHECK ((status = ANY (ARRAY['requested'::text, 'approved'::text, 'processing'::text, 'manual_review'::text, 'succeeded'::text, 'rejected'::text, 'failed'::text]))),
    CONSTRAINT payment_refunds_terminal_time_chk CHECK (((status = ANY (ARRAY['succeeded'::text, 'rejected'::text, 'failed'::text])) = (completed_at IS NOT NULL)))
);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_pkey PRIMARY KEY (id);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_provider_receipt_key UNIQUE (provider_receipt);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_provider_request_id_key UNIQUE (provider_request_id);
CREATE UNIQUE INDEX payment_refunds_one_active_per_payment_uidx ON payment_refunds USING btree (payment_id) WHERE (status = ANY (ARRAY['requested'::text, 'approved'::text, 'processing'::text, 'manual_review'::text, 'succeeded'::text]));
CREATE INDEX payment_refunds_operations_queue_idx ON payment_refunds USING btree (status, requested_at, id);
CREATE INDEX payment_refunds_user_requested_idx ON payment_refunds USING btree (user_id, requested_at DESC, id DESC);
CREATE INDEX payment_refunds_succeeded_at_idx ON payment_refunds USING btree (completed_at DESC) WHERE (status = 'succeeded'::text);
CREATE INDEX payment_refunds_unconverted_idx ON payment_refunds USING btree (completed_at, id) WHERE ((status = 'succeeded'::text) AND (amount_usd_minor IS NULL));

-- Relationships

ALTER TABLE ONLY fx_rates
    ADD CONSTRAINT fx_rates_currency_fkey FOREIGN KEY (currency) REFERENCES currencies(code);
ALTER TABLE ONLY fx_rates
    ADD CONSTRAINT fx_rates_set_by_fkey FOREIGN KEY (set_by) REFERENCES users(id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_competition_id_fkey FOREIGN KEY (competition_id) REFERENCES competitions(id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_entry_id_fkey FOREIGN KEY (entry_id) REFERENCES competition_entries(id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_game_account_owner_fk FOREIGN KEY (game_account_id, user_id) REFERENCES game_accounts(id, user_id);
ALTER TABLE ONLY payment_intents
    ADD CONSTRAINT payment_intents_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_entry_id_fkey FOREIGN KEY (entry_id) REFERENCES competition_entries(id);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_payment_entry_owner_fk FOREIGN KEY (payment_id, user_id, entry_id, amount_minor, currency) REFERENCES payment_intents(id, user_id, entry_id, amount_minor, currency);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_payment_id_fkey FOREIGN KEY (payment_id) REFERENCES payment_intents(id);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_reviewed_by_fkey FOREIGN KEY (reviewed_by) REFERENCES users(id);
ALTER TABLE ONLY payment_refunds
    ADD CONSTRAINT payment_refunds_user_id_fkey FOREIGN KEY (user_id) REFERENCES users(id);

COMMIT;
