package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// docOnlyShown is how many doc-only names the report prints without -all.
const docOnlyShown = 15

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the command: 0 clean, 1 violations, 2 a usage or input error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("telemetrynames", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root")
	cfgPath := fs.String("config", ".agents/telemetry-docs.json", "name map, relative to -root")
	all := fs.Bool("all", false, "list every spec name with no code, not only the first few")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		_, _ = fmt.Fprintf(stderr, "telemetry-names: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	cfg, err := LoadConfig(filepath.Join(*root, filepath.FromSlash(*cfgPath)))
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "telemetry-names: %v\n", err)
		return 2
	}
	rep, err := Check(*root, cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "telemetry-names: %v\n", err)
		return 2
	}
	var out strings.Builder
	if n := len(rep.DocOnly); n > 0 {
		fmt.Fprintf(&out, "telemetry-names (report only until plan row 17): %d names in the specs have no code yet\n", n)
		shown := rep.DocOnly
		if !*all && len(shown) > docOnlyShown {
			shown = shown[:docOnlyShown]
		}
		for _, d := range shown {
			fmt.Fprintf(&out, "  %s\n", d)
		}
		if len(shown) < n {
			fmt.Fprintf(&out, "  ... and %d more (-all lists them)\n", n-len(shown))
		}
	}
	if len(rep.Violations) == 0 {
		fmt.Fprintf(&out, "telemetry-names: %d names in code, each in the spec %s names for it\n", rep.Names, *cfgPath)
	}
	if _, err := io.WriteString(stdout, out.String()); err != nil {
		_, _ = fmt.Fprintf(stderr, "telemetry-names: write: %v\n", err)
		return 2
	}
	if len(rep.Violations) > 0 {
		var msg strings.Builder
		for _, v := range rep.Violations {
			msg.WriteString(v.String() + "\n")
		}
		fmt.Fprintf(&msg, "telemetry-names: %d problems; add each name to the spec %s names for it, or map its area there\n",
			len(rep.Violations), *cfgPath)
		_, _ = io.WriteString(stderr, msg.String())
		return 1
	}
	return 0
}
