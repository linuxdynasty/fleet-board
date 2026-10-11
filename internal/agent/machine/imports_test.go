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
// lsof on macOS, and process.NewProcess looks the pid up first. The only use
// approved (ADR 0031) is one value built as a literal, assigned to a variable
// of its own, with one method called on it, MemoryInfoWithContext:
//
//	p := &process.Process{Pid: …}
//	mi, err := p.MemoryInfoWithContext(ctx)
//
// So, in every non-test file that imports the package: the only use of the
// package name is the type `process.Process` in a literal that is the whole
// right-hand side of an assignment to one variable (no inline
// `(&process.Process{}).Cmdline()`, no literal passed to a function); and every
// other use of that variable is the receiver of a MemoryInfoWithContext call
// (not passed, returned, stored or called with another method).
func TestOnlyMemoryInfoIsCalledOnAProcess(t *testing.T) {
	t.Parallel()
	const procPkg = "github.com/shirou/gopsutil/v4/process"
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var calls int
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
		calls += checkProcessUse(t, f, af, local)
	}
	if calls == 0 {
		t.Fatal("found no MemoryInfoWithContext call on a Process: the check reads nothing")
	}
}

// checkProcessUse applies the rule of TestOnlyMemoryInfoIsCalledOnAProcess to
// one file and returns how many approved calls it found.
func checkProcessUse(t *testing.T, f string, af *ast.File, local string) int {
	t.Helper()
	// parent of every node, so a use is judged by where it sits.
	parent := map[ast.Node]ast.Node{}
	var stack []ast.Node
	ast.Inspect(af, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		if len(stack) > 0 {
			parent[n] = stack[len(stack)-1]
		}
		stack = append(stack, n)
		return true
	})

	// The approved literals: the whole right-hand side (behind one &) of an
	// assignment to a single variable.
	approved := map[*ast.CompositeLit]bool{}
	vars := map[string]*ast.Ident{} // variable name -> its declaring identifier
	ast.Inspect(af, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
			return true
		}
		rhs := as.Rhs[0]
		if u, ok := rhs.(*ast.UnaryExpr); ok && u.Op == token.AND {
			rhs = u.X
		}
		lit, ok := rhs.(*ast.CompositeLit)
		id, isID := as.Lhs[0].(*ast.Ident)
		if !ok || !isID {
			return true
		}
		if sel, ok := lit.Type.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok && x.Name == local && sel.Sel.Name == "Process" {
				approved[lit] = true
				vars[id.Name] = id
			}
		}
		return true
	})

	calls := 0
	ast.Inspect(af, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.SelectorExpr:
			// Every use of the package name.
			if x, ok := n.X.(*ast.Ident); ok && x.Name == local {
				lit, inLit := parent[n].(*ast.CompositeLit)
				if n.Sel.Name != "Process" || !inLit || !approved[lit] {
					t.Errorf("%s uses %s.%s outside `v := &%s.Process{…}`: build the Process as a literal assigned to a variable, and call only MemoryInfoWithContext on it", f, local, n.Sel.Name, local)
				}
			}
		case *ast.Ident:
			// Every use of the variable except where it is declared.
			if vars[n.Name] == nil || vars[n.Name] == n {
				return true
			}
			sel, isSel := parent[n].(*ast.SelectorExpr)
			call, isCall := parent[sel].(*ast.CallExpr)
			if !isSel || sel.X != n || sel.Sel.Name != "MemoryInfoWithContext" || !isCall || call.Fun != sel {
				t.Errorf("%s uses the Process variable %s other than as the receiver of a MemoryInfoWithContext call", f, n.Name)
				return true
			}
			calls++
		}
		return true
	})
	return calls
}
