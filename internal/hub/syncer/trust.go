package syncer

import (
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
)

// DefaultTrustedLabel is the label that marks an item dispatchable when no
// integration or repo names another (03j, defaults.md).
const DefaultTrustedLabel = "agent-ready"

// GateInput is everything Evaluate needs: the provider's evidence, the
// fleet and repo allow-lists, what the adapter can report, and now.
type GateInput struct {
	Item        *integrationv1.WorkItem
	Gate        *hubv1.TrustedGate
	RepoLabel   string
	RepoUsers   []*hubv1.TrustedUser
	Integration string
	// LabelsWithActor is IntegrationCapabilities.labels_with_actor: the
	// adapter reports who applied the trusted labels and when.
	LabelsWithActor bool
	// EditEvidence is IntegrationCapabilities.edit_evidence: the adapter
	// reports the last title or body edit and who made it.
	EditEvidence bool
	Now          time.Time
}

// Evaluate computes the trusted-issues gate (03j) for one mirror row. The
// adapter is read-only and never sees the allow-list: it returns evidence,
// the hub decides. Unknown actors and missing edit evidence fail closed.
//
// The label path is tried first, so an allow-listed re-label after an
// outsider edit restores trust; the author path is the fallback.
func Evaluate(in GateInput) *hubv1.TrustVerdict {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}
	deny := func(d hubv1.TrustDenial) *hubv1.TrustVerdict {
		return &hubv1.TrustVerdict{Trusted: false, Denial: d, EvaluatedTime: timestamppb.New(now)}
	}
	item := in.Item
	if item == nil {
		return deny(hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING)
	}
	// Without edit evidence no outsider edit can be ruled out, so nothing
	// passes (integration.proto: edit_evidence).
	if !in.EditEvidence {
		return deny(hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING)
	}
	label := effectiveLabel(in.Gate, in.RepoLabel, in.Integration)
	if !in.LabelsWithActor && labelPresent(item, label) {
		return deny(hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING)
	}
	if basis, granter, event, ok := labelPath(item, in, label); ok {
		if outsiderEdit(item, in, event) {
			return deny(hubv1.TrustDenial_TRUST_DENIAL_EDITED_AFTER_TRUST)
		}
		return &hubv1.TrustVerdict{
			Trusted: true, Basis: basis, GrantedBy: granter,
			TrustTime: event, EvaluatedTime: timestamppb.New(now),
		}
	} else if missing := labelMissing(item, label); missing == labelPresentUntrusted {
		return deny(hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED)
	} else if missing == labelPresentNoEvidence {
		return deny(hubv1.TrustDenial_TRUST_DENIAL_EVIDENCE_MISSING)
	}
	if granter, event, ok := authorPath(item, in); ok {
		if outsiderEdit(item, in, event) {
			return deny(hubv1.TrustDenial_TRUST_DENIAL_EDITED_AFTER_TRUST)
		}
		return &hubv1.TrustVerdict{
			Trusted: true, Basis: hubv1.TrustBasis_TRUST_BASIS_AUTHOR,
			GrantedBy: granter, TrustTime: event, EvaluatedTime: timestamppb.New(now),
		}
	}
	return deny(hubv1.TrustDenial_TRUST_DENIAL_NOT_ALLOW_LISTED)
}

// labelPresence says what a present trusted label contributes.
type labelPresence int

const (
	labelAbsent labelPresence = iota
	// Present and applied by someone outside the list.
	labelPresentUntrusted
	// Present but the actor or time is unknown, or the adapter cannot
	// report actors.
	labelPresentNoEvidence
)

// labelPath reports the label grant when the trusted label is present and
// its latest apply was by an allow-listed person.
func labelPath(item *integrationv1.WorkItem, in GateInput, label string) (hubv1.TrustBasis, *integrationv1.Person, *timestamppb.Timestamp, bool) {
	for _, l := range item.GetLabels() {
		if l == nil || !strings.EqualFold(l.GetName(), label) {
			continue
		}
		by := l.GetAppliedBy()
		at := l.GetAppliedTime()
		if !in.LabelsWithActor || by == nil || by.GetLogin() == "" || at == nil {
			return 0, nil, nil, false
		}
		if !allowListed(in, by) {
			return 0, nil, nil, false
		}
		return hubv1.TrustBasis_TRUST_BASIS_LABEL, by, at, true
	}
	return 0, nil, nil, false
}

// labelPresent reports whether the trusted label is on the item at all,
// whatever its evidence says.
func labelPresent(item *integrationv1.WorkItem, label string) bool {
	for _, l := range item.GetLabels() {
		if l != nil && strings.EqualFold(l.GetName(), label) {
			return true
		}
	}
	return false
}

