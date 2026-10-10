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
// code outside it could get or change a Line without calling a New…
// constructor (plan row 7, #140 and the owner's note on it):
//   - an exported package-level function not named New…;
//   - a New… function that returns a Line but takes no Purpose;
//   - an exported var or const whose type mentions Line, or whose type is
//     left to the compiler (it could be a Line);
//   - an exported method that returns a Line, or a type that holds one;
//   - an exported method with a pointer receiver on a type that holds a
//     Line (it could rewrite the Line in place);
//   - an exported function or method that takes a Line by reference (a
//     pointer, slice, map, channel or function of a type that holds one); a
//     Line passed by value is a copy and is fine;
//   - an exported var, or a result of an exported function or method, whose
//     type may hold a Line: any, an interface, or a type from another
//     package. error is the one exception, and only while Line has no
//     Error method.
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
	c := checker{decls: decls, holds: holds}

	var out []string
	for _, f := range files {
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				out = append(out, c.funcViolations(d)...)
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
						case vs.Type != nil && c.open(vs.Type):
							out = append(out, "exported "+d.Tok.String()+" "+n.Name+" has a type that may hold a Line")
						}
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

type checker struct {
	decls map[string]ast.Expr // the package's type declarations
	holds map[string]bool     // Line and the package's types that hold one
}

func (c checker) funcViolations(d *ast.FuncDecl) []string {
	if d.Recv != nil && d.Name.Name == "Error" && recvName(d) == "Line" {
		return []string{"Line has an Error method, so an error value can carry one"}
	}
	if !d.Name.IsExported() {
		return nil
	}
	kind := "function"
	if d.Recv != nil {
		kind = "method"
	}
	returnsLine := d.Type.Results != nil && mentions(d.Type.Results, c.holds)
	switch {
	case d.Recv != nil && returnsLine:
		return []string{"exported method " + d.Name.Name + " returns a Line"}
	case d.Recv != nil && isPointer(d.Recv.List[0].Type) && c.holds[recvName(d)]:
		return []string{"exported method " + d.Name.Name + " has a pointer receiver on a Line"}
	case c.byReference(d.Type.Params):
		return []string{"exported " + kind + " " + d.Name.Name + " takes a Line by reference"}
	case c.anyOpen(d.Type.Results):
		return []string{"exported " + kind + " " + d.Name.Name + " returns a type that may hold a Line"}
	case d.Recv != nil:
		return nil
	case !strings.HasPrefix(d.Name.Name, "New"):
		return []string{"exported function " + d.Name.Name + " is not named New…"}
	case returnsLine && !mentions(d.Type.Params, map[string]bool{"Purpose": true}):
		return []string{"constructor " + d.Name.Name + " takes no Purpose"}
	}
	return nil
}

// byReference reports a parameter that names a Line holder other than as
// the plain value type.
func (c checker) byReference(params *ast.FieldList) bool {
	for _, f := range params.List {
		if id, ok := f.Type.(*ast.Ident); ok && c.holds[id.Name] {
			continue
		}
		if mentions(f.Type, c.holds) {
			return true
		}
	}
	return false
}

func (c checker) anyOpen(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, f := range results.List {
		if c.open(f.Type) {
			return true
		}
	}
	return false
}

// open reports a type that could carry a Line the package put in it: any,
// an interface, a type from another package (it may be an interface), or a
// pointer, slice, map or array of one. error is closed: Line has no Error
// method (funcViolations checks that).
func (c checker) open(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		if t.Name == "any" {
			return true
		}
		_, isIface := c.decls[t.Name].(*ast.InterfaceType)
		return isIface
	case *ast.InterfaceType, *ast.SelectorExpr:
		return true
	case *ast.StarExpr:
		return c.open(t.X)
	case *ast.ArrayType:
		return c.open(t.Elt)
	case *ast.MapType:
		return c.open(t.Key) || c.open(t.Value)
	case *ast.ChanType:
		return c.open(t.Value)
	}
	return false
}

func isPointer(expr ast.Expr) bool {
	_, ok := expr.(*ast.StarExpr)
	return ok
}

// recvName is the receiver's base type name, "" for none.
func recvName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return ""
	}
	t := d.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name
	}
	return ""
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
		{"mutator on Line", "package typing\nfunc (l *Line) Retarget(id string) { l.cmd = id }\n", "exported method Retarget has a pointer receiver on a Line"},
		{"mutator on a holder", "package typing\ntype Pair struct{ L Line }\nfunc (p *Pair) Set(s string) {}\n", "exported method Set has a pointer receiver on a Line"},
		{"method takes a Line pointer", "package typing\ntype B struct{}\nfunc (B) Fix(l *Line) {}\n", "exported method Fix takes a Line by reference"},
		{"method takes a Line slice", "package typing\ntype B struct{}\nfunc (B) Fill(dst []Line) {}\n", "exported method Fill takes a Line by reference"},
		{"method takes a Line map", "package typing\ntype B struct{}\nfunc (B) Fill(dst map[string]Line) {}\n", "exported method Fill takes a Line by reference"},
		{"method takes a holder pointer", "package typing\ntype Pair struct{ L Line }\ntype B struct{}\nfunc (B) Fill(p *Pair) {}\n", "exported method Fill takes a Line by reference"},
		{"function takes a Line pointer", "package typing\nfunc NewFix(l *Line, p Purpose) error { return nil }\n", "exported function NewFix takes a Line by reference"},
		{"any var", "package typing\nvar Box any\n", "exported var Box has a type that may hold a Line"},
		{"interface literal var", "package typing\nvar Box interface{ IsZero() bool }\n", "exported var Box has a type that may hold a Line"},
		{"local interface var", "package typing\ntype Holder interface{ IsZero() bool }\nvar H Holder\n", "exported var H has a type that may hold a Line"},
		{"foreign type var", "package typing\nimport \"fmt\"\nvar Box fmt.Stringer\n", "exported var Box has a type that may hold a Line"},
		{"method returns any", "package typing\ntype B struct{}\nfunc (B) Get() any { return nil }\n", "exported method Get returns a type that may hold a Line"},
		{"constructor returns any", "package typing\nfunc NewAny(p Purpose) any { return nil }\n", "exported function NewAny returns a type that may hold a Line"},
		{"Line is an error", "package typing\nfunc (l Line) Error() string { return l.cmd }\n", "Line has an Error method, so an error value can carry one"},
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
