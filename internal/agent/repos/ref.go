package repos

import (
	"net/url"
	"strings"
)

// NormalizeRemote turns a git remote URL into the repo identity (02h "Repo
// identity"): lowercase (host, path) with no .git suffix, no user and no
// port. scp-like (`git@host:path`), ssh:// and https:// forms give the
// same value. A value with no host and path (local paths, file:// URLs,
// one-segment hosts) is not a repo: ok is false.
func NormalizeRemote(remote string) (host, path string, ok bool) {
	rest := remote
	if i := strings.Index(rest, "://"); i >= 0 {
		u, err := url.Parse(rest)
		if err != nil || u.Hostname() == "" {
			return "", "", false
		}
		host = u.Hostname()
		rest = strings.TrimPrefix(u.EscapedPath(), "/")
	} else if i := strings.IndexByte(rest, ':'); i >= 0 {
		host, rest = rest[:i], rest[i+1:]
		if strings.Contains(host, "/") || host == "" {
			return "", "", false
		}
		if j := strings.IndexByte(host, '@'); j >= 0 {
			host = host[j+1:]
		}
	} else {
		return "", "", false
	}
	path = strings.TrimSuffix(rest, "/")
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if host == "" || strings.Contains(path, ":") || strings.Count(path, "/") < 1 {
		return "", "", false
	}
	return strings.ToLower(host), strings.ToLower(path), true
}
