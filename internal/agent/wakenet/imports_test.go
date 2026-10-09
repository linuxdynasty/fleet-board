package wakenet

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestForbiddenSources enforces ADR 0029. In every file, tests included: no
// exec, no cgo, none of the libraries the ADR rules out. In product files:
// never kern.waketime, and never darwin CLOCK_MONOTONIC (it moves with the
// wall clock).
func TestForbiddenSources(t *testing.T) {
	t.Parallel()
	forbidden := map[string]string{
		"os/exec":                      "no exec (ADR 0029 decision 7)",
		"C":                            "no cgo",
		"golang.org/x/net/route":       "not allowed without another ADR",
		"github.com/godbus/dbus":       "not allowed without another ADR",
		"github.com/ebitengine/purego": "not allowed without another ADR",
	}
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
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for prefix, why := range forbidden {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					t.Errorf("%s imports %q: %s", name, path, why)
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.BasicLit:
				if !strings.HasSuffix(name, "_test.go") && x.Kind == token.STRING && strings.Contains(x.Value, "waketime") {
					t.Errorf("%s: %s reads kern.waketime (ADR 0029: never)", fset.Position(x.Pos()), x.Value)
				}
			case *ast.SelectorExpr:
				if strings.HasSuffix(name, "_darwin.go") && x.Sel.Name == "CLOCK_MONOTONIC" {
					t.Errorf("%s: darwin CLOCK_MONOTONIC (ADR 0029: never)", fset.Position(x.Pos()))
				}
			}
			return true
		})
	}
}
