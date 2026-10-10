package policy

import (
	"net"
	"net/url"
	"regexp"
	"strconv"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

var (
	harnessName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	labelName   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	envName     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
)

// resolve follows a YAML alias to the node it names.
func resolve(n *yaml.Node) *yaml.Node {
	for n.Kind == yaml.AliasNode && n.Alias != nil {
		n = n.Alias
	}
	return n
}

// isNull is a key with no value (`key:` or `key: ~`): no opinion.
func isNull(n *yaml.Node) bool {
	n = resolve(n)
	return n.Kind == yaml.ScalarNode && n.Tag == "!!null"
}

func scalar(n *yaml.Node, tag string) bool {
	return n.Kind == yaml.ScalarNode && n.Tag == tag
}

// wholeNumber reads an unsigned integer in lo..hi; want is the fixed phrase
// for a number outside the range.
func wholeNumber(n *yaml.Node, scope string, lo, hi uint64, want string) (uint64, *Problem) {
	n = resolve(n)
	var u uint64
	if !scalar(n, "!!int") || n.Decode(&u) != nil {
		return 0, problem(CodeBadType, n.Line, scope, "must be a whole number")
	}
	if u < lo || u > hi {
		return 0, problem(CodeOutOfRange, n.Line, scope, want)
	}
	return u, nil
}

func text(n *yaml.Node, scope string) (string, *Problem) {
	n = resolve(n)
	if !scalar(n, "!!str") {
		return "", problem(CodeBadType, n.Line, scope, "must be text")
	}
	return n.Value, nil
}

func boolean(n *yaml.Node, scope string) (bool, *Problem) {
	n = resolve(n)
	var b bool
	if !scalar(n, "!!bool") || n.Decode(&b) != nil {
		return false, problem(CodeBadType, n.Line, scope, "must be true or false")
	}
	return b, nil
}

// duration reads a Go duration with a unit ("30m", "2160h") and checks lo..hi
// (hi zero means no upper bound).
func duration(n *yaml.Node, scope string, lo, hi time.Duration, wantLo, wantHi string) (time.Duration, *Problem) {
	n = resolve(n)
	if !scalar(n, "!!str") {
		return 0, problem(CodeBadType, n.Line, scope, "must be a duration such as 30m or 2160h")
	}
	d, err := time.ParseDuration(n.Value)
	if err != nil {
		return 0, problem(CodeBadType, n.Line, scope, "must be a duration such as 30m or 2160h")
	}
	if d < lo {
		return 0, problem(CodeOutOfRange, n.Line, scope, wantLo)
	}
	if hi > 0 && d > hi {
		return 0, problem(CodeOutOfRange, n.Line, scope, wantHi)
	}
	return d, nil
}

// stringList reads a list of text items, each checked by ok; a failing item gets
// the fixed phrase bad. The result is never nil for a list, even an empty one.
func stringList(n *yaml.Node, scope string, ok func(string) bool, bad string) ([]string, *Problem) {
	n = resolve(n)
	if n.Kind != yaml.SequenceNode {
		return nil, problem(CodeBadType, n.Line, scope, "must be a list")
	}
	out := make([]string, 0, len(n.Content))
	seen := map[string]bool{}
	for _, item := range n.Content {
		s, p := text(item, scope)
		if p != nil {
			return nil, p
		}
		if !ok(s) {
			return nil, problem(CodeBadValue, resolve(item).Line, scope, bad)
		}
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

func isLoopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// loopbackAddr is a host:port with a loopback host and a port 1 to 65535.
func loopbackAddr(s string) bool {
	host, port, err := net.SplitHostPort(s)
	if err != nil || !isLoopbackHost(host) {
		return false
	}
	p, err := strconv.Atoi(port)
	return err == nil && p >= 1 && p <= 65535
}

func printable(s string, maxBytes int) bool {
	if len(s) > maxBytes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

// endpoint is an http or https URL with a host.
func endpoint(s string) (*url.URL, bool) {
	if len(s) > 512 || !printable(s, 512) {
		return nil, false
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return nil, false
	}
	return u, true
}
