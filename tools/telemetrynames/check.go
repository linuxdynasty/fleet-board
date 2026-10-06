package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one blocking problem at a place in the code.
type Finding struct {
	File string // slash path relative to the root
	Line int
	Msg  string
}

func (f Finding) String() string { return fmt.Sprintf("%s:%d: %s", f.File, f.Line, f.Msg) }

// Report is the result of Check. Violations block; DocOnly ("<spec>: <name>")
// is names a spec lists for a mapped area that no code uses yet, a report only.
type Report struct {
	Names      int
	Violations []Finding
	DocOnly    []string
}

// Check scans the Go code under cfg.CodeDirs below root and checks each
// telemetry name against the spec cfg maps it to. An error means the input
// could not be read (a bad config, a missing spec or code dir, a parse error).
func Check(root string, cfg Config) (Report, error) {
	if err := cfg.validate(); err != nil {
		return Report{}, err
	}
	specs := map[string]string{}
	for _, s := range cfg.specs() {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(s)))
		if err != nil {
			return Report{}, fmt.Errorf("read spec: %w", err)
		}
		specs[s] = string(data)
	}
	names, bad, err := scan(root, cfg.CodeDirs)
	if err != nil {
		return Report{}, err
	}
	rep := Report{Names: len(names), Violations: bad}
	for _, n := range names {
		if msg := judge(cfg, specs, n); msg != "" {
			rep.Violations = append(rep.Violations, Finding{File: n.file, Line: n.line, Msg: msg})
		}
	}
	sort.SliceStable(rep.Violations, func(i, j int) bool {
		a, b := rep.Violations[i], rep.Violations[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	rep.DocOnly = docOnly(cfg, specs, names)
	return rep, nil
}

// judge returns why a name breaks the rule, or "" when its spec lists it.
func judge(cfg Config, specs map[string]string, n name) string {
	var label, spec, why string
	candidates := []string{n.text}
	switch n.kind {
	case logName:
		area, s := cfg.logSpec(n.text)
		label, spec = fmt.Sprintf("log event %q", n.text), s
		if spec == "" {
			if area == "" {
				return label + " is not <area>.<event>, so it has no spec in the telemetry docs map"
			}
			return fmt.Sprintf("%s has area %q with no spec in the telemetry docs map", label, area)
		}
		why = fmt.Sprintf("the spec its area %q maps to", area)
	default:
		key := cfg.metricKey(n.text)
		label = fmt.Sprintf("metric %q", n.text)
		if n.tail {
			key = strings.TrimPrefix(n.text, "_")
			label = fmt.Sprintf("metric %q", "<prefix>"+n.text)
			candidates = candidates[:0]
			for _, p := range cfg.MetricPrefixes {
				candidates = append(candidates, p+n.text)
			}
		}
		prefix, s := cfg.metricSpec(key)
		if s == "" {
			return label + " has a tail with no spec in the telemetry docs map"
		}
		spec, why = s, fmt.Sprintf("the spec its tail prefix %q maps to", prefix)
	}
	if anyToken(specs[spec], candidates) {
		return ""
	}
	msg := fmt.Sprintf("%s is not in %s, %s", label, spec, why)
	var others []string
	for _, s := range cfg.specs() {
		if s != spec && anyToken(specs[s], candidates) {
			others = append(others, s)
		}
	}
	if len(others) > 0 {
		msg += "; it is only in " + strings.Join(others, ", ")
	}
	return msg
}

// docOnly lists the names each spec holds for the areas and tail prefixes
// mapped to it that no code name matches. A log token is <area>.<event> with
// exactly one dot, so span names (hub.snapshot.apply) are not counted.
func docOnly(cfg Config, specs map[string]string, names []name) []string {
	inCode := map[string]bool{}
	for _, n := range names {
		switch {
		case n.kind == metricName && n.tail:
			for _, p := range cfg.MetricPrefixes {
				inCode[p+n.text] = true
			}
		default:
			inCode[n.text] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, spec := range cfg.specs() {
		for _, tok := range tokens(specs[spec]) {
			if inCode[tok] || !mappedHere(cfg, spec, tok) {
				continue
			}
			entry := spec + ": " + tok
			if !seen[entry] {
				seen[entry] = true
				out = append(out, entry)
			}
		}
	}
	sort.Strings(out)
	return out
}

// mappedHere is true when tok is a log event or metric name whose area or
// tail prefix maps to spec.
func mappedHere(cfg Config, spec, tok string) bool {
	if strings.Count(tok, ".") == 1 {
		_, s := cfg.logSpec(tok)
		return s == spec && isLower(strings.Replace(tok, ".", "", 1))
	}
	if strings.Contains(tok, ".") || strings.HasSuffix(tok, "_") || !isLower(tok) {
		return false
	}
	key := cfg.metricKey(tok)
	if key == tok {
		return false
	}
	_, s := cfg.metricSpec(key)
	return s == spec
}

func isLower(s string) bool {
	for i := range len(s) {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return s != ""
}

func isWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// tokens splits text into runs of word characters and dots, without a
// trailing dot (the end of a sentence).
func tokens(text string) []string {
	var out []string
	start := -1
	for i := 0; i <= len(text); i++ {
		if i < len(text) && (isWord(text[i]) || text[i] == '.') {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if tok := strings.TrimRight(text[start:i], "."); tok != "" {
				out = append(out, tok)
			}
			start = -1
		}
	}
	return out
}

// anyToken is true when text holds one of names as a whole token.
func anyToken(text string, names []string) bool {
	for _, n := range names {
		if hasToken(text, n) {
			return true
		}
	}
	return false
}

// hasToken is true when name appears in text with no word character or dot
// before it and no word character (or a dot that starts another word) after
// it: `enroll.issue` does not match `enroll.issued`, `hub.started.` does.
func hasToken(text, name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(text); {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		s, e := i+j, i+j+len(name)
		before := s == 0 || !isWord(text[s-1]) && text[s-1] != '.'
		after := e == len(text) || !isWord(text[e]) && (text[e] != '.' || e+1 == len(text) || !isWord(text[e+1]))
		if before && after {
			return true
		}
		i = s + 1
	}
	return false
}
