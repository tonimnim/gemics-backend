BEGIN;

-- A push installation belongs to the sign-in session that registered it.
-- Logout, a remote session revoke and account deletion end that session, and
-- delivery only reaches installations whose session is still live, so a
-- signed-out or lost phone stops receiving the player's pushes. The API reads
-- this column: apply this migration before deploying it.
ALTER TABLE push_tokens
    ADD COLUMN session_id uuid REFERENCES refresh_sessions(id) ON DELETE CASCADE;

-- Installations registered before the link existed cannot be tied to a
-- session, so they are revoked. The app registers its token again on its next
-- launch, which binds it to the session it is signed in with.
UPDATE push_tokens SET revoked_at=now(),updated_at=now()
WHERE session_id IS NULL AND revoked_at IS NULL;

CREATE INDEX push_tokens_session_idx ON push_tokens (session_id);

COMMIT;
