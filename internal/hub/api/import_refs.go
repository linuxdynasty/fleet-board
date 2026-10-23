package api

import (
	"fmt"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Portable reference resolution for FleetConfig documents (#397). The
// export carries names, not hub ids; import resolves them against the
// target hub before diffing, so the same document plans unchanged on the
// hub it came from and on any hub with matching integrations and machines.

// refTables resolves one document's portable refs to local hub ids:
// integrations by provider and base URL, machines by display name. Only
// an unambiguous enrolled name resolves; a pending name, an unknown name
// and a duplicated display name do not.
type refTables struct {
	integrations map[string]string
	machines     map[string]string
}

// newRefTables builds the resolution tables from an import baseline. A
// nil rest is an empty hub: nothing resolves.
func newRefTables(rest *configRest) *refTables {
	t := &refTables{
		integrations: map[string]string{},
		machines:     map[string]string{},
	}
	if rest == nil {
		return t
	}
	for key, id := range rest.integrationIDs {
		t.integrations[key] = id
	}
	for name, baselines := range rest.machines {
		if len(baselines) == 1 {
			t.machines[name] = baselines[0].id
		}
	}
	return t
}

// integrationRefKey is the document key of an integration ref: the same
// provider/base-URL join the baseline maps use.
func integrationRefKey(ref *hubv1.IntegrationRef) string {
	return ref.GetProvider() + "\x00" + ref.GetBaseUrl()
}

// integrationID resolves one document integration ref to the local id.
// An unset ref is not an error: the caller falls back to its own default.
func (t *refTables) integrationID(ref *hubv1.IntegrationRef) (string, bool) {
	if ref == nil {
		return "", false
	}
	id, ok := t.integrations[integrationRefKey(ref)]
	return id, ok
}

// machineID resolves one document machine display name to the local id.
func (t *refTables) machineID(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	id, ok := t.machines[name]
	return id, ok
}

// resolveIntegrationRef moves one document ref to its wrapper on the
// normalized clone, so the plan diffs local ids. A ref beside its wrapper
// is ambiguous and refused; an unknown ref names the missing integration.
func (t *refTables) resolveIntegrationRef(entity hubv1.ConfigEntity, name, field string,
	ref *hubv1.IntegrationRef, set func(string), hasWrapper bool,
) *hubv1.ConfigChange {
	if ref == nil {
		return nil
	}
	if hasWrapper {
		return blockedImportLine(entity, name, field,
			"the entry names both an integration id and a portable ref; documents carry refs only")
	}
	id, ok := t.integrationID(ref)
	if !ok {
		return invalidImportLine(entity, name, field,
			typev1.ReasonCode_REASON_CODE_RESOURCE_NOT_FOUND,
			"no integration matches "+ref.GetProvider()+" "+ref.GetBaseUrl()+"; connect it first")
	}
	set(id)
	return nil
}

// normalizeRepoRefs resolves a cloned repo spec's document refs to local
// ids: the code host (falling back to the host match when the doc carries
// no ref), the tracker projects, and the repo trusted gate. It returns
// the plan problem, or nil when the clone is ready to diff.
func (t *refTables) normalizeRepoRefs(field, name, host string, spec *hubv1.RepoSpec,
	codeHosts map[string]string,
) *hubv1.ConfigChange {
	entity := hubv1.ConfigEntity_CONFIG_ENTITY_REPO
	if spec.GetCodeHostRef() != nil {
		if p := t.resolveIntegrationRef(entity, name, field+".spec.code_host_ref",
			spec.GetCodeHostRef(),
			func(id string) {
				spec.Repo.CodeHost = &typev1.IntegrationId{Value: id}
			}, spec.GetRepo().GetCodeHost() != nil); p != nil {
			return p
		}
		spec.CodeHostRef = nil
	} else if codeHost, ok := codeHosts[host]; ok {
		spec.Repo.CodeHost = &typev1.IntegrationId{Value: codeHost}
	}
	for i, tp := range spec.GetTrackerProjects() {
		if tp.GetIntegrationRef() == nil {
			continue
		}
		idx := i
		if p := t.resolveIntegrationRef(entity, name,
			fmt.Sprintf("%s.spec.tracker_projects[%d].integration_ref", field, idx),
			tp.GetIntegrationRef(),
			func(id string) { tp.Integration = &typev1.IntegrationId{Value: id} },
			tp.GetIntegration() != nil); p != nil {
			return p
		}
		tp.IntegrationRef = nil
	}
	// A repo gate holds users only (its label is a plain string): the
	// fleet-level labels live on the hub gate, normalized separately.
	for i, u := range spec.GetTrustedGate().GetUsers() {
		if u.GetIntegrationRef() == nil {
			continue
		}
		idx := i
		if p := t.resolveIntegrationRef(entity, name,
			fmt.Sprintf("%s.spec.trusted_gate.users[%d].integration_ref", field, idx),
			u.GetIntegrationRef(),
			func(id string) { u.Integration = &typev1.IntegrationId{Value: id} },
			u.GetIntegration() != nil); p != nil {
			return p
		}
		u.IntegrationRef = nil
	}
	return nil
}

// normalizeAccountRefs resolves a cloned account spec's binding machine
// names to local ids. An unknown name blocks the line: the machine is not
// on this hub, so the binding cannot apply here.
func (t *refTables) normalizeAccountRefs(name, field string, spec *hubv1.AccountSpec) *hubv1.ConfigChange {
	entity := hubv1.ConfigEntity_CONFIG_ENTITY_ACCOUNT
	for i, b := range spec.GetBindings() {
		if b.GetMachineDisplayName() == "" {
			continue
		}
		if b.GetMachine() != nil {
			return blockedImportLine(entity, name,
				fmt.Sprintf("%s.spec.bindings[%d]", field, i),
				"the entry names both a machine id and a display name; documents carry names only")
		}
		id, ok := t.machineID(b.GetMachineDisplayName())
		if !ok {
			return blockedImportLine(entity, name,
				fmt.Sprintf("%s.spec.bindings[%d]", field, i),
				"no machine is enrolled as "+b.GetMachineDisplayName())
		}
		b.Machine = &typev1.MachineId{Value: id}
		b.MachineDisplayName = ""
	}
	return nil
}

// normalizeGateRefs resolves a cloned trusted gate's document refs to
// local ids, for the hub-settings compare.
func (t *refTables) normalizeGateRefs(field string, gate *hubv1.TrustedGate) *hubv1.ConfigChange {
	if gate == nil {
		return nil
	}
	entity := hubv1.ConfigEntity_CONFIG_ENTITY_HUB_SETTINGS
	for i, u := range gate.GetUsers() {
		if u.GetIntegrationRef() == nil {
			continue
		}
		idx := i
		if p := t.resolveIntegrationRef(entity, "hub",
			fmt.Sprintf("%s.spec.hub.trusted_gate.users[%d].integration_ref", field, idx),
			u.GetIntegrationRef(),
			func(id string) { u.Integration = &typev1.IntegrationId{Value: id} },
			u.GetIntegration() != nil); p != nil {
			return p
		}
		u.IntegrationRef = nil
	}
	for _, l := range gate.GetLabels() {
		if l.GetIntegrationRef() == nil {
			continue
		}
		if p := t.resolveIntegrationRef(entity, "hub",
			field+".spec.hub.trusted_gate.labels.integration_ref",
			l.GetIntegrationRef(),
			func(id string) { l.Integration = &typev1.IntegrationId{Value: id} },
			l.GetIntegration() != nil); p != nil {
			return p
		}
		l.IntegrationRef = nil
	}
	return nil
}
