package policy

import (
	"bytes"
	"errors"
	"io"
	"regexp"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// Result is what [Parse] found. Policy is nil exactly when Problem is set.
type Result struct {
	Policy *Policy
	// SchemaVersion is the file's schema_version when it could be read and
	// is a short printable value (even when another key is the problem);
	// "1" for a file with no schema_version key.
	SchemaVersion string
	Problem       *Problem
}

var yamlLine = regexp.MustCompile(`line (\d+)`)

// Parse reads policy.yaml bytes. It is strict: an unknown key, a duplicate
// key, a wrong type or a value outside its range is a Problem (safe mode),
// never a guess. The reason in the Problem never carries file contents.
func Parse(data []byte) Result {
	if len(data) > MaxFileBytes {
		return Result{Problem: problem(CodeTooLarge, 0, "", "the file is over 64 KiB")}
	}
	root, p := rootMapping(data)
	if p != nil {
		return Result{Problem: p}
	}
	out := &Policy{SchemaVersion: SchemaVersion}
	res := Result{SchemaVersion: SchemaVersion}
	if root == nil {
		res.Policy = out
		return res
	}
	// schema_version first: with an unknown schema the other keys cannot be
	// judged, and it is reported even when another key is the problem.
	for i := 0; i+1 < len(root.Content); i += 2 {
		if k := resolve(root.Content[i]); k.Kind == yaml.ScalarNode && k.Value == "schema_version" {
			if isNull(root.Content[i+1]) {
				continue
			}
			v, sp := text(root.Content[i+1], "schema_version")
			res.SchemaVersion = ""
			if sp == nil && printable(v, 16) {
				res.SchemaVersion = v
			}
			if sp == nil && v != SchemaVersion {
				res.Problem = problem(CodeSchemaVersion, resolve(root.Content[i+1]).Line, "schema_version", `only "1" is known`)
				return res
			}
			if sp != nil {
				res.Problem = sp
				return res
			}
		}
	}
	if p := visit(root, "", topKeys, func(key string, v *yaml.Node) *Problem {
		if isNull(v) {
			return nil
		}
		return topKeys[key](out, v)
	}); p != nil {
		res.Problem = p
		return res
	}
	res.Policy = out
	return res
}

// rootMapping returns the top-level mapping, nil for an empty file.
func rootMapping(data []byte) (*yaml.Node, *Problem) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	err := dec.Decode(&doc)
	if errors.Is(err, io.EOF) {
		return nil, nil
	}
	if err != nil {
		return nil, yamlProblem(err)
	}
	var second yaml.Node
	if err := dec.Decode(&second); !errors.Is(err, io.EOF) {
		return nil, problem(CodeBadYAML, second.Line, "", "more than one YAML document")
	}
	n := &doc
	if n.Kind == yaml.DocumentNode {
		if len(n.Content) == 0 {
			return nil, nil
		}
		n = n.Content[0]
	}
	n = resolve(n)
	if isNull(n) {
		return nil, nil
	}
	if n.Kind != yaml.MappingNode {
		return nil, problem(CodeBadType, n.Line, "", "the file must be a map of keys to values")
	}
	return n, nil
}

// yamlProblem keeps only the line number of a YAML syntax error: the library's
// text can quote the file.
func yamlProblem(err error) *Problem {
	line := 0
	if m := yamlLine.FindStringSubmatch(err.Error()); m != nil {
		line, _ = strconv.Atoi(m[1])
	}
	return problem(CodeBadYAML, line, "", "not valid YAML")
}

// handler reads one key's value into a Policy.
type handler func(p *Policy, v *yaml.Node) *Problem

// visit walks a mapping: every key must be in table (unknown_key otherwise)
// and appear once (duplicate_key); fn gets each value in file order. scope is
// the name of the key that holds the mapping ("" at the top).
func visit[V any](m *yaml.Node, scope string, table map[string]V, fn func(key string, v *yaml.Node) *Problem) *Problem {
	m = resolve(m)
	if m.Kind != yaml.MappingNode {
		return problem(CodeBadType, m.Line, scope, "must be a map of keys to values")
	}
	seen := map[string]bool{}
	for i := 0; i+1 < len(m.Content); i += 2 {
		k := resolve(m.Content[i])
		if !scalar(k, "!!str") {
			return problem(CodeBadType, k.Line, scope, "keys must be text")
		}
		if _, ok := table[k.Value]; !ok {
			return problem(CodeUnknownKey, k.Line, scope, "unknown key")
		}
		if seen[k.Value] {
			return problem(CodeDuplicateKey, k.Line, scope, k.Value+": given twice")
		}
		seen[k.Value] = true
		if p := fn(k.Value, m.Content[i+1]); p != nil {
			return p
		}
	}
	return nil
}
