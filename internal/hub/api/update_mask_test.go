package api_test

import (
	"testing"

	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/api"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// repoMaskAllows mirrors the UpdateRepo allowlist: every spec field except
// spec.repo (repo.proto: "Updatable: every spec field except spec.repo").
var repoMaskAllows = map[string]bool{
	"spec.enabled":            true,
	"spec.auto_dispatch":      true,
	"spec.consent":            true,
	"spec.max_parallel_goals": true,
	"spec.worktree_mode":      true,
	"spec.branch_pattern":     true,
	"spec.base_branch":        true,
	"spec.hooks_mode":         true,
	"spec.tracker_projects":   true,
	"spec.trusted_gate":       true,
}

func TestCheckFieldMask(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		paths   []string
		nilMask bool
		wantErr bool
		// wantFields are the violation fields, in order, when wantErr.
		wantFields []string
		wantReason typev1.ReasonCode
	}{
		{
			name: "nil mask names no field", nilMask: true, wantErr: true,
			wantFields: []string{"update_mask"},
			wantReason: typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID,
		},
		{
			name: "empty mask names no field", paths: nil, wantErr: true,
			wantFields: []string{"update_mask"},
			wantReason: typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID,
		},
		{
			name: "every updatable spec field passes",
			paths: []string{
				"spec.enabled", "spec.auto_dispatch", "spec.consent",
				"spec.max_parallel_goals", "spec.worktree_mode", "spec.branch_pattern",
				"spec.base_branch", "spec.hooks_mode", "spec.tracker_projects",
				"spec.trusted_gate",
			},
		},
		{
			name: "identity is not updatable", paths: []string{"spec.repo"}, wantErr: true,
			wantFields: []string{"update_mask.paths[0]"},
			wantReason: typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID,
		},
		{
			name: "unknown path fails", paths: []string{"spec.enabled", "owner"},
			wantErr:    true,
			wantFields: []string{"update_mask.paths[1]"},
			wantReason: typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID,
		},
		{
			name: "a repeated path fails", paths: []string{"spec.enabled", "spec.enabled"},
			wantErr:    true,
			wantFields: []string{"update_mask.paths[1]"},
			wantReason: typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID,
		},
		{
			name:    "each bad path gets its violation",
			paths:   []string{"spec.repo", "spec.enabled", "nope", "spec.enabled"},
			wantErr: true,
			wantFields: []string{
				"update_mask.paths[0]", "update_mask.paths[2]", "update_mask.paths[3]",
			},
			wantReason: typev1.ReasonCode_REASON_CODE_UPDATE_MASK_INVALID,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var mask *fieldmaskpb.FieldMask
			if !tc.nilMask {
				mask = &fieldmaskpb.FieldMask{Paths: tc.paths}
			}
			err := api.CheckFieldMask(mask, repoMaskAllows)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("CheckFieldMask(%v) = %v, want nil", tc.paths, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("CheckFieldMask(%v) = nil, want error", tc.paths)
			}
			code, reason, fields := detail(t, err)
			_ = code
			if reason != tc.wantReason {
				t.Errorf("reason = %v, want %v", reason, tc.wantReason)
			}
			if len(fields) != len(tc.wantFields) {
				t.Fatalf("fields = %v, want %v", fields, tc.wantFields)
			}
			for i := range fields {
				if fields[i] != tc.wantFields[i] {
					t.Errorf("fields[%d] = %q, want %q", i, fields[i], tc.wantFields[i])
				}
			}
		})
	}
}