// labelMissing classifies a present-but-unusable trusted label so the denial
// tells an outsider's label from missing evidence.
func labelMissing(item *integrationv1.WorkItem, label string) labelPresence {
	for _, l := range item.GetLabels() {
		if l == nil || !strings.EqualFold(l.GetName(), label) {
			continue
		}
		if l.GetAppliedBy() == nil || l.GetAppliedBy().GetLogin() == "" || l.GetAppliedTime() == nil {
			return labelPresentNoEvidence
		}
		return labelPresentUntrusted
	}
	return labelAbsent
}

// authorPath reports the author grant when the author is allow-listed.
func authorPath(item *integrationv1.WorkItem, in GateInput) (*integrationv1.Person, *timestamppb.Timestamp, bool) {
	author := item.GetAuthor()
	if author == nil || author.GetLogin() == "" || !allowListed(in, author) {
		return nil, nil, false
	}
	created := item.GetCreateTime()
	if created == nil {
		return nil, nil, false
	}
	return author, created, true
}

// outsiderEdit reports an edit by someone outside the list after the event
// that made the item trusted (03j: title or body edits, not comments).
func outsiderEdit(item *integrationv1.WorkItem, in GateInput, event *timestamppb.Timestamp) bool {
	editor := item.GetLastEditor()
	edited := item.GetLastEditTime()
	if editor == nil || editor.GetLogin() == "" || edited == nil || event == nil {
		return false
	}
	if !edited.AsTime().After(event.AsTime()) {
		return false
	}
	return !allowListed(in, editor)
}

// effectiveLabel is the repo's label, else the gate's label for the
// integration, else the fleet default (trusted_gate.proto).
func effectiveLabel(gate *hubv1.TrustedGate, repoLabel, integration string) string {
	if repoLabel != "" {
		return repoLabel
	}
	if gate != nil {
		fallback := ""
		for _, l := range gate.GetLabels() {
			if l == nil {
				continue
			}
			id := ""
			if l.GetIntegration() != nil {
				id = l.GetIntegration().GetValue()
			}
			if id == integration && l.GetName() != "" {
				return l.GetName()
			}
			if id == "" && l.GetName() != "" && fallback == "" {
				fallback = l.GetName()
			}
		}
		if fallback != "" {
			return fallback
		}
	}
	return DefaultTrustedLabel
}

// allowListed matches a provider person against the fleet and repo entries
// for this integration only. Logins compare case-insensitively; when the
// entry pins provider_user_id the id must match too, so a renamed or
// recycled login does not inherit trust (03j).
func allowListed(in GateInput, who *integrationv1.Person) bool {
	if who == nil {
		return false
	}
	match := func(u *hubv1.TrustedUser) bool {
		if u == nil {
			return false
		}
		id := ""
		if u.GetIntegration() != nil {
			id = u.GetIntegration().GetValue()
		}
		if id != in.Integration {
			return false
		}
		if !strings.EqualFold(u.GetLogin(), who.GetLogin()) {
			return false
		}
		if pin := u.GetProviderUserId(); pin != "" && pin != who.GetProviderId() {
			return false
		}
		return true
	}
	if in.Gate != nil {
		for _, u := range in.Gate.GetUsers() {
			if match(u) {
				return true
			}
		}
	}
	for _, u := range in.RepoUsers {
		if match(u) {
			return true
		}
	}
	return false
}

// InitialStage maps the verdict and the provider state to the hub's stage in
// the same upsert transaction (03m state machine): a disabled repo keeps
// items SEEN, a foreign claim parks them CLAIMED_ELSEWHERE, closed provider
// states close, trusted open items are READY and the rest wait MANUAL_ONLY
// with the reason the verdict names.
func InitialStage(
	state integrationv1.WorkItemState,
	verdict *hubv1.TrustVerdict,
	foreignClaim bool,
	repoEnabled bool,
) (hubv1.WorkStage, hubv1.WorkManualReason) {
	if state == integrationv1.WorkItemState_WORK_ITEM_STATE_CLOSED ||
		state == integrationv1.WorkItemState_WORK_ITEM_STATE_MERGED ||
		state == integrationv1.WorkItemState_WORK_ITEM_STATE_CANCELLED {
		return hubv1.WorkStage_WORK_STAGE_CLOSED, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED
	}
	if !repoEnabled {
		return hubv1.WorkStage_WORK_STAGE_SEEN, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED
	}
	if foreignClaim {
		return hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED
	}
	if verdict != nil && verdict.GetTrusted() {
		return hubv1.WorkStage_WORK_STAGE_READY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNSPECIFIED
	}
	if verdict != nil && verdict.GetDenial() == hubv1.TrustDenial_TRUST_DENIAL_EDITED_AFTER_TRUST {
		return hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_EDITED_AFTER_TRUST
	}
	return hubv1.WorkStage_WORK_STAGE_MANUAL_ONLY, hubv1.WorkManualReason_WORK_MANUAL_REASON_UNTRUSTED
}
