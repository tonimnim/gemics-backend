package httpapi

import "testing"

func TestPublicPlayerAvatarReference(t *testing.T) {
	const playerID = "11111111-1111-4111-8111-111111111111"
	if reference := publicPlayerAvatarReference(playerID, false); reference != nil {
		t.Fatalf("player without an avatar received %q", *reference)
	}
	reference := publicPlayerAvatarReference(playerID, true)
	if reference == nil || *reference != "/v1/players/"+playerID+"/avatar" {
		t.Fatalf("unexpected stable avatar reference: %v", reference)
	}
}
