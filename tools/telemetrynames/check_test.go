package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	obsSpec  = "docs/specs/obs.md"
	workSpec = "docs/specs/work.md"
)

// fixtureSpecs is the spec text every case starts from: obs.md owns hub. and
// writer_, work.md owns enroll. and enroll_. enroll.issue_failed is in obs.md
// only, the wrong spec for its area.
var fixtureSpecs = map[string]string{
	obsSpec: "## Logs\n\n| `hub.started` (`mode`) | run is serving |\n" +
		"| `hub.planned_only` | not built yet |\n\n" +
		"Also `enroll.issue_failed`, mentioned here.\n\n" +
		"| `fleet_hub_writer_jobs_total{db, result}` | counter |\n",
	workSpec: "- Logs: `enroll.issued{machine}`.\n- `fleet_hub_enroll_tokens_total{result}`\n",
}

func fixtureConfig() Config {
	return Config{
		Version:        1,
		CodeDirs:       []string{"cmd", "internal"},
		MetricPrefixes: []string{"fleet_hub", "fleet_agent"},
		Logs:           map[string]string{"hub.": obsSpec, "enroll.": workSpec},
		Metrics:        map[string]string{"writer_": obsSpec, "enroll_": workSpec},
	}
}

// writeTree writes files (slash paths relative to root) and makes sure cmd/
// and internal/ exist.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for _, dir := range []string{"cmd", "internal"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// body wraps statements in a file whose first statement is on line 4.
func body(stmts string) string {
	return "package x\n\nfunc f() {\n" + stmts + "\n}\n"
}

func TestCheckFindings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name:  "documented log event passes",
			files: map[string]string{"internal/x/x.go": body(`	log.Info("hub.started", "mode", m)`)},
		},
		{
			name:  "undocumented log event fails naming it",
			files: map[string]string{"internal/x/x.go": body(`	s.log.Info("hub.vanished")`)},
			want:  []string{`internal/x/x.go:4: log event "hub.vanished" is not in docs/specs/obs.md, the spec its area "hub." maps to`},
		},
		{
			name: "non-static log names fail with file and line",
			files: map[string]string{"internal/x/x.go": body("\tlog.Info(name)\n" +
				"\tlog.Warn(fmt.Sprintf(\"hub.%s\", k))")},
			want: []string{
				"internal/x/x.go:4: log event name is not a static string (the message argument of Info)",
				"internal/x/x.go:5: log event name is not a static string (the message argument of Warn)",
			},
		},
		{
			name: "multi-line call is found at the name's line",
			files: map[string]string{"internal/x/x.go": body("\ts.tel.Logger.Error(\n" +
				"\t\t\"hub.vanished\",\n\t\t\"err_kind\", k,\n\t)")},
			want: []string{`internal/x/x.go:5: log event "hub.vanished" is not in docs/specs/obs.md, the spec its area "hub." maps to`},
		},
		{
			name: "Context variants read the second argument",
			files: map[string]string{"internal/x/x.go": body("\tl.tel.Logger.InfoContext(ctx, \"hub.started\")\n" +
				"\tl.tel.Logger.WarnContext(\n\t\tctx,\n\t\t\"hub.ctx_missing\", \"cause\", c)")},
			want: []string{`internal/x/x.go:7: log event "hub.ctx_missing" is not in docs/specs/obs.md, the spec its area "hub." maps to`},
		},
		{
			name: "prefix plus tail metrics are found and checked",
			files: map[string]string{"internal/x/x.go": body("\tm.Int64Counter(prefix+\"_writer_jobs_total\")\n" +
				"\tm.Float64Histogram(t.MetricPrefix()+\"_writer_lost_seconds\",\n\t\tmetric.WithUnit(\"s\"))\n" +
				"\tm.Int64Counter(\"fleet_hub_enroll_tokens_total\")")},
			want: []string{`internal/x/x.go:5: metric "<prefix>_writer_lost_seconds" is not in docs/specs/obs.md, the spec its tail prefix "writer_" maps to`},
		},
		{
			name:  "non-static metric name fails",
			files: map[string]string{"internal/x/x.go": body(`	m.Int64ObservableGauge(name)`)},
			want:  []string{"internal/x/x.go:4: metric name has no static string tail (the name argument of Int64ObservableGauge)"},
		},
		{
			name: "unmapped log area and metric tail fail",
			files: map[string]string{"internal/x/x.go": body("\tlog.Warn(\"snapshot.applied\")\n" +
				"\tm.Int64Counter(prefix+\"_rpc_total\")")},
			want: []string{
				`internal/x/x.go:4: log event "snapshot.applied" has area "snapshot." with no spec in the telemetry docs map`,
				`internal/x/x.go:5: metric "<prefix>_rpc_total" has a tail with no spec in the telemetry docs map`,
			},
		},
		{
			name:  "listed only in another spec fails",
			files: map[string]string{"internal/x/x.go": body(`	f.tel.Logger.ErrorContext(ctx, "enroll.issue_failed")`)},
			want:  []string{`internal/x/x.go:4: log event "enroll.issue_failed" is not in docs/specs/work.md, the spec its area "enroll." maps to; it is only in docs/specs/obs.md`},
		},
		{
			name: "tests, testdata and gen are not scanned",
			files: map[string]string{
				"internal/x/x_test.go":        body(`	log.Info("hub.vanished")`),
				"internal/x/testdata/y.go":    body(`	log.Info("hub.vanished")`),
				"internal/gen/fleet/z.go":     body(`	log.Info("hub.vanished")`),
				"cmd/fleetd/testdata/main.go": body(`	log.Info(name)`),
			},
		},
		{
			name: "package functions are skipped except log/slog",
			files: map[string]string{"cmd/fleetd/main.go": "package main\n\nimport (\n\t\"log/slog\"\n\t\"net/http\"\n)\n\n" +
				"func f() {\n\thttp.Error(w, \"bad\", 500)\n\tslog.Info(\"hub.vanished\")\n\tfmt.Println(err.Error())\n}\n"},
			want: []string{`cmd/fleetd/main.go:10: log event "hub.vanished" is not in docs/specs/obs.md, the spec its area "hub." maps to`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTree(t, root, fixtureSpecs)
			writeTree(t, root, tc.files)
			rep, err := Check(root, fixtureConfig())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			var got []string
			for _, f := range rep.Violations {
				got = append(got, f.String())
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("violations:\n got  %q\n want %q", got, tc.want)
			}
		})
	}
}

