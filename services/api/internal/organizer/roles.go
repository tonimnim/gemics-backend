// Package organizer models organization membership and the permissions each
// role grants. The matrix lives here, away from HTTP and SQL, so authorization
// can be reasoned about and tested as a pure function. Handlers ask for a
// permission, never for a role name, so adding a role later does not require
// revisiting every call site.
package organizer

import "sort"

type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleAnalyst Role = "analyst"
)

type Permission string

const (
	// PermissionOrganizationRead covers the organization record itself.
	PermissionOrganizationRead Permission = "organization.read"
	// PermissionOrganizationManage covers renaming and settings.
	PermissionOrganizationManage Permission = "organization.manage"
	// PermissionMemberRead lists who belongs to the organization.
	PermissionMemberRead Permission = "member.read"
	// PermissionMemberManage grants and revokes roles, so it is owner-only:
	// anything less would let an admin promote itself to owner.
	PermissionMemberManage Permission = "member.manage"
	// PermissionCompetitionRead covers organizer views of drafts and
	// unpublished competitions that the public API deliberately hides.
	PermissionCompetitionRead Permission = "competition.read"
	// PermissionCompetitionWrite creates and edits competitions.
	PermissionCompetitionWrite Permission = "competition.write"
	// PermissionCompetitionPublish moves a competition through its lifecycle,
	// which is what makes it visible and registrable to players.
	PermissionCompetitionPublish Permission = "competition.publish"
	// PermissionEntryRead lists registrations and their check-in state.
	PermissionEntryRead Permission = "entry.read"
	// PermissionEntryManage seeds, disqualifies and reinstates entries.
	PermissionEntryManage Permission = "entry.manage"
)

// grants is deliberately explicit rather than hierarchical. An analyst is not a
// "weaker admin": it reads without changing anything. No organization role
// decides match results; disputed results go to the Gamics review queue.
var grants = map[Role]map[Permission]struct{}{
	RoleOwner: toSet(
		PermissionOrganizationRead, PermissionOrganizationManage,
		PermissionMemberRead, PermissionMemberManage,
		PermissionCompetitionRead, PermissionCompetitionWrite, PermissionCompetitionPublish,
		PermissionEntryRead, PermissionEntryManage,
	),
	RoleAdmin: toSet(
		PermissionOrganizationRead,
		PermissionMemberRead,
		PermissionCompetitionRead, PermissionCompetitionWrite, PermissionCompetitionPublish,
		PermissionEntryRead, PermissionEntryManage,
	),
	RoleAnalyst: toSet(
		PermissionOrganizationRead,
		PermissionCompetitionRead,
		PermissionEntryRead,
	),
}

// Roles lists every assignable role, ordered from most to least privileged for
// presentation. Membership rows are constrained to exactly this set in SQL.
func Roles() []Role {
	return []Role{RoleOwner, RoleAdmin, RoleAnalyst}
}

// ParseRole accepts only an exact role name. It does not lowercase or trim,
// because an authorization input that needs cleaning is an input worth
// rejecting.
func ParseRole(value string) (Role, bool) {
	role := Role(value)
	if _, ok := grants[role]; !ok {
		return "", false
	}
	return role, true
}

// Valid reports whether the role is one this package knows how to authorize.
func (role Role) Valid() bool {
	_, ok := grants[role]
	return ok
}

// Can is the only authorization question handlers should ask. An unknown role
// grants nothing, so a membership row written by a future migration cannot
// accidentally authorize anything before this matrix is updated.
func (role Role) Can(permission Permission) bool {
	permissions, ok := grants[role]
	if !ok {
		return false
	}
	_, granted := permissions[permission]
	return granted
}

// Permissions returns the sorted permissions a role holds. It exists for
// organizer responses so a console can disable controls it cannot use rather
// than discovering the boundary through 403s.
func (role Role) Permissions() []Permission {
	permissions, ok := grants[role]
	if !ok {
		return []Permission{}
	}
	values := make([]Permission, 0, len(permissions))
	for permission := range permissions {
		values = append(values, permission)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	return values
}

func toSet(permissions ...Permission) map[Permission]struct{} {
	set := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		set[permission] = struct{}{}
	}
	return set
}
