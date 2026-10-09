package wakenet

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// forbiddenImports maps an import path (and everything under it) to the reason.
var forbiddenImports = map[string]string{
	"os/exec":                      "no exec (ADR 0029 decision 7)",
	"C":                            "no cgo",
	"golang.org/x/net/route":       "not allowed without another ADR",
	"github.com/godbus/dbus":       "not allowed without another ADR",
	"github.com/ebitengine/purego": "not allowed without another ADR",
}

// forbiddenCalls are the ways to start a process without os/exec.
var forbiddenCalls = map[string][]string{
	"os":                    {"StartProcess"},
	"syscall":               {"Exec", "ForkExec", "StartProcess"},
	"golang.org/x/sys/unix": {"Exec"},
}

// noDotImport are the packages whose process calls a dot import would hide
// from the selector check.
var noDotImport = []string{"os", "syscall", "golang.org/x/sys/unix"}

// startsProcess reports whether pkg.name starts a process: a forbidden call,
// or the raw system call number of exec, fork, vfork or posix_spawn.
func startsProcess(pkg, name string) bool {
	if slices.Contains(forbiddenCalls[pkg], name) {
		return true
	}
	if pkg != "syscall" && pkg != "golang.org/x/sys/unix" {
		return false
	}
	return strings.HasPrefix(name, "SYS_EXEC") || strings.HasPrefix(name, "SYS_POSIX_SPAWN") ||
		name == "SYS_FORK" || name == "SYS_VFORK"
}

// TestForbiddenSources enforces ADR 0029. In every file, tests included: no
// exec (os/exec, the lower-level calls, the raw exec, fork, vfork and
// posix_spawn call numbers, or a dot import that would hide them), no cgo, none of the libraries the
// ADR rules out, and in any file built for darwin never CLOCK_MONOTONIC (it
// moves with the wall clock). In product files: never kern.waketime.
func TestForbiddenSources(t *testing.T) {
	t.Parallel()
	darwin := build.Default
	darwin.GOOS, darwin.GOARCH, darwin.CgoEnabled = "darwin", "arm64", false
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 10 {
		t.Fatalf("found %d Go files; is the test running in the package folder?", len(files))
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		forDarwin, err := darwin.MatchFile(".", name)
		if err != nil {
			t.Fatalf("build constraints of %s: %v", name, err)
		}
		local := checkImports(t, name, f)
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if !strings.HasSuffix(name, "_test.go") && x.Kind == token.STRING && strings.Contains(x.Value, "waketime") {
					t.Errorf("%s: %s reads kern.waketime (ADR 0029: never)", fset.Position(x.Pos()), x.Value)
				}
			case *ast.SelectorExpr:
				if forDarwin && x.Sel.Name == "CLOCK_MONOTONIC" {
					t.Errorf("%s: darwin CLOCK_MONOTONIC (ADR 0029: never)", fset.Position(x.Pos()))
				}
				if pkg, ok := x.X.(*ast.Ident); ok && startsProcess(local[pkg.Name], x.Sel.Name) {
					t.Errorf("%s: %s.%s starts a process (ADR 0029 decision 7: no exec)",
						fset.Position(x.Pos()), pkg.Name, x.Sel.Name)
				}
			}
			return true
		})
	}
}

// checkImports reports forbidden imports in f and returns its local package
// names mapped to import paths.
func checkImports(t *testing.T, name string, f *ast.File) map[string]string {
	t.Helper()
	local := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		for prefix, why := range forbiddenImports {
			if p == prefix || strings.HasPrefix(p, prefix+"/") {
				t.Errorf("%s imports %q: %s", name, p, why)
			}
		}
		as := path.Base(p)
		if imp.Name != nil {
			as = imp.Name.Name
		}
		if as == "." && slices.Contains(noDotImport, p) {
			t.Errorf("%s dot-imports %q: it would hide process calls from this check", name, p)
		}
		local[as] = p
	}
	return local
}
