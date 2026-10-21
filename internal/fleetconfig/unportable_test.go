package fleetconfig

import (
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

func TestUnportableIDs(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		config *hubv1.FleetConfig
		want   []string
	}{
		{"nil", nil, nil},
		{"empty", &hubv1.FleetConfig{}, nil},
		{
			"portable names only", &hubv1.FleetConfig{
				FormatVersion: 1,
				Accounts:      []*hubv1.AccountConfig{{Spec: &hubv1.AccountSpec{DisplayName: "walk"}}},
			},
			nil,
		},
		{"binding machine", &hubv1.FleetConfig{Accounts: []*hubv1.AccountConfig{{
			Spec: &hubv1.AccountSpec{Bindings: []*hubv1.AccountBindingSpec{
				{Machine: &typev1.MachineId{Value: "machine-1"}},
			}},
		}}}, []string{"config.accounts[0].spec.bindings[0].machine"}},
		{"tracker integration", &hubv1.FleetConfig{Repos: []*hubv1.RepoSpec{{
			TrackerProjects: []*hubv1.TrackerProject{{Integration: &typev1.IntegrationId{Value: "github"}}},
		}}}, []string{"config.repos[0].tracker_projects[0].integration"}},
		{"stamped code host", &hubv1.FleetConfig{Repos: []*hubv1.RepoSpec{{
			Repo: &typev1.RepoRef{Host: "github.com", CodeHost: &typev1.IntegrationId{}},
		}}}, []string{"config.repos[0].repo.code_host"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := UnportableIDs(tc.config)
			if len(got) != len(tc.want) {
				t.Fatalf("UnportableIDs = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("UnportableIDs = %v, want %v", got, tc.want)
				}
			}
		})
	}
}
