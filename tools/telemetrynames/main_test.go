package main

import (
	"bytes"
	"strings"
	"testing"
)

const fixtureConfigJSON = `{
 "version": 1,
 "note": "test map",
 "code_dirs": ["cmd", "internal"],
 "metric_prefixes": ["fleet_hub", "fleet_agent"],
 "logs": {"hub.": "docs/specs/obs.md", "enroll.": "docs/specs/work.md"},
 "metrics": {"writer_": "docs/specs/obs.md", "enroll_": "docs/specs/work.md"}
}
`

func TestRunExitCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		code       string // the statement in internal/x/x.go
		config     string // "" writes no config file
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name: "clean tree exits 0", code: `	log.Info("hub.started")`, config: fixtureConfigJSON,
			wantCode: 0, wantStdout: "telemetry-names: 1 names in code, each in the spec .agents/telemetry-docs.json names for it",
		},
		{
			name: "doc-only names are reported and still exit 0", code: `	log.Info("hub.started")`, config: fixtureConfigJSON,
			wantCode: 0, wantStdout: "  docs/specs/obs.md: hub.planned_only\n",
		},
		{
			name: "an undocumented name exits 1 naming it", code: `	log.Info("hub.vanished")`, config: fixtureConfigJSON,
			wantCode: 1, wantStderr: `internal/x/x.go:4: log event "hub.vanished" is not in docs/specs/obs.md`,
		},
		{
			name: "a missing config exits 2", code: `	log.Info("hub.started")`,
			wantCode: 2, wantStderr: "telemetry-names: read config:",
		},
		{
			name: "an unknown config key exits 2", code: `	log.Info("hub.started")`,
			config:   strings.Replace(fixtureConfigJSON, `"note"`, `"notes"`, 1),
			wantCode: 2, wantStderr: `unknown field "notes"`,
		},
		{
			name: "an extra argument exits 2", code: `	log.Info("hub.started")`, config: fixtureConfigJSON,
			args: []string{"internal"}, wantCode: 2, wantStderr: `unexpected argument "internal"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTree(t, root, fixtureSpecs)
			writeTree(t, root, map[string]string{"internal/x/x.go": body(tc.code)})
			if tc.config != "" {
				writeTree(t, root, map[string]string{".agents/telemetry-docs.json": tc.config})
			}
			var stdout, stderr bytes.Buffer
			code := run(append([]string{"-root", root}, tc.args...), &stdout, &stderr)
			if code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (stderr %q)", code, tc.wantCode, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tc.wantStdout)
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tc.wantStderr)
			}
		})
	}
}
