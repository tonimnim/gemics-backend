BEGIN;

-- A display name is chosen by the player during onboarding and is never
-- derived from the email address. NULL means that step is still pending;
-- until then display_name mirrors the player's handle, so every surface
-- shows the handle. The API reads this column: apply this migration before
-- deploying it.
ALTER TABLE users ADD COLUMN display_name_set_at timestamptz;

-- A name that differs from the email's local part was chosen by the player
-- (or written by account deletion), so that step is already done.
UPDATE users SET display_name_set_at=now()
WHERE email IS NULL OR lower(display_name) NOT IN (
    lower(split_part(email,'@',1)), lower(left(split_part(email,'@',1),64)));

-- Signup used to copy the email's local part. Those names are replaced by the
-- player's handle, or by a neutral placeholder until the player creates one,
-- and the player is asked to choose a display name.
UPDATE users player SET display_name=COALESCE(
        (SELECT profile.handle FROM player_profiles profile WHERE profile.user_id=player.id),'Player'),
    updated_at=now()
WHERE player.display_name_set_at IS NULL;

COMMIT;
