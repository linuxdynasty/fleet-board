package machine_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Three gopsutil functions are called and no others. On macOS gopsutil's
// host.PlatformInformation starts `sw_vers`, load.Misc starts `ps` and
// disk.SerialNumber starts `system_profiler`, each outside internal/proc,
// which 01 principle 8 reserves for starting a child (a bounded, logged,
// counted one). ADR 0023 also limits the import to a few subpackages.
//
// The check follows import PATHS: a call is judged by the package its
// qualifier resolves to, so an alias (`import d "…/disk"`) or a second import
// of the same package under another name cannot hide a call, and a dot import
// is refused because it hides the qualifier.
func TestOnlyApprovedGopsutilCalls(t *testing.T) {
	t.Parallel()
	const root = "github.com/shirou/gopsutil/v4/"
	approved := []string{root + "disk.UsageWithContext", root + "load.AvgWithContext", root + "mem.VirtualMemoryWithContext"}
	allowedPkgs := []string{root + "disk", root + "load", root + "mem"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]string{} // local name -> import path
		for _, imp := range af.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			name := path.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			switch {
			case p == "os/exec" || p == "golang.org/x/sys/execabs":
				t.Errorf("%s imports %s: start a child with internal/proc", f, p)
			case strings.HasPrefix(p, "github.com/shirou/gopsutil"):
				if !slices.Contains(allowedPkgs, p) {
					t.Errorf("%s imports %s: only disk, load and mem are allowed", f, p)
				}
				if name == "." || name == "_" {
					t.Errorf("%s imports %s as %q: a dot or blank import hides its calls", f, p, name)
				}
				names[name] = p
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && names[id.Name] != "" {
				found = append(found, names[id.Name]+"."+sel.Sel.Name)
			}
			return true
		})
	}
	for _, c := range found {
		if !slices.Contains(approved, c) {
			t.Errorf("gopsutil call %s is not approved: it may start a child process", c)
		}
	}
	if len(found) == 0 {
		t.Fatal("found no gopsutil call: the check reads nothing")
	}
}
