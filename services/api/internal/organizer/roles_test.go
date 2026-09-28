package organizer

import (
	"slices"
	"strings"
	"testing"
)

func TestRoleMatrixSeparatesAnalystFromEditor(t *testing.T) {
	every := []Permission{
		PermissionOrganizationRead, PermissionOrganizationManage,
		PermissionMemberRead, PermissionMemberManage,
		PermissionCompetitionRead, PermissionCompetitionWrite, PermissionCompetitionPublish,
		PermissionEntryRead, PermissionEntryManage,
	}
	want := map[Role][]Permission{
		RoleOwner: every,
		RoleAdmin: {
			PermissionOrganizationRead, PermissionMemberRead,
			PermissionCompetitionRead, PermissionCompetitionWrite, PermissionCompetitionPublish,
			PermissionEntryRead, PermissionEntryManage,
		},
		RoleAnalyst: {PermissionOrganizationRead, PermissionCompetitionRead, PermissionEntryRead},
	}
	for _, role := range Roles() {
		for _, permission := range every {
			if got, expected := role.Can(permission), slices.Contains(want[role], permission); got != expected {
				t.Errorf("%s.Can(%s) = %v, want %v", role, permission, got, expected)
			}
		}
		if got := role.Permissions(); len(got) != len(want[role]) {
			t.Errorf("%s holds %v, want exactly %v", role, got, want[role])
		}
	}
}

// Organizers never decide match results: disputed results go to the Gamics
// review queue. The retired referee role and its dispute permission must not
// come back through the matrix.
func TestNoRoleDecidesResults(t *testing.T) {
	if _, ok := ParseRole("referee"); ok {
		t.Fatal("the retired referee role was accepted")
	}
	if Role("referee").Can(Permission("dispute.resolve")) {
		t.Fatal("the retired referee role was granted dispute.resolve")
	}
	for _, role := range Roles() {
		for _, permission := range role.Permissions() {
			if strings.HasPrefix(string(permission), "dispute.") || strings.HasPrefix(string(permission), "result") {
				t.Errorf("%s holds result-deciding permission %s", role, permission)
			}
		}
	}
	if want := []Role{RoleOwner, RoleAdmin, RoleAnalyst}; !slices.Equal(Roles(), want) {
		t.Fatalf("Roles() = %v, want %v", Roles(), want)
	}
}

func TestUnknownRoleGrantsNothing(t *testing.T) {
	unknown := Role("superuser")
	if unknown.Valid() {
		t.Fatal("an unknown role reported itself as valid")
	}
	for _, permission := range RoleOwner.Permissions() {
		if unknown.Can(permission) {
			t.Fatalf("unknown role was granted %s", permission)
		}
	}
	if len(unknown.Permissions()) != 0 {
		t.Fatalf("unknown role reported permissions: %v", unknown.Permissions())
	}
}

func TestParseRoleRejectsUncleanInput(t *testing.T) {
	for _, value := range []string{"Owner", " owner", "owner ", "", "OWNER", "root", "referee"} {
		if _, ok := ParseRole(value); ok {
			t.Errorf("ParseRole(%q) was accepted", value)
		}
	}
	for _, role := range Roles() {
		parsed, ok := ParseRole(string(role))
		if !ok || parsed != role {
			t.Errorf("ParseRole(%q) did not round trip", role)
		}
	}
}

// Owner is the escalation path for every other role, so any permission granted
// anywhere must also be held by owner. Without this, a new permission added to
// admin alone would silently lock owners out of their own organization.
func TestOwnerHoldsEveryGrantedPermission(t *testing.T) {
	for _, role := range Roles() {
		for _, permission := range role.Permissions() {
			if !RoleOwner.Can(permission) {
				t.Errorf("%s holds %s but owner does not", role, permission)
			}
		}
	}
}

func TestPermissionsAreSortedAndStable(t *testing.T) {
	first := RoleAdmin.Permissions()
	second := RoleAdmin.Permissions()
	if len(first) != len(second) {
		t.Fatalf("permission count is unstable: %d vs %d", len(first), len(second))
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("permission order is unstable at %d: %s vs %s", index, first[index], second[index])
		}
		if index > 0 && first[index-1] >= first[index] {
			t.Fatalf("permissions are not sorted: %s before %s", first[index-1], first[index])
		}
	}
}
