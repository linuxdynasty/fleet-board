package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// nameKind tells a log event from a metric.
type nameKind int

const (
	logName nameKind = iota
	metricName
)

// name is one telemetry name found in code. For a metric built as
// `prefix + "_tail"`, tail is true and text is the static tail ("_tail").
type name struct {
	kind nameKind
	text string
	tail bool
	file string // slash path relative to the root
	line int
}

// logArg is the index of the message argument of each slog method.
var logArg = map[string]int{
	"Debug": 0, "Info": 0, "Warn": 0, "Error": 0,
	"DebugContext": 1, "InfoContext": 1, "WarnContext": 1, "ErrorContext": 1,
	"Log": 2, "LogAttrs": 2,
}

// instruments are the metric.Meter methods that create an instrument; the
// name is always their first argument.
var instruments = map[string]bool{}

func init() {
	for _, num := range []string{"Int64", "Float64"} {
		for _, kind := range []string{
			"Counter", "UpDownCounter", "Histogram", "Gauge",
			"ObservableCounter", "ObservableUpDownCounter", "ObservableGauge",
		} {
			instruments[num+kind] = true
		}
	}
}

// skipDir is true for folders that hold no product code.
func skipDir(base string) bool {
	return base == "gen" || base == "testdata" || base == "vendor" || base == "node_modules" ||
		(strings.HasPrefix(base, ".") && base != ".")
}

// scan parses every non-test .go file under root/dir for each dir and returns
// the names found and a finding for each name that is not a static string.
func scan(root string, dirs []string) ([]name, []Finding, error) {
	var names []name
	var bad []Finding
	fset := token.NewFileSet()
	for _, dir := range dirs {
		top := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(top); err != nil || !st.IsDir() {
			return nil, nil, fmt.Errorf("code dir %q: not a directory under %s", dir, root)
		}
		err := filepath.WalkDir(top, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if p != top && skipDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(fset, p, nil, 0)
			if err != nil {
				return fmt.Errorf("parse %s: %w", filepath.ToSlash(rel), err)
			}
			n, b := scanFile(fset, filepath.ToSlash(rel), file)
			names, bad = append(names, n...), append(bad, b...)
			return nil
		})
		if err != nil {
			return nil, nil, fmt.Errorf("scan %s: %w", dir, err)
		}
	}
	return names, bad, nil
}

// scanFile finds slog calls and meter instrument constructors in one file.
// A call on an imported package is skipped (http.Error is not a logger)
// unless the package is log/slog. A local name that shadows an import (a
// parameter log *slog.Logger in a file that imports log) resolves to its
// declaration, so its calls are still scanned.
func scanFile(fset *token.FileSet, rel string, file *ast.File) ([]name, []Finding) {
	pkgs := importNames(file)
	var names []name
	var bad []Finding
	line := func(n ast.Node) int { return fset.Position(n.Pos()).Line }
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil {
			if p, imported := pkgs[id.Name]; imported && p != "log/slog" {
				return true
			}
		}
		method := sel.Sel.Name
		if i, ok := logArg[method]; ok && len(call.Args) > i {
			arg := call.Args[i]
			if s, ok := static(arg); ok {
				names = append(names, name{kind: logName, text: s, file: rel, line: line(arg)})
			} else {
				bad = append(bad, Finding{File: rel, Line: line(arg), Msg: "log event name is not a static string (the message argument of " + method + ")"})
			}
		}
		if instruments[method] && len(call.Args) > 0 {
			arg := call.Args[0]
			if s, ok := static(arg); ok {
				names = append(names, name{kind: metricName, text: s, file: rel, line: line(arg)})
			} else if s, ok := staticTail(arg); ok {
				names = append(names, name{kind: metricName, text: s, tail: true, file: rel, line: line(arg)})
			} else {
				bad = append(bad, Finding{File: rel, Line: line(arg), Msg: "metric name has no static string tail (the name argument of " + method + ")"})
			}
		}
		return true
	})
	return names, bad
}

// importNames maps each name a file uses for an import to its path.
func importNames(file *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := path.Base(p)
		if len(local) > 1 && local[0] == 'v' && strings.Trim(local[1:], "0123456789") == "" {
			local = path.Base(path.Dir(p))
		}
		if imp.Name != nil {
			local = imp.Name.Name
		}
		out[local] = p
	}
	return out
}

// static is the value of a string literal or a sum of string literals.
func static(e ast.Expr) (string, bool) {
	switch e := e.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(e.Value)
		return s, err == nil
	case *ast.ParenExpr:
		return static(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		l, ok := static(e.X)
		if !ok {
			return "", false
		}
		r, ok := static(e.Y)
		return l + r, ok
	}
	return "", false
}

// staticTail is the static string at the end of `expr + "tail"`.
func staticTail(e ast.Expr) (string, bool) {
	b, ok := e.(*ast.BinaryExpr)
	if !ok || b.Op != token.ADD {
		return "", false
	}
	if s, ok := static(b.Y); ok {
		if more, ok := staticTail(b.X); ok {
			return more + s, true
		}
		return s, true
	}
	return "", false
}
