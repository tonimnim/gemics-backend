package httpapi

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// accountWritesPlayer inserts an active player with a profile.
func accountWritesPlayer(t *testing.T, pool *pgxpool.Pool, handle string) string {
	t.Helper()
	var userID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(email,display_name,status) VALUES ($1,$2,'active') RETURNING id`,
		handle+"@gamics.test", handle).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	resultFlowExec(t, pool, `INSERT INTO player_profiles(user_id,handle) VALUES ($1,$2)`, userID, handle)
	return userID
}

func TestIntegrationPlayerSelectsTheirAvatar(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)
	player := accountWritesPlayer(t, pool, "avatar_player")
	var uploadID string
	if err := pool.QueryRow(t.Context(), `INSERT INTO profile_media_uploads
		(user_id,object_key,media_type,byte_size,checksum_sha256,status,upload_expires_at,completed_at)
		VALUES ($1,'avatars/test.webp','image/webp',2048,$2,'completed',now()+interval '1 hour',now()) RETURNING id`,
		player, bytes.Repeat([]byte{7}, 32)).Scan(&uploadID); err != nil {
		t.Fatal(err)
	}
	body := `{"uploadId":"` + uploadID + `"}`
	// Selecting it again, once attached, is allowed too.
	for range 2 {
		selected := competitionVisibilityDecode[struct {
			Data struct {
				UploadID string `json:"uploadId"`
				Status   string `json:"status"`
			} `json:"data"`
		}](t, competitionVisibilityCall(t, server.updateProfileAvatar, http.MethodPatch, "/v1/me/avatar", player, "", body),
			http.StatusOK)
		if selected.Data.UploadID != uploadID || selected.Data.Status != "active" {
			t.Fatalf("selection = %+v", selected.Data)
		}
	}
	var objectKey, status string
	if err := pool.QueryRow(t.Context(), `SELECT profile.avatar_object_key,upload.status FROM player_profiles profile
		JOIN profile_media_uploads upload ON upload.user_id=profile.user_id WHERE profile.user_id=$1`, player).
		Scan(&objectKey, &status); err != nil {
		t.Fatal(err)
	}
	if objectKey != "avatars/test.webp" || status != "attached" {
		t.Fatalf("profile avatar %q with upload %s", objectKey, status)
	}
	if audits := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events WHERE action='profile.avatar_updated'
		AND actor_user_id=$1 AND subject_id=$1::text AND after_state->>'uploadId'=$2`, player, uploadID); audits != 2 {
		t.Fatalf("%d avatar audit events, want 2", audits)
	}
}

func TestIntegrationPlayerDeletesTheirAccount(t *testing.T) {
	pool := openMigratedIntegrationDatabase(t)
	server := competitionVisibilityServer(pool)
	player := accountWritesPlayer(t, pool, "leaving_player")
	requested := competitionVisibilityDecode[accountDeletionView](t, competitionVisibilityCall(t,
		server.requestAccountDeletion, http.MethodPost, "/v1/me/deletion-request", player, "", ""), http.StatusAccepted)
	// The cooling-off period has passed.
	resultFlowExec(t, pool, `UPDATE account_deletion_requests SET requested_at=requested_at-(execute_after-now())-interval '1 minute',
		execute_after=now()-interval '1 minute' WHERE id=$1`, requested.ID)
	recorder := competitionVisibilityCall(t, server.executeAccountDeletion, http.MethodPost, "/v1/me/deletion-request/execute",
		player, "", `{"requestId":"`+requested.ID+`","confirmation":"DELETE"}`)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("deletion status %d: %s", recorder.Code, recorder.Body.String())
	}
	if deleted := resultReportsCount(t, pool, `SELECT count(*) FROM users WHERE id=$1 AND status='deleted'
		AND display_name='Deleted player'`, player); deleted != 1 {
		t.Fatal("the account was not anonymized")
	}
	if audits := resultReportsCount(t, pool, `SELECT count(*) FROM audit_events WHERE action='account.deleted'
		AND actor_user_id=$1 AND subject_id=$1::text`, player); audits != 1 {
		t.Fatalf("%d deletion audit events, want 1", audits)
	}
	if events := resultReportsCount(t, pool, `SELECT count(*) FROM outbox_events WHERE event_type='account.deleted'
		AND aggregate_id=$1::text`, player); events != 1 {
		t.Fatalf("%d deletion outbox events, want 1", events)
	}
}

