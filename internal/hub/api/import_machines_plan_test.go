package api

import (
	"testing"

	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// TestPlanConfigImportKeepsUnknownMachinesPending is criterion 21: a
// machine entry no enrolled machine has is kept as a pending machine
// (CONFIG_ENTITY_PENDING_MACHINE), not refused as an unsupported line.
func TestPlanConfigImportKeepsUnknownMachinesPending(t *testing.T) {
	t.Parallel()
	maxActive := uint32(2)
	doc := &hubv1.FleetConfig{
		FormatVersion: 1,
		Machines: []*hubv1.MachineConfig{{
			Name:   "field-laptop",
			Labels: []string{"gpu"},
			Values: &typev1.SettingValues{MaxActive: &maxActive},
		}},
	}
	changes, err := planConfigImport(doc, nil, nil, nil)
	if err != nil {
		t.Fatalf("planConfigImport: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("plan lines = %+v, want 1", changes)
	}
	got := changes[0]
	if got.GetEntity() != hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE {
		t.Errorf("line entity = %v, want CONFIG_ENTITY_PENDING_MACHINE", got.GetEntity())
	}
	if got.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE {
		t.Errorf("line kind = %v, want CREATE", got.GetKind())
	}
	if got.GetName() != "field-laptop" {
		t.Errorf("line name = %q, want field-laptop", got.GetName())
	}
	if got.GetProblem() != nil {
		t.Errorf("line problem = %v, want none", got.GetProblem())
	}
}

func machineDoc(entries ...*hubv1.MachineConfig) *hubv1.FleetConfig {
	return &hubv1.FleetConfig{FormatVersion: 1, Machines: entries}
}

func machineRest() *configRest {
	two := uint32(2)
	return &configRest{
		machines: map[string][]machineBaseline{
			"laptop": {{id: "m1", labels: []string{"gpu"}, values: &typev1.SettingValues{MaxActive: &two}}},
			"duo": {
				{id: "m2", labels: []string{"a"}},
				{id: "m3", labels: []string{"b"}},
			},
		},
		pending: map[string]pendingBaseline{
			"kept": {labels: []string{"field"}, values: &typev1.SettingValues{}},
		},
	}
}

func planMachines(t *testing.T, doc *hubv1.FleetConfig, rest *configRest) []*hubv1.ConfigChange {
	t.Helper()
	changes, err := planConfigImport(doc, nil, nil, rest)
	if err != nil {
		t.Fatalf("planConfigImport: %v", err)
	}
	return changes
}

// TestPlanConfigImportUpdatesEnrolledMachineLabels is the matched half
// of criterion 21: an entry for an enrolled display name sets labels.
func TestPlanConfigImportUpdatesEnrolledMachineLabels(t *testing.T) {
	t.Parallel()
	changes := planMachines(t, machineDoc(&hubv1.MachineConfig{Name: "laptop", Labels: []string{"gpu", "field"}}), machineRest())
	if len(changes) != 1 {
		t.Fatalf("plan lines = %+v, want 1", changes)
	}
	got := changes[0]
	if got.GetEntity() != hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE {
		t.Errorf("line entity = %v, want CONFIG_ENTITY_MACHINE", got.GetEntity())
	}
	if got.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
		t.Errorf("line kind = %v, want UPDATE", got.GetKind())
	}
	if len(got.GetChangedFields()) != 1 || got.GetChangedFields()[0] != "spec.labels" {
		t.Errorf("changed fields = %v, want [spec.labels]", got.GetChangedFields())
	}
}

// TestPlanConfigImportLeavesMatchingMachineAlone proves convergence:
// what the hub holds plans unchanged, and an empty entry states no
// opinion (import never deletes what the document does not name).
func TestPlanConfigImportLeavesMatchingMachineAlone(t *testing.T) {
	t.Parallel()
	two := uint32(2)
	for _, entry := range []*hubv1.MachineConfig{
		{Name: "laptop", Labels: []string{"gpu"}, Values: &typev1.SettingValues{MaxActive: &two}},
		{Name: "laptop"},
	} {
		changes := planMachines(t, machineDoc(entry), machineRest())
		if len(changes) != 1 {
			t.Fatalf("plan lines = %+v, want 1", changes)
		}
		if got := changes[0]; got.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED ||
			got.GetEntity() != hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE {
			t.Errorf("line = %v/%v, want MACHINE UNCHANGED", got.GetEntity(), got.GetKind())
		}
	}
}

// TestPlanConfigImportUpdatesMachineValues proves the settings half of
// a matched entry: stated values the layer does not hold plan an update.
func TestPlanConfigImportUpdatesMachineValues(t *testing.T) {
	t.Parallel()
	four := uint32(4)
	changes := planMachines(t,
		machineDoc(&hubv1.MachineConfig{Name: "laptop", Values: &typev1.SettingValues{MaxActive: &four}}),
		machineRest())
	if len(changes) != 1 {
		t.Fatalf("plan lines = %+v, want 1", changes)
	}
	got := changes[0]
	if got.GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
		t.Fatalf("line kind = %v, want UPDATE", got.GetKind())
	}
	if len(got.GetChangedFields()) != 1 || got.GetChangedFields()[0] != "spec.values" {
		t.Errorf("changed fields = %v, want [spec.values]", got.GetChangedFields())
	}
}

