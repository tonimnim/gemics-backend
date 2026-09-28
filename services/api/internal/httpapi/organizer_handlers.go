package httpapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gamics-io/gamics/services/api/internal/organizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// One person can own a handful of organizations legitimately. Beyond that it is
// slug squatting, so the cap is enforced at creation rather than cleaned up
// later.
const maxOwnedOrganizations = 5

var organizationSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type organizationInput struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	CountryCode string `json:"countryCode"`
}

type organizationPatch struct {
	Name        *string `json:"name"`
	CountryCode *string `json:"countryCode"`
}

type organizationSummary struct {
	ID               string                 `json:"id"`
	Name             string                 `json:"name"`
	Slug             string                 `json:"slug"`
	CountryCode      string                 `json:"countryCode"`
	Status           string                 `json:"status"`
	Role             organizer.Role         `json:"role"`
	Permissions      []organizer.Permission `json:"permissions"`
	CompetitionCount int                    `json:"competitionCount"`
	CreatedAt        time.Time              `json:"createdAt"`
}

type organizationMemberInput struct {
	Handle string `json:"handle"`
	Role   string `json:"role"`
}

type organizationMemberPatch struct {
	Role string `json:"role"`
}

type organizationMember struct {
	UserID      string         `json:"userId"`
	DisplayName string         `json:"displayName"`
	Handle      *string        `json:"handle"`
	CountryCode string         `json:"countryCode"`
	Role        organizer.Role `json:"role"`
	JoinedAt    time.Time      `json:"joinedAt"`
}

// createOrganization is the bootstrap for every organizer surface: it is the one
// organizer route with no {orgId} to authorize against, so it authorizes on
// account standing instead. The creator becomes the owner in the same
// transaction; an organization without an owner would be unmanageable.
func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	var input organizationInput
	if !decodeJSON(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	if len(name) < 2 || len(name) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_name", "The organization name must be 2 to 80 characters.")
		return
	}
	slug := strings.TrimSpace(strings.ToLower(input.Slug))
	if slug == "" {
		slug = slugify(name)
	}
	if !validOrganizationSlug(slug) {
		writeError(w, http.StatusBadRequest, "invalid_slug",
			"The slug must be 3 to 48 characters of lowercase letters, numbers and single hyphens.")
		return
	}
	countryCode := strings.ToUpper(strings.TrimSpace(input.CountryCode))
	if countryCode == "" {
		countryCode = "KE"
	}
	if !countryPattern.MatchString(countryCode) {
		writeError(w, http.StatusBadRequest, "invalid_country", "Use a two-letter ISO country code.")
		return
	}

	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the organization.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	var accountable bool
	err = tx.QueryRow(r.Context(), `SELECT (terms_accepted_at IS NOT NULL AND privacy_accepted_at IS NOT NULL)
		FROM users WHERE id=$1 AND status='active'`, userID).Scan(&accountable)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !accountable) {
		writeError(w, http.StatusConflict, "onboarding_required",
			"Accept the terms and privacy policy before creating an organization.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to verify your account.")
		return
	}
	var owned int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM organization_members
		WHERE user_id=$1 AND role='owner'`, userID).Scan(&owned); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the organization.")
		return
	}
	if owned >= maxOwnedOrganizations {
		writeError(w, http.StatusConflict, "organization_limit_reached",
			"You already own the maximum number of organizations. Contact Gamics support to raise the limit.")
		return
	}

	var summary organizationSummary
	err = tx.QueryRow(r.Context(), `INSERT INTO organizations(name,slug,country_code)
		VALUES ($1,$2,$3) RETURNING id,name,slug,country_code,status,created_at`, name, slug, countryCode).
		Scan(&summary.ID, &summary.Name, &summary.Slug, &summary.CountryCode, &summary.Status, &summary.CreatedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "slug_taken", "That organization slug is already in use.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to create the organization.")
		return
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO organization_members(organization_id,user_id,role)
		VALUES ($1,$2,'owner')`, summary.ID, userID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to assign the organization owner.")
		return
	}
	if err = appendAudit(r, tx, summary.ID, userID, "organization.created", "organization", summary.ID, nil,
		map[string]any{"name": name, "slug": slug, "countryCode": countryCode}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the organization.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to finish creating the organization.")
		return
	}
	summary.Role = organizer.RoleOwner
	summary.Permissions = organizer.RoleOwner.Permissions()
	writeJSON(w, http.StatusCreated, map[string]any{"data": summary})
}