func TestCheckDocOnlyIsReportedNotFailed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, fixtureSpecs)
	writeTree(t, root, map[string]string{"internal/x/x.go": body("\tlog.Info(\"hub.started\")\n" +
		"\tm.Int64Counter(prefix+\"_writer_jobs_total\")")})
	rep, err := Check(root, fixtureConfig())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("violations = %v, want none", rep.Violations)
	}
	want := []string{
		"docs/specs/obs.md: hub.planned_only",
		"docs/specs/work.md: enroll.issued",
		"docs/specs/work.md: fleet_hub_enroll_tokens_total",
	}
	if !slices.Equal(rep.DocOnly, want) {
		t.Errorf("DocOnly:\n got  %q\n want %q", rep.DocOnly, want)
	}
}

func TestCheckRefusesBadInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "missing spec", mutate: func(c *Config) { c.Logs["hub."] = "docs/specs/gone.md" }, wantErr: "gone.md"},
		{name: "spec outside docs/specs", mutate: func(c *Config) { c.Logs["hub."] = "internal/x/README.md" }, wantErr: "docs/specs/"},
		{name: "log key without a dot", mutate: func(c *Config) { c.Logs["hub"] = obsSpec }, wantErr: `"hub"`},
		{name: "missing code dir", mutate: func(c *Config) { c.CodeDirs = []string{"nowhere"} }, wantErr: "nowhere"},
		{name: "wrong version", mutate: func(c *Config) { c.Version = 2 }, wantErr: "version"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeTree(t, root, fixtureSpecs)
			writeTree(t, root, map[string]string{"internal/x/x.go": body(`	log.Info("hub.started")`)})
			cfg := fixtureConfig()
			tc.mutate(&cfg)
			_, err := Check(root, cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want one naming %q", err, tc.wantErr)
			}
		})
	}
}
