package api

import (
	"strconv"
	"strings"

	"connectrpc.com/connect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// RepoSpec size rules from the 02b size-limits table. Whole messages stay
// under the 1 MiB Connect cap (02e); these are the per-field caps inside it.
const (
	// maxBranchPatternBytes is the RepoSpec.branch_pattern cap.
	maxBranchPatternBytes = 128
	// maxTrustedGateUsers is the RepoSpec trusted_gate.users entry cap.
	maxTrustedGateUsers = 64
	// maxTrustedGateLoginBytes is the per-entry login cap (trusted_gate.proto).
	maxTrustedGateLoginBytes = 64
	// maxTrackerProjects is the RepoSpec.tracker_projects entry cap.
	maxTrackerProjects = 64
)

// checkRequestID applies the 02b request_id rule: required, at most 64
// bytes, printable ASCII without spaces (a UUID or ULID).
func checkRequestID(requestID string) error {
	var v violations
	switch {
	case requestID == "":
		v.add("request_id", typev1.ReasonCode_REASON_CODE_REQUEST_ID_MISSING, "request_id is required")
	case len(requestID) > maxRequestID:
		v.add("request_id", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "request_id is over 64 bytes")
	case !printableASCII(requestID):
		v.add("request_id", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"request_id must be printable ASCII without spaces (a UUID or ULID)")
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// checkRepoIdentity applies the 02b identity rules to a repo write: the repo
// identity (host and path). It returns the canonical identity. Descriptions
// never echo the request.
func checkRepoIdentity(repo *hubv1.Repo) (host, path string, err error) {
	var v violations
	spec := repo.GetSpec()
	switch ref := spec.GetRepo(); {
	case repo == nil || spec == nil || ref == nil:
		v.add("repo", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "repo with its spec and repo ref is required")
	default:
		host, path = ref.GetHost(), ref.GetPath()
		switch {
		case host == "":
			v.add("repo.spec.repo.host", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT, "repo.spec.repo.host is required")
		case !hostname.MatchString(host):
			v.add("repo.spec.repo.host", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
				"repo.spec.repo.host may hold only letters, digits, '.', '-' and '_'")
		case host != strings.ToLower(host):
			v.add("repo.spec.repo.host", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"repo.spec.repo.host is lowercase (RepoRef identity)")
		}
		if badPath(path) {
			v.add("repo.spec.repo.path", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"repo.spec.repo.path is lowercase without '.git', segments of letters, digits and '., -, _, /'")
		}
	}
	if len(v) > 0 {
		return "", "", apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return host, path, nil
}

// checkRepoSpec applies the 02b spec rules: the branch pattern names its
// rejectRepoDocRefs refuses the FleetConfig portable refs on the live
// paths (#397): code_host_ref, tracker integration refs and trusted-gate
// integration refs belong to exchange documents, where import resolves
// them. Live calls keep writing hub ids; silently ignoring a ref would
// drop the owner's intent.
func rejectRepoDocRefs(spec *hubv1.RepoSpec) error {
	var v violations
	if spec.GetCodeHostRef() != nil {
		v.add("repo.spec.code_host_ref", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"code_host_ref is for fleet.yaml documents; live calls write repo.code_host")
	}
	for i, tp := range spec.GetTrackerProjects() {
		if tp.GetIntegrationRef() != nil {
			v.add("repo.spec.tracker_projects["+strconv.Itoa(i)+"].integration_ref",
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"integration_ref is for fleet.yaml documents; live calls write integration ids")
			break
		}
	}
	for i, u := range spec.GetTrustedGate().GetUsers() {
		if u.GetIntegrationRef() != nil {
			v.add("repo.spec.trusted_gate.users["+strconv.Itoa(i)+"].integration_ref",
				typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"integration_ref is for fleet.yaml documents; live calls write integration ids")
			break
		}
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// placeholder, and the spec stays inside the size-limits table.
func checkRepoSpec(spec *hubv1.RepoSpec) error {
	var v violations
	if bp := spec.GetBranchPattern(); bp != "" {
		switch {
		case len(bp) > maxBranchPatternBytes:
			v.add("repo.spec.branch_pattern", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
				"repo.spec.branch_pattern is over 128 bytes")
		case !strings.Contains(bp, "{work}") && !strings.Contains(bp, "{goal}"):
			v.add("repo.spec.branch_pattern", typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				"repo.spec.branch_pattern names {work} or {goal}")
		}
	}
	if users := spec.GetTrustedGate().GetUsers(); len(users) > maxTrustedGateUsers {
		v.add("repo.spec.trusted_gate.users", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
			"repo.spec.trusted_gate.users holds at most 64 entries")
	} else {
		for i, u := range users {
			if len(u.GetLogin()) > maxTrustedGateLoginBytes {
				v.add("repo.spec.trusted_gate.users["+strconv.Itoa(i)+"].login",
					typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
					"repo.spec.trusted_gate.users login is over 64 bytes")
				break
			}
		}
	}
	if projects := spec.GetTrackerProjects(); len(projects) > maxTrackerProjects {
		v.add("repo.spec.tracker_projects", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE,
			"repo.spec.tracker_projects holds at most 64 entries")
	}
	if len(v) > 0 {
		return apiError(connect.CodeInvalidArgument, v[0].GetReason(),
			"the request has invalid fields", nil, v)
	}
	return nil
}

// badPath is a noncanonical repo path. RepoRef identity is lowercase with no
// ".git" (type/v1/work.proto, 02h "Repo identity"), so an uppercase path or
// a ".git" suffix names a repo the agent and provider read as another one;
// empty, dot and unprintable segments are refused as before.
func badPath(path string) bool {
	if path == "" {
		return true
	}
	if path != strings.ToLower(path) {
		return true
	}
	if strings.HasSuffix(path, ".git") {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		switch {
		case seg == "", seg == ".", seg == "..":
			return true
		case !printableASCII(seg):
			return true
		}
		for _, r := range seg {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			case r == '.', r == '-', r == '_':
			default:
				return true
			}
		}
	}
	return false
}

// repoID is the Repo id for host/path.
func repoID(host, path string) string {
	return host + "/" + path
}

// parseRepoID splits a Repo id back into host and path.
func parseRepoID(id string) (host, path string, err error) {
	host, path, found := strings.Cut(id, "/")
	if !found || host == "" || path == "" || badPath(path) || !hostname.MatchString(host) ||
		host != strings.ToLower(host) {
		return "", "", apiError(connect.CodeInvalidArgument,
			typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
			"the request has invalid fields", nil,
			[]*typev1.FieldViolation{{
				Field:       "id",
				Reason:      typev1.ReasonCode_REASON_CODE_INVALID_ARGUMENT,
				Description: "id is host/path",
			}})
	}
	return host, path, nil
}
