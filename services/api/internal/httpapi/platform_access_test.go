package httpapi

import (
	"slices"
	"testing"
)

func TestPlatformRolePermissionsAreSeparated(t *testing.T) {
	every := []platformPermission{
		platformRefundView, platformRefundManage, platformVerificationManage, platformPaymentReviewManage,
		platformResultReviewManage, platformPlayerStrikeRevoke,
	}
	cases := []struct {
		role string
		want []platformPermission
	}{
		{role: "admin", want: every},
		{role: "support", want: []platformPermission{platformRefundView}},
		{role: "reviewer", want: []platformPermission{platformVerificationManage, platformResultReviewManage}},
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
