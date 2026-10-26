package hookreconcile

import (
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

func baseInput() Input {
	return Input{
		Harnesses: []Harness{
			{Name: "claude", LaunchAllowed: true, State: HookState{Present: true, Installed: true, Verified: true, Version: "v3"}},
			{Name: "codex", LaunchAllowed: true, State: HookState{Present: true, Installed: true, Verified: true, Version: "v3"}},
		},
		Checkouts: []Checkout{
			{RepoHost: "github.com", RepoPath: "acme/web", Hooks: map[HP]HookState{}},
		},
		Repos: []Repo{
			{Host: "github.com", Path: "acme/web", Enabled: true, Mode: hubv1.RepoHooksMode_REPO_HOOKS_MODE_GENERIC},
		},
		Expected: map[string]string{"claude": "v3", "codex": "v3"},
		Now:      time.Unix(1_700_000_000, 0),
	}
}

func TestDecide(t *testing.T) {
	t.Parallel()
	up := HookState{Present: true, Installed: true, Verified: true, Version: "v3"}
	cases := map[string]struct {
		mutate func(*Input)
		want   []Want
	}{
		"healthy installs want nothing": {},
		"shapeless harness wants nothing": {
			mutate: func(in *Input) {
				in.Harnesses = append(in.Harnesses, Harness{Name: "opencode", LaunchAllowed: true, State: HookState{}})
			},
		},
		"missing checkout state wants install": {
			mutate: func(in *Input) {
				in.Harnesses[0].State = HookState{}
			},
			want: []Want{{Key: Key{Harness: "claude", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "missing"}},
		},
		"uninstalled wants install": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
			},
			want: []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "not_installed"}},
		},
		"unverified wants verify": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true, Installed: true, Version: "v3"}
			},
			want: []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "unverified"}},
		},
		"behind version wants upgrade": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true, Installed: true, Verified: true, Version: "v2"}
				in.Harnesses[1].State = up
			},
			want: []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "behind"}},
		},
		"eshu mode only reports": {
			mutate: func(in *Input) {
				in.Repos[0].Mode = hubv1.RepoHooksMode_REPO_HOOKS_MODE_ESHU
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
			},
		},
		"auto follows repo ships own": {
			mutate: func(in *Input) {
				in.Repos[0].Mode = hubv1.RepoHooksMode_REPO_HOOKS_MODE_AUTO
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true, RepoShipsOwn: true}
			},
		},
		"auto unknown installs to learn": {
			mutate: func(in *Input) {
				in.Repos[0].Mode = hubv1.RepoHooksMode_REPO_HOOKS_MODE_AUTO
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
			},
			want: []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "not_installed"}},
		},
		"generic installs over repo hooks": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true, RepoShipsOwn: true}
			},
			want: []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "not_installed"}},
		},
		"live goal guards the harness": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
				in.Live = map[string]bool{"codex": true}
			},
		},
		"disabled repo is skipped": {
			mutate: func(in *Input) {
				in.Repos[0].Enabled = false
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
			},
		},
		"unknown repo is skipped": {
			mutate: func(in *Input) {
				in.Repos = nil
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
			},
		},
		"launch disallowed is skipped": {
			mutate: func(in *Input) {
				in.Harnesses[1].LaunchAllowed = false
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
			},
		},
		"recent send throttles": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
				in.ResendAfter = 24 * time.Hour
				in.Sent = map[Key]time.Time{{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}: in.Now.Add(-time.Hour)}
			},
		},
		"stale send is due again": {
			mutate: func(in *Input) {
				in.Checkouts[0].Hooks[HP{Harness: "codex"}] = HookState{Present: true}
				in.ResendAfter = 24 * time.Hour
				in.Sent = map[Key]time.Time{{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}: in.Now.Add(-25 * time.Hour)}
			},
			want: []Want{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "not_installed"}},
		},
		"force bypasses state and throttle": {
			mutate: func(in *Input) {
				in.ResendAfter = 24 * time.Hour
				in.Sent = map[Key]time.Time{{Harness: "claude", RepoHost: "github.com", RepoPath: "acme/web"}: in.Now}
				in.Force = []Forced{{Key: Key{Harness: "claude", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "button"}}
			},
			want: []Want{{Key: Key{Harness: "claude", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "forced:button"}},
		},
		"force never breaks the live guard": {
			mutate: func(in *Input) {
				in.Live = map[string]bool{"codex": true}
				in.Force = []Forced{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "button"}}
			},
		},
		"force never installs on eshu": {
			mutate: func(in *Input) {
				in.Repos[0].Mode = hubv1.RepoHooksMode_REPO_HOOKS_MODE_ESHU
				in.Force = []Forced{{Key: Key{Harness: "codex", RepoHost: "github.com", RepoPath: "acme/web"}, Reason: "button"}}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := baseInput()
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			got := Decide(in)
			if len(got) != len(tc.want) {
				t.Fatalf("Decide = %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Decide = %+v, want %+v", got, tc.want)
				}
			}
		})
	}
}
