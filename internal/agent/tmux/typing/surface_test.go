package typing_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// surfaceViolations reads one package's non-test files and names every way
// code outside it could get a Line without calling a New… constructor
// (plan row 7, #140 and the owner's note on it):
//   - an exported package-level function not named New…;
//   - a New… function that returns a Line but takes no Purpose;
//   - an exported var or const whose type mentions Line, or whose type is
//     left to the compiler (it could be a Line);
//   - an exported method that returns a Line, or a type that holds one.
//
// "Holds a Line" follows the package's own type declarations, so a struct
// with a Line field counts too.
func surfaceViolations(files []*ast.File) []string {
	decls := map[string]ast.Expr{}
	for _, f := range files {
		for _, d := range f.Decls {
			g, ok := d.(*ast.GenDecl)
			if !ok || g.Tok != token.TYPE {
				continue
			}
			for _, s := range g.Specs {
				if ts, ok := s.(*ast.TypeSpec); ok {
					decls[ts.Name.Name] = ts.Type
				}
			}
		}
	}
	holds := map[string]bool{"Line": true}
	for changed := true; changed; {
		changed = false
		for name, expr := range decls {
			if !holds[name] && mentions(expr, holds) {
				holds[name], changed = true, true
			}
		}
	}

	var out []string
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				out = append(out, funcViolations(d, holds)...)
			case *ast.GenDecl:
				if d.Tok != token.VAR && d.Tok != token.CONST {
					continue
				}
				for _, s := range d.Specs {
					vs, ok := s.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for _, n := range vs.Names {
						if !n.IsExported() {
							continue
						}
						switch {
						case vs.Type == nil && d.Tok == token.VAR:
							out = append(out, "exported var "+n.Name+" has no stated type")
						case vs.Type != nil && mentions(vs.Type, holds):
							out = append(out, "exported "+d.Tok.String()+" "+n.Name+" is a Line")
						}
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func funcViolations(d *ast.FuncDecl, holds map[string]bool) []string {
	if !d.Name.IsExported() {
		return nil
	}
	returnsLine := d.Type.Results != nil && mentions(d.Type.Results, holds)
	if d.Recv != nil {
		if returnsLine {
			return []string{"exported method " + d.Name.Name + " returns a Line"}
		}
		return nil
	}
	if !strings.HasPrefix(d.Name.Name, "New") {
		return []string{"exported function " + d.Name.Name + " is not named New…"}
	}
	if returnsLine && !mentions(d.Type.Params, map[string]bool{"Purpose": true}) {
		return []string{"constructor " + d.Name.Name + " takes no Purpose"}
	}
	return nil
}

// mentions reports whether expr names any type in names.
func mentions(expr ast.Node, names map[string]bool) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && names[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

func parseSources(t *testing.T, srcs ...string) []*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(srcs))
	for i, src := range srcs {
		f, err := parser.ParseFile(fset, "f"+string(rune('0'+i))+".go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse seeded source %d: %v", i, err)
		}
		files = append(files, f)
	}
	return files
}

// GREEN: the real package has none of them.
func TestPackageExportsLinesOnlyThroughNewConstructors(t *testing.T) {
	t.Parallel()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var srcs []string
	for _, n := range names {
		if strings.HasSuffix(n, "_test.go") {
			continue
		}
		b, err := os.ReadFile(n) //nolint:gosec // G304: a file of this package, listed by the glob above
		if err != nil {
			t.Fatal(err)
		}
		srcs = append(srcs, string(b))
	}
	if len(srcs) == 0 {
		t.Fatal("no package files found")
	}
	if v := surfaceViolations(parseSources(t, srcs...)); len(v) != 0 {
		t.Fatalf("the typing package leaks a Line:\n%s", strings.Join(v, "\n"))
	}
}

// RED: each seeded leak is named, so the GREEN case above can fail.
func TestSurfaceCheckCatchesEachLeak(t *testing.T) {
	t.Parallel()
	const base = `package typing
type Purpose int
type Line struct{ cmd string }
func NewLiteral(pane string, p Purpose, text string) (Line, error) { return Line{}, nil }
func (l Line) Command() (string, error) { return l.cmd, nil }
func (l Line) IsZero() bool { return l.cmd == "" }
`
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"exported helper", "package typing\nfunc Build() {}\n", "exported function Build is not named New…"},
		{"exported line var", "package typing\nvar Enter Line\n", "exported var Enter is a Line"},
		{"exported pointer var", "package typing\nvar Enter *Line\n", "exported var Enter is a Line"},
		{"exported func var", "package typing\nvar Make func() Line\n", "exported var Make is a Line"},
		{"inferred var", "package typing\nvar Enter = mk()\nfunc mk() Line { return Line{} }\n", "exported var Enter has no stated type"},
		{"line slice", "package typing\nvar All []Line\n", "exported var All is a Line"},
		{"method returns line", "package typing\nfunc (l Line) With(s string) Line { return l }\n", "exported method With returns a Line"},
		{"method returns line and error", "package typing\ntype B struct{}\nfunc (B) Make() (Line, error) { return Line{}, nil }\n", "exported method Make returns a Line"},
		{"method returns a holder", "package typing\ntype Pair struct{ L Line }\ntype B struct{}\nfunc (B) Make() Pair { return Pair{} }\n", "exported method Make returns a Line"},
		{"holder var", "package typing\ntype Pair struct{ L Line }\nvar P Pair\n", "exported var P is a Line"},
		{"constructor without purpose", "package typing\nfunc NewRaw(pane string) Line { return Line{} }\n", "constructor NewRaw takes no Purpose"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if v := surfaceViolations(parseSources(t, base)); len(v) != 0 {
				t.Fatalf("the clean base has violations: %v", v)
			}
			got := surfaceViolations(parseSources(t, base, tc.src))
			if len(got) != 1 || got[0] != tc.want {
				t.Fatalf("violations = %q, want [%q]", got, tc.want)
			}
		})
	}
}
