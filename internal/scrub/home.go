package scrub

import "strings"

// cleanHome is home without a trailing slash, or "" when home is not an
// absolute folder below the root made of name bytes and slashes. A home with
// a space, a quote or a ~ is left as written rather than half matched.
func cleanHome(home string) string {
	home = strings.TrimRight(home, "/")
	if !strings.HasPrefix(home, "/") {
		return ""
	}
	for i := range len(home) {
		if c := home[i]; c != '/' && (!nameByte(c) || c == '~') {
			return ""
		}
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
// before it does not continue a path, or it follows file://.
func homeStartsAt(s string, i int) bool {
	return i == 0 || !nameByte(s[i-1]) && s[i-1] != '/' || strings.HasSuffix(s[:i], "file://")
}

// homeEndsAt reports whether a home folder ending at end is whole: a slash,
// the end of s, or a byte that cannot be part of a name follows it.
func homeEndsAt(s string, end int) bool {
	return end == len(s) || s[end] == '/' || !nameByte(s[end])
}

// replaceHome writes each whole occurrence of home in s as "~", judged on s
// as it is. "~" is a name byte, so the result holds no whole home again.
func replaceHome(s, home string) string {
	if home == "" || !strings.Contains(s, home) {
		return s
	}
	var at []int
	for from := 0; ; {
		i := strings.Index(s[from:], home)
		if i < 0 {
			break
		}
		i += from
		if end := i + len(home); homeStartsAt(s, i) && homeEndsAt(s, end) {
			at = append(at, i)
			from = end
			continue
		}
		from = i + 1
	}
	if len(at) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	last := 0
	for _, i := range at {
		b.WriteString(s[last:i])
		b.WriteByte('~')
		last = i + len(home)
	}
	b.WriteString(s[last:])
	return b.String()
}

// endsInHome reports whether s ends with home where home could start, so a
// cut there would leave text a second pass reads as the home folder.
func endsInHome(s, home string) bool {
	return home != "" && strings.HasSuffix(s, home) && homeStartsAt(s, len(s)-len(home))
}
