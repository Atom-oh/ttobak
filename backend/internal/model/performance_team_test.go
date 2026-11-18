package model

import "testing"

func TestMapSFDCTeamRole(test *testing.T) {
	cases := []struct {
		role, all, want string
	}{
		{"Account Manager", "Account Manager", RoleAM},
		{"Account Manager", "Migration BDM", ""},
		{"Account Manager", "Migration BDM;Account Manager", RoleAM},
		{"Technical Account Manager", "Technical Account Manager", RoleTAM},
		{"Solutions Architect", "Solutions Architect;Other", RoleSA},
		{"Enterprise Support Manager", "", RoleTAM},
		{"Inside Sales Rep", "Inside Sales Rep", ""},
		{"Business Development Manager", "Other", ""},
		{"", "", ""},
	}
	for _, item := range cases {
		if got := MapSFDCTeamRole(item.role, item.all); got != item.want {
			test.Errorf("MapSFDCTeamRole(%q, %q) = %q, want %q", item.role, item.all, got, item.want)
		}
	}
}

func TestPerformanceTeamKeys(test *testing.T) {
	if got := PerformanceTeamSortKey("001A", "kim.lee"); got != "PERFTEAM#001A#kim.lee" {
		test.Fatalf("sort key %q", got)
	}
	if got := PerformanceTeamExternalID("001A", "kim.lee"); got != "001A:kim.lee" {
		test.Fatalf("external id %q", got)
	}
	account, alias, ok := ParsePerformanceTeamExternalID("001A:kim.lee")
	if !ok || account != "001A" || alias != "kim.lee" {
		test.Fatalf("round trip %q %q %v", account, alias, ok)
	}
	for _, bad := range []string{"001A", "001A:", ":kim", "001A:Kim", "001A:kim@x", "0#1:kim", "001A:kim:lee"} {
		if _, _, ok := ParsePerformanceTeamExternalID(bad); ok {
			test.Errorf("%q must be rejected", bad)
		}
	}
}
