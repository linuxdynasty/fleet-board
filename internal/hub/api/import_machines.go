package api

import (
	"context"
	"fmt"
	"sort"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// machineBaseline is one enrolled machine the import plan diffs a
// machine entry against: the id (for the settings-layer scope), the
// display name and the labels with the layer's values. version is the
// row's version, carried into the apply op so a concurrent owner write
// aborts the apply instead of losing the change.
type machineBaseline struct {
	id      string
	labels  []string
	values  *typev1.SettingValues
	version uint64
}

// pendingBaseline is one kept machine entry the plan diffs against.
type pendingBaseline struct {
	labels []string
	values *typev1.SettingValues
}

// classifiedMachine is one machine entry's plan line with the write
// an apply rebuilds from the same classification (like the repo
// classifier): a matched enrolled machine writes ImportMachineOp, a
// kept entry writes ImportPendingOp. hasOp is false for unchanged and
// blocked lines.
type classifiedMachine struct {
	line      *hubv1.ConfigChange
	machine   statedb.ImportMachineOp
	pending   statedb.ImportPendingOp
	isPending bool
	hasOp     bool
}

// emptyValues is an unset or default SettingValues: no opinion, so
// import leaves a stored layer alone and never deletes one (config.proto:
// import only creates and updates).
func emptyValues(v *typev1.SettingValues) bool {
	return v == nil || proto.Equal(v, &typev1.SettingValues{})
}

// sortedLabels is a copy of labels in order, for order-insensitive
// comparison: enroll and the owner may spell the same set differently.
func sortedLabels(labels []string) []string {
	out := append([]string{}, labels...)
	sort.Strings(out)
	return out
}

// equalLabelSets reports whether two label lists hold the same set.
func equalLabelSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := sortedLabels(a), sortedLabels(b)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// machineLayerSpec renders the SettingsSpec an import stores for a
// machine entry: the entry's values with the scope unset (the scope
// column names the machine once the row exists). The store keeps these
// bytes opaque.
func machineLayerSpec(values *typev1.SettingValues) ([]byte, error) {
	if values == nil {
		values = &typev1.SettingValues{}
	}
	return protojson.Marshal(&hubv1.SettingsSpec{Values: values})
}

// layerValues unwraps a stored machine layer's SettingsSpec protojson
// to its values. A corrupt stored row is the hub's own failure.
func layerValues(raw []byte) (*typev1.SettingValues, error) {
	var spec hubv1.SettingsSpec
	if err := protojson.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("api: decode machine layer: %w", err)
	}
	if spec.GetValues() == nil {
		return &typev1.SettingValues{}, nil
	}
	return spec.GetValues(), nil
}

// machineRestBaseline reads the stored machines an import plan diffs
// machine entries against: the enrolled machines by display name with
// their layers, and the kept entries by name.
func machineRestBaseline(ctx context.Context, s *store.Store) (map[string][]machineBaseline, map[string]pendingBaseline, error) {
	identities, err := statedb.ListMachineIdentities(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	layers, err := statedb.ListMachineLayerSpecs(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	rows, err := statedb.ListPendingMachines(ctx, s)
	if err != nil {
		return nil, nil, err
	}
	enrolled := map[string][]machineBaseline{}
	for _, id := range identities {
		values := &typev1.SettingValues{}
		if raw, ok := layers[id.MachineID]; ok {
			if values, err = layerValues(raw); err != nil {
				return nil, nil, err
			}
		}
		enrolled[id.Name] = append(enrolled[id.Name], machineBaseline{
			id: id.MachineID, labels: id.Labels, values: values,
			version: id.Version,
		})
	}
	kept := map[string]pendingBaseline{}
	for _, p := range rows {
		values, err := layerValues(p.Values)
		if err != nil {
			return nil, nil, err
		}
		kept[p.Name] = pendingBaseline{labels: p.Labels, values: values}
	}
	return enrolled, kept, nil
}

// exportMachines renders the machines section (02m): one entry per
// enrolled machine (its labels with its layer's values, when it holds
// any) and one per kept entry, in name order. A kept row shadowing an
// enrolled name (only a rename leaves one) folds into the enrolled
// entry instead of rendering beside it: the enrolled labels and values
// win, the kept ones fill in where the enrolled entry states none (the
// import's own fold direction), so the export holds one entry per name
// and the next import apply heals the pair instead of refusing the
// second entry as a duplicate.
func exportMachines(ctx context.Context, s *store.Store) ([]*hubv1.MachineConfig, error) {
	identities, err := statedb.ListMachineIdentities(ctx, s)
	if err != nil {
		return nil, err
	}
	layers, err := statedb.ListMachineLayerSpecs(ctx, s)
	if err != nil {
		return nil, err
	}
	rows, err := statedb.ListPendingMachines(ctx, s)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.MachineConfig
	for _, id := range identities {
		mc := &hubv1.MachineConfig{Name: id.Name, Labels: id.Labels}
		if raw, ok := layers[id.MachineID]; ok {
			values, err := layerValues(raw)
			if err != nil {
				return nil, err
			}
			if !emptyValues(values) {
				mc.Values = values
			}
		}
		out = append(out, mc)
	}
	for _, p := range rows {
		values, err := layerValues(p.Values)
		if err != nil {
			return nil, err
		}
		if foldShadowedPending(out, p.Name, p.Labels, values) {
			continue
		}
		mc := &hubv1.MachineConfig{Name: p.Name, Labels: p.Labels}
		if !emptyValues(values) {
			mc.Values = values
		}
		out = append(out, mc)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].GetName() < out[j].GetName() })
	return out, nil
}