// TestPlanConfigImportTracksKeptEntries proves the kept half: a stored
// pending entry plans unchanged when it holds what the entry states,
// update otherwise.
func TestPlanConfigImportTracksKeptEntries(t *testing.T) {
	t.Parallel()
	same := planMachines(t, machineDoc(&hubv1.MachineConfig{Name: "kept", Labels: []string{"field"}}), machineRest())
	if len(same) != 1 || same[0].GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED ||
		same[0].GetEntity() != hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE {
		t.Fatalf("same lines = %+v, want one PENDING_MACHINE UNCHANGED", same)
	}
	diff := planMachines(t, machineDoc(&hubv1.MachineConfig{Name: "kept", Labels: []string{"other"}}), machineRest())
	if len(diff) != 1 || diff[0].GetKind() != hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
		t.Fatalf("diff lines = %+v, want one UPDATE", diff)
	}
	if len(diff[0].GetChangedFields()) != 1 || diff[0].GetChangedFields()[0] != "spec.labels" {
		t.Errorf("changed fields = %v, want [spec.labels]", diff[0].GetChangedFields())
	}
}

// TestPlanConfigImportRefusesBadMachineEntries proves the plan fails
// closed: an entry without a name, with a bad label or bad values, a
// name two enrolled machines share, and a duplicated entry all plan a
// blocking line, never half a write.
func TestPlanConfigImportRefusesBadMachineEntries(t *testing.T) {
	t.Parallel()
	bad := uint32(99)
	cases := []struct {
		name string
		doc  *hubv1.FleetConfig
	}{
		{"empty name", machineDoc(&hubv1.MachineConfig{})},
		{"bad label", machineDoc(&hubv1.MachineConfig{Name: "m", Labels: []string{"GPU"}})},
		{"bad values", machineDoc(&hubv1.MachineConfig{Name: "m", Values: &typev1.SettingValues{MaxActive: &bad}})},
		{"ambiguous name", machineDoc(&hubv1.MachineConfig{Name: "duo", Labels: []string{"c"}})},
		{"duplicated", machineDoc(
			&hubv1.MachineConfig{Name: "m"},
			&hubv1.MachineConfig{Name: "m"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changes := planMachines(t, tc.doc, machineRest())
			blocked := false
			for _, c := range changes {
				if c.GetProblem() != nil &&
					c.GetProblem().GetReason() == typev1.ReasonCode_REASON_CODE_CONFIG_INVALID {
					blocked = true
				}
			}
			if !blocked {
				t.Errorf("plan lines = %+v, want a CONFIG_INVALID line", changes)
			}
		})
	}
}

// TestMachineLayerSpecRoundTrips proves the stored layer shape survives
// its own encoding: what the apply writes, the baseline reads back.
func TestMachineLayerSpecRoundTrips(t *testing.T) {
	t.Parallel()
	two := uint32(2)
	raw, err := machineLayerSpec(&typev1.SettingValues{MaxActive: &two})
	if err != nil {
		t.Fatalf("machineLayerSpec: %v", err)
	}
	got, err := layerValues(raw)
	if err != nil {
		t.Fatalf("layerValues: %v", err)
	}
	if !proto.Equal(got, &typev1.SettingValues{MaxActive: &two}) {
		t.Errorf("layer values = %+v, want max_active 2", got)
	}
	if _, err := layerValues([]byte("{broken")); err == nil {
		t.Error("corrupt layer accepted, want an error")
	}
}