// listMyOrganizations answers "where am I an organizer, and what may I do
// there", which is what an organizer console needs before it can route anywhere.
func (s *Server) listMyOrganizations(w http.ResponseWriter, r *http.Request) {
	if !s.requireDatabase(w) {
		return
	}
	userID := identityFromContext(r.Context()).UserID
	rows, err := s.db.Writer.Query(r.Context(), `SELECT organization.id,organization.name,organization.slug,
		organization.country_code,organization.status,member.role,organization.created_at,
		(SELECT count(*) FROM competitions WHERE organization_id=organization.id)
		FROM organization_members member
		JOIN organizations organization ON organization.id=member.organization_id
		WHERE member.user_id=$1 ORDER BY organization.created_at DESC,organization.id LIMIT 100`, userID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load your organizations.")
		return
	}
	defer rows.Close()
	items := make([]organizationSummary, 0)
	for rows.Next() {
		var item organizationSummary
		var role string
		if err := rows.Scan(&item.ID, &item.Name, &item.Slug, &item.CountryCode, &item.Status,
			&role, &item.CreatedAt, &item.CompetitionCount); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load your organizations.")
			return
		}
		item.Role = organizer.Role(role)
		item.Permissions = item.Role.Permissions()
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load your organizations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	var summary organizationSummary
	err := s.db.Writer.QueryRow(r.Context(), `SELECT id,name,slug,country_code,status,created_at,
		(SELECT count(*) FROM competitions WHERE organization_id=organizations.id)
		FROM organizations WHERE id=$1`, membership.OrganizationID).
		Scan(&summary.ID, &summary.Name, &summary.Slug, &summary.CountryCode, &summary.Status,
			&summary.CreatedAt, &summary.CompetitionCount)
	if errors.Is(err, pgx.ErrNoRows) {
		writeOrganizationNotFound(w)
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the organization.")
		return
	}
	summary.Role = membership.Role
	summary.Permissions = membership.Role.Permissions()
	writeJSON(w, http.StatusOK, map[string]any{"data": summary})
}

// patchOrganization deliberately cannot change the slug. Public competition
// links and organizer identity are built on it, so a rename would break shared
// links and let one organization impersonate another's retired slug.
func (s *Server) patchOrganization(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	var patch organizationPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	if patch.Name == nil && patch.CountryCode == nil {
		writeError(w, http.StatusBadRequest, "empty_patch", "Provide at least one field to update.")
		return
	}
	var name, countryCode *string
	if patch.Name != nil {
		trimmed := strings.TrimSpace(*patch.Name)
		if len(trimmed) < 2 || len(trimmed) > 80 {
			writeError(w, http.StatusBadRequest, "invalid_name", "The organization name must be 2 to 80 characters.")
			return
		}
		name = &trimmed
	}
	if patch.CountryCode != nil {
		upper := strings.ToUpper(strings.TrimSpace(*patch.CountryCode))
		if !countryPattern.MatchString(upper) {
			writeError(w, http.StatusBadRequest, "invalid_country", "Use a two-letter ISO country code.")
			return
		}
		countryCode = &upper
	}

	userID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the organization.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	var beforeName, beforeCountry string
	if err = tx.QueryRow(r.Context(), `SELECT name,country_code FROM organizations WHERE id=$1 FOR UPDATE`,
		membership.OrganizationID).Scan(&beforeName, &beforeCountry); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the organization.")
		return
	}
	var summary organizationSummary
	if err = tx.QueryRow(r.Context(), `UPDATE organizations
		SET name=COALESCE($2,name),country_code=COALESCE($3,country_code),updated_at=now()
		WHERE id=$1 RETURNING id,name,slug,country_code,status,created_at`,
		membership.OrganizationID, name, countryCode).
		Scan(&summary.ID, &summary.Name, &summary.Slug, &summary.CountryCode, &summary.Status, &summary.CreatedAt); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the organization.")
		return
	}
	if err = appendAudit(r, tx, membership.OrganizationID, userID, "organization.updated", "organization",
		membership.OrganizationID, map[string]any{"name": beforeName, "countryCode": beforeCountry},
		map[string]any{"name": summary.Name, "countryCode": summary.CountryCode}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the change.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the organization.")
		return
	}
	summary.Role = membership.Role
	summary.Permissions = membership.Role.Permissions()
	writeJSON(w, http.StatusOK, map[string]any{"data": summary})
}