// foldShadowedPending folds a kept row shadowing an enrolled name into
// the enrolled export entries, the way the import folds a shadowed pair
// (classifyMatchedMachine): the enrolled entry keeps what it states,
// the kept labels and values fill in where it states none. It reports
// whether any enrolled entry held the name.
func foldShadowedPending(out []*hubv1.MachineConfig, name string, labels []string,
	values *typev1.SettingValues,
) bool {
	matched := false
	for _, mc := range out {
		if mc.GetName() != name {
			continue
		}
		matched = true
		if len(mc.GetLabels()) == 0 && len(labels) > 0 {
			mc.Labels = labels
		}
		if mc.GetValues() == nil && !emptyValues(values) {
			mc.Values = values
		}
	}
	return matched
}

// countMachineLines counts a plan's machine lines for the applied log
// and audit detail: MACHINE updates with a write, and kept-entry
// creates and updates.
func countMachineLines(changes []*hubv1.ConfigChange) (machines, pending int) {
	for _, c := range changes {
		switch c.GetEntity() {
		case hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE:
			if c.GetKind() == hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE {
				machines++
			}
		case hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE:
			switch c.GetKind() {
			case hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE,
				hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE:
				pending++
			default:
				// Unchanged and blocked lines write nothing.
			}
		default:
			// Repos and the other sections count their own way.
		}
	}
	return machines, pending
}

// checkMachineEntry validates one document machine entry: the name
// (the display-name rules) with its labels (the ids.proto label rules)
// and values (the SettingValues rules). It returns the plan line for
// an invalid entry, like the repo classifier.
func checkMachineEntry(mc *hubv1.MachineConfig, i int) *hubv1.ConfigChange {
	field := fmt.Sprintf("config.machines[%d]", i)
	name := mc.GetName()
	if name == "" {
		name = fmt.Sprintf("machines[%d]", i)
	}
	var v violations
	switch {
	case mc.GetName() == "":
		v.add("name", typev1.ReasonCode_REASON_CODE_INVALID_NAME, "machine_name is required")
	case len(mc.GetName()) > maxDisplayName:
		v.add("name", typev1.ReasonCode_REASON_CODE_FIELD_TOO_LARGE, "machine_name is over 128 bytes")
	case !displayText(mc.GetName()):
		v.add("name", typev1.ReasonCode_REASON_CODE_INVALID_NAME,
			"machine_name must be UTF-8 without control or invisible format characters")
	}
	checkLabels(&v, mc.GetLabels())
	if err := checkMergedValues(mc.GetValues()); err != nil {
		return blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE, name, field, firstViolationText(err))
	}
	if len(v) > 0 {
		return blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE, name, field,
			v[0].GetField()+": "+v[0].GetDescription())
	}
	return nil
}

// classifyMachine plans one document machine entry (02m): an entry
// matching an enrolled display name sets its labels and settings
// (entity MACHINE); one matching none is kept as a pending machine
// (entity PENDING_MACHINE), applied at enroll. Empty labels or values
// are no opinion: import never deletes what the document does not
// name. A name several enrolled machines share is blocked: the plan
// cannot tell which row the entry means.
func classifyMachine(mc *hubv1.MachineConfig, i int, enrolled map[string][]machineBaseline,
	pending map[string]pendingBaseline, seen map[string]bool,
) classifiedMachine {
	field := fmt.Sprintf("config.machines[%d]", i)
	if problem := checkMachineEntry(mc, i); problem != nil {
		return classifiedMachine{line: problem}
	}
	name := mc.GetName()
	if seen[name] {
		return classifiedMachine{line: blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE,
			name, field, "the machine is duplicated in the document")}
	}
	seen[name] = true
	kept, hasKept := pending[name]
	if rows := enrolled[name]; len(rows) > 1 {
		return classifiedMachine{line: blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE,
			name, field, "several enrolled machines share this display name")}
	} else if len(rows) == 1 {
		return classifyMatchedMachine(mc, rows[0], kept, hasKept)
	}
	return classifyPendingMachine(mc, kept, hasKept)
}

