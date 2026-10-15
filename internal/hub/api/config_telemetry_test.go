package api_test

import (
	"bufio"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// configSample is the value of the config_writes series with this op and
// outcome, or -1 when absent.
func configSample(t *testing.T, body, op, outcome string) float64 {
	t.Helper()
	sc := bufio.NewScanner(strings.NewReader(body))
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "fleet_hub_config_writes_total{") &&
			strings.Contains(line, `op="`+op+`"`) &&
			strings.Contains(line, `outcome="`+outcome+`"`) {
			f := strings.Fields(line)
			v, err := strconv.ParseFloat(f[len(f)-1], 64)
			if err != nil {
				return -1
			}
			return v
		}
	}
	return -1
}

// TestConfigWritesAreCountedByOpAndOutcome proves every owner config
// write is counted by op and outcome: a create, its replay, a refused
// validation, a delete and an export.
func TestConfigWritesAreCountedByOpAndOutcome(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"), trusted(false))
	createOwnerAccount(t, e, "req-1", accountSpec())
	createOwnerAccount(t, e, "req-1", accountSpec())
	if _, err := e.client.CreateAccount(t.Context(), connect.NewRequest(&hubv1.CreateAccountRequest{
		RequestId: "", Harness: &typev1.Harness{Name: "opencode"},
		Provider: "anthropic", Spec: accountSpec(),
	})); err == nil {
		t.Fatal("empty request_id accepted")
	}
	seedGitHub(t, e)
	maxActive := uint32(2)
	updateFleetSettings(t, e, "set-1", &hubv1.SettingsSpec{
		Scope:  fleetScope(),
		Values: &typev1.SettingValues{MaxActive: &maxActive},
	}, []string{"spec.values.max_active"}, 0)
	updateIntegration(t, e, "int-1", "github",
		&hubv1.IntegrationSpec{Label: "GitHub"}, []string{"spec.label"}, 0)
	exportConfig(t, e)
	body := e.scrape(t)
	cases := []struct {
		op, outcome string
		want        float64
	}{
		{"account_create", "ok", 1},
		{"account_create", "replayed", 1},
		{"account_create", "refused", 1},
		{"settings_update", "ok", 1},
		{"integration_update", "ok", 1},
		{"export", "ok", 1},
	}
	for _, tc := range cases {
		if got := configSample(t, body, tc.op, tc.outcome); got != tc.want {
			t.Errorf("config_writes{%s %s} = %v, want %v", tc.op, tc.outcome, got, tc.want)
		}
	}
}