func (s *Server) listOrganizationMembers(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	rows, err := s.db.Writer.Query(r.Context(), `SELECT member.user_id,player.display_name,profile.handle,
		player.country_code,member.role,member.created_at
		FROM organization_members member JOIN users player ON player.id=member.user_id
		LEFT JOIN player_profiles profile ON profile.user_id=member.user_id
		WHERE member.organization_id=$1 ORDER BY member.created_at,member.user_id LIMIT 200`, membership.OrganizationID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the members.")
		return
	}
	defer rows.Close()
	items := make([]organizationMember, 0)
	for rows.Next() {
		var item organizationMember
		var role string
		if err := rows.Scan(&item.UserID, &item.DisplayName, &item.Handle, &item.CountryCode,
			&role, &item.JoinedAt); err != nil {
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the members.")
			return
		}
		item.Role = organizer.Role(role)
		items = append(items, item)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the members.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items})
}

// addOrganizationMember resolves the invitee by public handle rather than email.
// Accepting an email would turn this route into an account-existence oracle for
// anyone who can create an organization.
func (s *Server) addOrganizationMember(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	var input organizationMemberInput
	if !decodeJSON(w, r, &input) {
		return
	}
	handle := strings.TrimSpace(input.Handle)
	if !handlePattern.MatchString(handle) {
		writeError(w, http.StatusNotFound, "player_not_found", "No player with that handle was found.")
		return
	}
	role, ok := organizer.ParseRole(strings.TrimSpace(input.Role))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_role", "Choose owner, admin or analyst.")
		return
	}

	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to add the member.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	var member organizationMember
	err = tx.QueryRow(r.Context(), `SELECT player.id,player.display_name,profile.handle,player.country_code
		FROM player_profiles profile JOIN users player ON player.id=profile.user_id
		WHERE lower(profile.handle)=lower($1) AND player.status='active'`, handle).
		Scan(&member.UserID, &member.DisplayName, &member.Handle, &member.CountryCode)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "player_not_found", "No player with that handle was found.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to look up that player.")
		return
	}
	var storedRole string
	err = tx.QueryRow(r.Context(), `INSERT INTO organization_members(organization_id,user_id,role)
		VALUES ($1,$2,$3) RETURNING role,created_at`, membership.OrganizationID, member.UserID, string(role)).
		Scan(&storedRole, &member.JoinedAt)
	if isUniqueViolation(err) {
		writeError(w, http.StatusConflict, "already_a_member", "That player is already in this organization.")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to add the member.")
		return
	}
	member.Role = organizer.Role(storedRole)
	if err = appendAudit(r, tx, membership.OrganizationID, actorID, "organization.member_added", "organization_member",
		member.UserID, nil, map[string]any{"role": string(role)}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the change.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the member.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": member})
}

func (s *Server) patchOrganizationMember(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	targetID := strings.TrimSpace(r.PathValue("userId"))
	if !uuidPattern.MatchString(targetID) {
		writeError(w, http.StatusNotFound, "member_not_found", "That member was not found.")
		return
	}
	var patch organizationMemberPatch
	if !decodeJSON(w, r, &patch) {
		return
	}
	role, ok := organizer.ParseRole(strings.TrimSpace(patch.Role))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_role", "Choose owner, admin or analyst.")
		return
	}

	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the member.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	previous, ok := lockMembershipForChange(w, r, tx, membership.OrganizationID, targetID, role)
	if !ok {
		return
	}
	var member organizationMember
	var storedRole string
	if err = tx.QueryRow(r.Context(), `UPDATE organization_members SET role=$3
		WHERE organization_id=$1 AND user_id=$2 RETURNING user_id,role,created_at`,
		membership.OrganizationID, targetID, string(role)).Scan(&member.UserID, &storedRole, &member.JoinedAt); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to update the member.")
		return
	}
	member.Role = organizer.Role(storedRole)
	if err = tx.QueryRow(r.Context(), `SELECT player.display_name,profile.handle,player.country_code
		FROM users player LEFT JOIN player_profiles profile ON profile.user_id=player.id
		WHERE player.id=$1`, targetID).Scan(&member.DisplayName, &member.Handle, &member.CountryCode); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the member.")
		return
	}
	if err = appendAudit(r, tx, membership.OrganizationID, actorID, "organization.member_role_changed",
		"organization_member", targetID, map[string]any{"role": previous},
		map[string]any{"role": string(role)}); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the change.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the member.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": member})
}

