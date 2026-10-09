package scrub

import "strings"

// cleanHome is home without a trailing slash, or "" when home is not an
// absolute folder below the root.
func cleanHome(home string) string {
	home = strings.TrimRight(home, "/")
	if !strings.HasPrefix(home, "/") || strings.ContainsAny(home, " \t\n\r") {
		return ""
	}
	return home
}

// nameByte reports whether c can be part of a file name, so a home folder
// followed by it is the start of a longer name. Bytes of non-ASCII
// characters count as name bytes.
func nameByte(c byte) bool {
	return c >= 0x80 || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' ||
		c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' || c == '~'
}

// homeStartsAt reports whether a whole home folder can start at i: the byte
// before it does not continue a path.
func homeStartsAt(s string, i int) bool {
	return i == 0 || !nameByte(s[i-1]) && s[i-1] != '/'
}

// replaceHome writes each whole occurrence of home in s as "~": one that
// starts a path and ends at a slash, the end of s, or a byte that cannot be
// part of a name.
func replaceHome(s, home string) string {
	if home == "" || !strings.Contains(s, home) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last, from := 0, 0
	for {
		i := strings.Index(s[from:], home)
		if i < 0 {
			break
		}
		i += from
		end := i + len(home)
		if homeStartsAt(s, i) && (end == len(s) || s[end] == '/' || !nameByte(s[end])) {
			b.WriteString(s[last:i])
			b.WriteByte('~')
			last = end
			from = end
			continue
		}
		from = i + 1
	}
	b.WriteString(s[last:])
	return b.String()
}

// endsInHome reports whether s ends with home where home could start, so a
// cut there would leave text a second pass reads as the home folder.
func endsInHome(s, home string) bool {
	return home != "" && strings.HasSuffix(s, home) && homeStartsAt(s, len(s)-len(home))
}
