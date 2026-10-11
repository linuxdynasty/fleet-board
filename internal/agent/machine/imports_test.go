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
	// process.Process is only the type, built as a literal with a Pid: its
	// constructor looks the process up through paths that can start a child.
	// The one method called on it is checked below.
	approved := []string{root + "disk.UsageWithContext", root + "load.AvgWithContext", root + "mem.VirtualMemoryWithContext", root + "process.Process"}
	allowedPkgs := []string{root + "disk", root + "load", root + "mem", root + "process"}
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
					t.Errorf("%s imports %s: only disk, load, mem and process are allowed", f, p)
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

// A gopsutil Process has methods that read the process table through ps or
// lsof on macOS. Only MemoryInfoWithContext is called on one (proc_pidinfo
// through purego on macOS, /proc/<pid>/statm on Linux), and only on a value
// built as a literal in this package (ADR 0031).
func TestOnlyMemoryInfoIsCalledOnAProcess(t *testing.T) {
	t.Parallel()
	const procPkg = "github.com/shirou/gopsutil/v4/process"
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		local := ""
		for _, imp := range af.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == procPkg {
				local = path.Base(p)
				if imp.Name != nil {
					local = imp.Name.Name
				}
			}
		}
		if local == "" {
			continue
		}
		isProcessLit := func(e ast.Expr) bool {
			if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
				e = u.X
			}
			lit, ok := e.(*ast.CompositeLit)
			if !ok {
				return false
			}
			sel, ok := lit.Type.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			id, isID := sel.X.(*ast.Ident)
			return isID && id.Name == local && sel.Sel.Name == "Process"
		}
		vars := map[string]bool{}
		ast.Inspect(af, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				for i, rhs := range n.Rhs {
					if id, ok := n.Lhs[i].(*ast.Ident); ok && isProcessLit(rhs) {
						vars[id.Name] = true
					}
				}
			case *ast.CallExpr:
				// process.NewProcess* and every other package function.
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == local {
						t.Errorf("%s calls %s.%s: build the Process as a literal", f, local, sel.Sel.Name)
					}
				}
			}
			return true
		})
		ast.Inspect(af, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && vars[id.Name] {
					calls = append(calls, sel.Sel.Name)
				}
			}
			return true
		})
		if len(vars) == 0 {
			t.Errorf("%s imports the process package but builds no Process literal the check can follow", f)
		}
	}
	for _, c := range calls {
		if c != "MemoryInfoWithContext" {
			t.Errorf("a gopsutil Process method %s is called: only MemoryInfoWithContext is approved", c)
		}
	}
	if len(calls) == 0 {
		t.Fatal("found no call on a Process: the check reads nothing")
	}
}
