package hostname

import "strings"

// Mismatch reports whether reported differs from expected: both compare
// lower case, without a trailing "." and then a trailing ".local"; an empty
// reported hostname is a mismatch.
func Mismatch(expected, reported string) bool {
	r := norm(reported)
	return r == "" || r != norm(expected)
}

func norm(h string) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(h), "."), ".local")
}
