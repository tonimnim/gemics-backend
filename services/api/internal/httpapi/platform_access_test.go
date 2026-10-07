package httpapi

import (
	"slices"
	"strings"
	"testing"
)

func TestPlatformRolePermissionsAreSeparated(t *testing.T) {
	every := []platformPermission{
		platformOverviewView, platformRefundView, platformRefundManage, platformVerificationManage,
		platformPaymentReviewManage, platformResultReviewManage, platformPlayerStrikeRevoke,
		platformCompetitionManage, platformStaffManage, platformFinanceView,
	}
	cases := []struct {
		role string
		want []platformPermission
	}{
		{role: "admin", want: every},
		// Every staff role runs competitions; none sees money.
		{role: "support", want: []platformPermission{platformOverviewView, platformCompetitionManage}},
		{role: "reviewer", want: []platformPermission{platformOverviewView, platformCompetitionManage,
			platformVerificationManage, platformResultReviewManage}},
		{role: "operator", want: []platformPermission{platformOverviewView, platformCompetitionManage,
			platformVerificationManage, platformResultReviewManage, platformPlayerStrikeRevoke}},
		// Organization roles and unknown values never reach a platform queue.
		{role: "owner"},
		{role: "referee"},
		{role: ""},
	}
	for _, testCase := range cases {
		for _, permission := range every {
			if got, expected := platformRoleCan(testCase.role, permission), slices.Contains(testCase.want, permission); got != expected {
				t.Errorf("platformRoleCan(%q, %s) = %v, want %v", testCase.role, permission, got, expected)
			}
		}
	}
}

func TestOnlyAdminsGrantStaffRolesOrSeeMoney(t *testing.T) {
	for _, role := range platformRoles {
		for _, permission := range []platformPermission{platformStaffManage, platformFinanceView,
			platformRefundView, platformRefundManage, platformPaymentReviewManage} {
			if got := platformRoleCan(role, permission); got != (role == "admin") {
				t.Errorf("platformRoleCan(%q, %s) = %v", role, permission, got)
			}
		}
	}
	if len(platformRoles) != len(platformRolePermissions) {
		t.Fatalf("platformRoles %v and platformRolePermissions disagree", platformRoles)
	}
	migration := readSourceFile(t, "../../migrations/000001_accounts.up.sql")
	if !strings.Contains(migration, "CHECK ((role = ANY (ARRAY['support'::text, 'reviewer'::text, 'operator'::text, 'admin'::text])))") {
		t.Fatal("platform_staff_roles does not allow exactly the platform roles")
	}
}