// classifyMatchedMachine plans an entry for an enrolled machine: update
// when the entry states labels or values the machine does not hold,
// unchanged otherwise. A kept row shadowing the name (only a rename can
// leave one: enroll consumes it) is dropped by the apply, folding its
// labels and values in where the entry states none, so the pair cannot
// linger. Every emitted op carries the baseline version, so the apply
// refuses a machine an owner write moved since the dry run.
func classifyMatchedMachine(mc *hubv1.MachineConfig, stored machineBaseline, kept pendingBaseline, hasKept bool) classifiedMachine {
	name := mc.GetName()
	labelsSame := len(mc.GetLabels()) == 0 || equalLabelSets(mc.GetLabels(), stored.labels)
	storedValues := stored.values
	if storedValues == nil {
		storedValues = &typev1.SettingValues{}
	}
	valuesSame := emptyValues(mc.GetValues()) || proto.Equal(mc.GetValues(), storedValues)
	if hasKept {
		if len(mc.GetLabels()) == 0 && len(kept.labels) > 0 {
			labelsSame = equalLabelSets(kept.labels, stored.labels)
		}
		if emptyValues(mc.GetValues()) && !emptyValues(kept.values) {
			valuesSame = proto.Equal(kept.values, storedValues)
		}
	}
	if labelsSame && valuesSame {
		out := classifiedMachine{line: &hubv1.ConfigChange{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE, Name: name,
			Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED,
		}}
		if hasKept {
			// Nothing to change on the machine, but the shadowing
			// kept row still goes.
			out.machine = statedb.ImportMachineOp{
				MachineID: stored.id, Name: name,
				ExpectedVersion: stored.version, DropPending: name,
			}
			out.hasOp = true
		}
		return out
	}
	var changed []string
	op := statedb.ImportMachineOp{MachineID: stored.id, Name: name, ExpectedVersion: stored.version}
	if hasKept {
		op.DropPending = name
	}
	wantLabels := mc.GetLabels()
	if len(wantLabels) == 0 && hasKept {
		wantLabels = kept.labels
	}
	if len(wantLabels) > 0 && !equalLabelSets(wantLabels, stored.labels) {
		changed = append(changed, "spec.labels")
		op.Labels = wantLabels
	}
	wantValues := mc.GetValues()
	if emptyValues(wantValues) && hasKept {
		wantValues = kept.values
	}
	if !emptyValues(wantValues) && !proto.Equal(wantValues, storedValues) {
		changed = append(changed, "spec.values")
		layer, err := machineLayerSpec(wantValues)
		if err != nil {
			return classifiedMachine{line: blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE,
				name, fmt.Sprintf("config.machines[%s]", name), "the entry's values cannot be encoded")}
		}
		op.Layer = layer
	}
	return classifiedMachine{
		line: &hubv1.ConfigChange{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE, Name: name,
			Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE, ChangedFields: changed,
		},
		machine: op, hasOp: true,
	}
}

// classifyPendingMachine plans an entry no enrolled machine has: create
// the kept entry, update it when it holds something else, unchanged
// when it already holds what the entry states. has reports whether a
// kept row exists.
func classifyPendingMachine(mc *hubv1.MachineConfig, kept pendingBaseline, has bool) classifiedMachine {
	name := mc.GetName()
	wantLabels := mc.GetLabels()
	if len(wantLabels) == 0 && has {
		wantLabels = kept.labels
	}
	wantValues := mc.GetValues()
	if emptyValues(wantValues) && has {
		wantValues = kept.values
	}
	if emptyValues(wantValues) {
		wantValues = &typev1.SettingValues{}
	}
	layer, err := machineLayerSpec(wantValues)
	if err != nil {
		return classifiedMachine{line: blockedImportLine(hubv1.ConfigEntity_CONFIG_ENTITY_MACHINE,
			name, fmt.Sprintf("config.machines[%s]", name), "the entry's values cannot be encoded")}
	}
	if !has {
		return classifiedMachine{
			line: &hubv1.ConfigChange{
				Entity: hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE, Name: name,
				Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_CREATE,
			},
			pending:   statedb.ImportPendingOp{Name: name, Labels: wantLabels, Layer: layer},
			isPending: true, hasOp: true,
		}
	}
	keptValues := kept.values
	if keptValues == nil {
		keptValues = &typev1.SettingValues{}
	}
	if equalLabelSets(wantLabels, kept.labels) && proto.Equal(wantValues, keptValues) {
		return classifiedMachine{line: &hubv1.ConfigChange{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE, Name: name,
			Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UNCHANGED,
		}}
	}
	var changed []string
	if !equalLabelSets(wantLabels, kept.labels) {
		changed = append(changed, "spec.labels")
	}
	if !proto.Equal(wantValues, keptValues) {
		changed = append(changed, "spec.values")
	}
	return classifiedMachine{
		line: &hubv1.ConfigChange{
			Entity: hubv1.ConfigEntity_CONFIG_ENTITY_PENDING_MACHINE, Name: name,
			Kind: hubv1.ConfigChangeKind_CONFIG_CHANGE_KIND_UPDATE, ChangedFields: changed,
		},
		pending:   statedb.ImportPendingOp{Name: name, Labels: wantLabels, Layer: layer},
		isPending: true, hasOp: true,
	}
}