func (s *Server) removeOrganizationMember(w http.ResponseWriter, r *http.Request) {
	membership := organizerFromContext(r.Context())
	targetID := strings.TrimSpace(r.PathValue("userId"))
	if !uuidPattern.MatchString(targetID) {
		writeError(w, http.StatusNotFound, "member_not_found", "That member was not found.")
		return
	}
	actorID := identityFromContext(r.Context()).UserID
	tx, err := s.db.Writer.Begin(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to remove the member.")
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	previous, ok := lockMembershipForChange(w, r, tx, membership.OrganizationID, targetID, "")
	if !ok {
		return
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM organization_members
		WHERE organization_id=$1 AND user_id=$2`, membership.OrganizationID, targetID); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to remove the member.")
		return
	}
	if err = appendAudit(r, tx, membership.OrganizationID, actorID, "organization.member_removed",
		"organization_member", targetID, map[string]any{"role": previous}, nil); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to audit the change.")
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to save the change.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// lockMembershipForChange locks every owner row in the organization before a
// role change or removal is allowed, then refuses any change that would leave
// the organization without an owner. Locking the owner set rather than reading a
// count is what makes two concurrent "demote the other owner" requests safe: one
// waits, then sees the other's result.
//
// nextRole is empty for removal. It returns the member's current role.
func lockMembershipForChange(w http.ResponseWriter, r *http.Request, tx pgx.Tx,
	organizationID, targetID string, nextRole organizer.Role) (string, bool) {
	rows, err := tx.Query(r.Context(), `SELECT user_id FROM organization_members
		WHERE organization_id=$1 AND role='owner' ORDER BY user_id FOR UPDATE`, organizationID)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check organization owners.")
		return "", false
	}
	owners := make([]string, 0, 4)
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			rows.Close()
			writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check organization owners.")
			return "", false
		}
		owners = append(owners, owner)
	}
	rows.Close()
	if rows.Err() != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to check organization owners.")
		return "", false
	}

	var current string
	err = tx.QueryRow(r.Context(), `SELECT role FROM organization_members
		WHERE organization_id=$1 AND user_id=$2 FOR UPDATE`, organizationID, targetID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusNotFound, "member_not_found", "That member was not found.")
		return "", false
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "Unable to load the member.")
		return "", false
	}
	if current == string(organizer.RoleOwner) && nextRole != organizer.RoleOwner && len(owners) <= 1 {
		writeError(w, http.StatusConflict, "last_owner",
			"Promote another owner before changing or removing the last owner.")
		return "", false
	}
	return current, true
}

// appendAudit writes the organizer audit trail. Every organizer mutation calls
// it inside its own transaction so the change and its history commit together.
func appendAudit(r *http.Request, tx pgx.Tx, organizationID, actorID, action, subjectType, subjectID string,
	before, after map[string]any) error {
	return appendAuditContext(r.Context(), tx, r.Header.Get("X-Request-ID"), organizationID, actorID, action,
		subjectType, subjectID, before, after)
}

func appendAuditContext(ctx context.Context, tx pgx.Tx, requestID, organizationID, actorID, action,
	subjectType, subjectID string, before, after map[string]any) error {
	return appendAuditActorContext(ctx, tx, requestID, organizationID, &actorID, action, subjectType, subjectID, before, after)
}

// appendAuditActorContext is the one organization audit writer. A nil actor
// records an automated change, such as a deadline worker or a future system
// decider, with actor_user_id NULL.
func appendAuditActorContext(ctx context.Context, tx pgx.Tx, requestID, organizationID string, actorID *string,
	action, subjectType, subjectID string, before, after map[string]any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_events(organization_id,actor_user_id,action,subject_type,subject_id,
		request_id,before_state,after_state) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		organizationID, actorID, action, subjectType, subjectID, requestID, auditState(before), auditState(after))
	return err
}

// auditState keeps an absent state as SQL NULL. A typed nil map would otherwise
// be encoded as the JSON literal null, which reads identically in a dump but
// breaks `before_state IS NULL` queries over the audit trail.
func auditState(value map[string]any) any {
	if value == nil {
		return nil
	}
	return value
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func validOrganizationSlug(slug string) bool {
	return len(slug) >= 3 && len(slug) <= 48 && organizationSlugPattern.MatchString(slug)
}

// slugify derives a URL-safe slug from a display name. It is a convenience for
// clients that do not want to ask the organizer for one; the result is still
// validated, so a name that produces nothing usable is rejected rather than
// silently turned into an empty slug.
func slugify(value string) string {
	var builder strings.Builder
	previousHyphen := false
	for _, character := range strings.ToLower(value) {
		switch {
		case character >= 'a' && character <= 'z', character >= '0' && character <= '9':
			builder.WriteRune(character)
			previousHyphen = false
		default:
			if !previousHyphen && builder.Len() > 0 {
				builder.WriteByte('-')
				previousHyphen = true
			}
		}
	}
	slug := strings.Trim(builder.String(), "-")
	if len(slug) > 48 {
		slug = strings.Trim(slug[:48], "-")
	}
	return slug
}
