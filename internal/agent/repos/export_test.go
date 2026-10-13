package repos

// ParseAuthStatusForTest parses `gh auth status` output.
func ParseAuthStatusForTest(out string) []Host { return parseAuthStatus(out) }

// ParseViewerPermissionForTest parses `gh repo view` permission output.
func ParseViewerPermissionForTest(out string) (string, bool, bool) {
	return parseViewerPermission(out)
}
