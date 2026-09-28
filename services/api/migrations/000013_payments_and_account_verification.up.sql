BEGIN;

ALTER TABLE competition_entries DROP CONSTRAINT competition_entries_status_check;
ALTER TABLE competition_entries ADD CONSTRAINT competition_entries_status_check
    CHECK (status IN ('registered','checked_in','accepted','withdrawal_pending','withdrawn','disqualified'));

-- A successful collection remains immutable accounting history. Withdrawals
-- and refunds have their own lifecycle so a refund can never make the original
-- M-Pesa receipt disappear from the player's statement.
CREATE UNIQUE INDEX payment_intents_id_user_entry_uidx
    ON payment_intents (id, user_id, entry_id, amount_minor, currency);

-- Review is non-terminal: a delayed, verified callback can still complete it.
-- Older prelaunch code could stamp completed_at when escalating to review.
UPDATE payment_intents SET completed_at=NULL
WHERE status NOT IN ('succeeded','failed') AND completed_at IS NOT NULL;
ALTER TABLE payment_intents DROP CONSTRAINT payment_intents_terminal_time_chk;
ALTER TABLE payment_intents ADD CONSTRAINT payment_intents_terminal_time_chk CHECK (
    (status IN ('succeeded','failed')) = (completed_at IS NOT NULL)
);

CREATE TABLE payment_refunds (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_id uuid NOT NULL REFERENCES payment_intents(id),
    user_id uuid NOT NULL REFERENCES users(id),
    entry_id uuid NOT NULL REFERENCES competition_entries(id),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    currency char(3) NOT NULL DEFAULT 'KES' CHECK (currency = 'KES'),
    reason_code text NOT NULL CHECK (reason_code IN (
        'player_withdrawal','competition_cancelled','duplicate_payment','operations_adjustment'
    )),
    mandatory boolean NOT NULL DEFAULT false,
    player_note text NOT NULL DEFAULT '' CHECK (char_length(player_note) <= 500),
    status text NOT NULL DEFAULT 'requested' CHECK (status IN (
        'requested','approved','processing','manual_review','succeeded','rejected','failed'
    )),
    provider text NOT NULL DEFAULT 'mpesa' CHECK (provider = 'mpesa'),
    provider_request_id text UNIQUE,
    provider_receipt text UNIQUE CHECK (
        provider_receipt IS NULL OR char_length(provider_receipt) BETWEEN 1 AND 128
    ),
    provider_result_description text CHECK (
        provider_result_description IS NULL OR char_length(provider_result_description) <= 1000
    ),
    reviewed_by uuid REFERENCES users(id),
    requested_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz,
    completed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT payment_refunds_payment_entry_owner_fk
        FOREIGN KEY (payment_id, user_id, entry_id, amount_minor, currency)
        REFERENCES payment_intents (id, user_id, entry_id, amount_minor, currency),
    CONSTRAINT payment_refunds_terminal_time_chk CHECK (
        (status IN ('succeeded','rejected','failed')) = (completed_at IS NOT NULL)
    ),
    CONSTRAINT payment_refunds_review_pair_chk CHECK (
        (reviewed_by IS NULL) = (reviewed_at IS NULL)
    ),
    CONSTRAINT payment_refunds_receipt_state_chk CHECK (
        (status = 'succeeded') = (provider_receipt IS NOT NULL)
    ),
    CONSTRAINT payment_refunds_mandatory_not_rejected_chk CHECK (
        NOT mandatory OR status <> 'rejected'
    ),
    CONSTRAINT payment_refunds_cancelled_mandatory_chk CHECK (
        reason_code <> 'competition_cancelled' OR mandatory
    )
);
CREATE UNIQUE INDEX payment_refunds_one_active_per_payment_uidx
    ON payment_refunds (payment_id)
    WHERE status IN ('requested','approved','processing','manual_review','succeeded');
CREATE INDEX payment_refunds_user_requested_idx
    ON payment_refunds (user_id, requested_at DESC, id DESC);
CREATE INDEX payment_refunds_operations_queue_idx
    ON payment_refunds (status, requested_at, id);

-- Platform staff is deliberately separate from organization membership. An
-- organizer must never be able to verify a global player identity simply by
-- owning a tournament.
CREATE TABLE platform_staff_roles (
    user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role IN ('support','reviewer','admin')),
    granted_by uuid REFERENCES users(id),
    granted_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);
CREATE INDEX platform_staff_active_role_idx
    ON platform_staff_roles (role, user_id) WHERE revoked_at IS NULL;

ALTER TABLE game_accounts
    ADD COLUMN verification_method text
        CHECK (verification_method IN ('manual_evidence','publisher_api')),
    ADD COLUMN publisher_verified boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT game_accounts_publisher_verified_chk CHECK (
        NOT publisher_verified OR (verification_status='verified' AND verification_method='publisher_api')
    );

CREATE TABLE game_account_verification_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    game_account_id uuid NOT NULL REFERENCES game_accounts(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    method text NOT NULL DEFAULT 'manual_evidence'
        CHECK (method IN ('manual_evidence','publisher_api')),
    status text NOT NULL DEFAULT 'requested'
        CHECK (status IN ('requested','under_review','approved','rejected','withdrawn')),
    player_note text NOT NULL DEFAULT '' CHECK (char_length(player_note) <= 500),
    decision_reason text NOT NULL DEFAULT '' CHECK (char_length(decision_reason) <= 1000),
    publisher_verified boolean NOT NULL DEFAULT false,
    reviewed_by uuid REFERENCES users(id),
    requested_at timestamptz NOT NULL DEFAULT now(),
    reviewed_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT game_account_verification_owner_fk
        FOREIGN KEY (game_account_id, user_id) REFERENCES game_accounts (id, user_id),
    CONSTRAINT game_account_verification_decision_chk CHECK (
        (status IN ('approved','rejected') AND reviewed_at IS NOT NULL AND reviewed_by IS NOT NULL)
        OR status NOT IN ('approved','rejected')
    ),
    CONSTRAINT game_account_verification_review_pair_chk CHECK (
        (reviewed_at IS NULL) = (reviewed_by IS NULL)
    ),
    CONSTRAINT game_account_verification_rejection_reason_chk CHECK (
        status <> 'rejected' OR char_length(decision_reason) > 0
    ),
    CONSTRAINT game_account_publisher_verified_chk CHECK (
        NOT publisher_verified OR (method='publisher_api' AND status='approved')
    )
);
CREATE UNIQUE INDEX game_account_verification_one_active_uidx
    ON game_account_verification_requests (game_account_id)
    WHERE status IN ('requested','under_review');
CREATE INDEX game_account_verification_user_requested_idx
    ON game_account_verification_requests (user_id, requested_at DESC, id DESC);
CREATE INDEX game_account_verification_queue_idx
    ON game_account_verification_requests (status, requested_at, id);

CREATE TABLE game_account_verification_evidence (
    request_id uuid NOT NULL REFERENCES game_account_verification_requests(id) ON DELETE CASCADE,
    evidence_id uuid NOT NULL UNIQUE REFERENCES evidence_uploads(id),
    position smallint NOT NULL CHECK (position >= 0),
    PRIMARY KEY (request_id, evidence_id),
    UNIQUE (request_id, position)
);

COMMIT;
