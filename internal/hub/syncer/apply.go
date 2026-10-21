package syncer

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	integrationv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/integration/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// workItemsRound polls one repo's issues and commits the page with its
// verdicts, cursors and budgets in one transaction (03m W4).
func (s *Syncer) workItemsRound(ctx context.Context, a Adapter, id string, repo statedb.WatchedRepo, full bool, now time.Time) error {
	ctx, span := s.tracer().Start(ctx, "hub.sync.work_items",
		trace.WithAttributes(attribute.String("integration", id), attribute.String("repo", repo.Path)))
	defer span.End()
	start := s.now()
	project := repo.Path
	cursor, err := statedb.GetCursor(ctx, s.Store, id, ResourceWorkItems, project)
	if err != nil {
		return err
	}
	var since time.Time
	if cursor.HasSince && !full {
		since = time.UnixMilli(cursor.SinceMs).UTC()
	}
	var validators *integrationv1.Validators
	if !full {
		validators = validatorsFor(cursor.ETag, cursor.LastModifiedMs, cursor.HasLastMod)
	}
	// A close is seen only when the poll after the cursor includes closed
	// items (service.proto: include_closed once a cursor exists).
	includeClosed := cursor.ETag != "" || cursor.HasSince
	page, err := a.ListWorkItems(ctx, project, since, validators, includeClosed)
	if err != nil && providerErrorOf(err).GetKind() == integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_CURSOR_EXPIRED && !full {
		// The validators died server-side: drop them and take the full
		// page once, which stores fresh ones (03j).
		s.log().InfoContext(ctx, "sync.cursor_reset", "integration", id, "resource", ResourceWorkItems)
		page, err = a.ListWorkItems(ctx, project, time.Time{}, nil, includeClosed)
	}
	if err != nil {
		return s.fail(ctx, id, project, ResourceWorkItems, err, now)
	}
	s.mergeLimits(id, page.Meta)
	s.checkSkew(ctx, id, page.Meta, now)
	if page.NotModified {
		s.storeLimits(ctx, id, ResourceWorkItems, project, cursor, page.Meta, now)
		s.ok(ctx, id, ResourceWorkItems, "not_modified", start, now)
		return nil
	}
	caps := a.Describe()
	items := page.Items
	if err := s.fillItemEvidence(ctx, a, id, project, items); err != nil {
		return s.fail(ctx, id, project, ResourceWorkItems, err, now)
	}
	stored, err := statedb.GetWorkItemStages(ctx, s.Store, itemKeys(id, items))
	if err != nil {
		return err
	}
	gate := s.repoGate(project)
	mirrors := make([]statedb.WorkItemMirror, 0, len(items))
	for _, item := range items {
		verdict := Evaluate(GateInput{
			Item: item, Gate: s.Gate,
			RepoLabel: repoGateLabel(gate), RepoUsers: repoGateUsers(gate),
			Integration:     id,
			LabelsWithActor: caps.GetLabelsWithActor(),
			EditEvidence:    caps.GetEditEvidence(),
			Now:             now,
		})
		stage, reason := InitialStage(item.GetState(), verdict, false, gate.Enabled && repo.Enabled)
		// Only the claim loop moves an item out of CLAIMED_ELSEWHERE.
		if key := item.GetRef().GetKey(); key != nil {
			if stored[statedb.SyncKey(id, key.GetKind().String(), project, key.GetKey())] ==
				int32(hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE) &&
				stage == hubv1.WorkStage_WORK_STAGE_READY {
				stage = hubv1.WorkStage_WORK_STAGE_CLAIMED_ELSEWHERE
			}
		}
		itemJSON, err := protojson.Marshal(item)
		if err != nil {
			return err
		}
		verdictJSON, err := protojson.Marshal(verdict)
		if err != nil {
			return err
		}
		key := item.GetRef().GetKey()
		mirrors = append(mirrors, statedb.WorkItemMirror{
			Integration: id, Kind: key.GetKind().String(), Project: project, ItemKey: key.GetKey(),
			ItemJSON: itemJSON, UpdateMs: itemTimeMs(item.GetUpdateTime(), now),
			Stage: int32(stage), TrustJSON: verdictJSON, ManualReason: int32(reason),
		})
	}
	nextSince := page.NextSince
	if nextSince == nil {
		nextSince = &integrationv1.ChangeCursor{}
	}
	cursorOut := statedb.CursorValue{
		Resource: ResourceWorkItems, Page: project,
		ETag: page.Validators.GetEtag(),
	}
	if lm := page.Validators.GetLastModifiedTime(); lm != nil {
		cursorOut.LastModifiedMs, cursorOut.HasLastMod = lm.AsTime().UnixMilli(), true
	}
	if ts := nextSince.GetUpdatedAfterTime(); ts != nil {
		cursorOut.SinceMs, cursorOut.HasSince = ts.AsTime().UnixMilli(), true
	}
	changes, err := statedb.UpsertWorkItemPage(ctx, s.Store, id, mirrors,
		[]statedb.CursorValue{cursorOut}, rateLimitValues(page.Meta), now.UnixMilli())
	if err != nil {
		return err
	}
	// The page committed: its mirror changes publish to Watch (the
	// Watch ingest slice).
	s.publishWorkItems(ctx, changes)
	s.ok(ctx, id, ResourceWorkItems, "ok", start, now)
	return nil
}

// fillItemEvidence fetches timeline evidence for the polled items that
// carry a trusted label, and merges editors and label applies into them
// (03j: the hub asks per labelled item, not per item).
func (s *Syncer) fillItemEvidence(ctx context.Context, a Adapter, id, project string, items []*integrationv1.WorkItem) error {
	labels := s.trustedLabels(id, project)
	if len(labels) == 0 {
		return nil
	}
	byNumber := map[int]*integrationv1.WorkItem{}
	var numbers []int
	for _, item := range items {
		n, err := strconv.Atoi(item.GetRef().GetKey().GetKey())
		if err != nil || !carriesLabel(item, labels) {
			continue
		}
		byNumber[n] = item
		numbers = append(numbers, n)
	}
	for len(numbers) > 0 {
		batch := numbers
		if len(batch) > githubBatchLimit {
			batch = batch[:githubBatchLimit]
		}
		numbers = numbers[len(batch):]
		got, _, err := a.FillEvidence(ctx, project, batch, labels)
		if err != nil {
			return err
		}
		for _, ev := range got {
			item := byNumber[ev.Number]
			if item == nil {
				continue
			}
			mergeEvidence(item, ev, labels)
		}
	}
	return nil
}

// githubBatchLimit is the evidence batch ceiling (service.proto: at most
// capabilities.max_batch_size; 50 for GitHub GraphQL).
const githubBatchLimit = 50

// mergeEvidence applies one issue's evidence onto its mirror row: the
// requested labels carry their latest apply, other labels stay names only,
// and the last edit lands where the gate reads it.
func mergeEvidence(item *integrationv1.WorkItem, ev Evidence, labels []string) {
	var keep []*integrationv1.Label
	for _, l := range item.GetLabels() {
		if l != nil && !labelNamed(l.GetName(), labels) {
			keep = append(keep, l)
		}
	}
	item.Labels = append(keep, ev.Labels...)
	if ev.Editor != nil {
		item.LastEditor = ev.Editor
	}
	if !ev.LastEditedAt.IsZero() {
		item.LastEditTime = timestamppb.New(ev.LastEditedAt)
	}
}

func labelNamed(name string, labels []string) bool {
	for _, w := range labels {
		if strings.EqualFold(name, w) {
			return true
		}
	}
	return false
}

func carriesLabel(item *integrationv1.WorkItem, labels []string) bool {
	for _, l := range item.GetLabels() {
		if l != nil && labelNamed(l.GetName(), labels) {
			return true
		}
	}
	return false
}

// trustedLabels are the labels that can grant trust here: the repo's own,
// else the fleet gate's labels for the integration (03j).
func (s *Syncer) trustedLabels(id, project string) []string {
	gate := s.repoGate(project)
	if gate.Label != "" {
		return []string{gate.Label}
	}
	var out []string
	seen := map[string]bool{}
	add := func(name string) {
		if name == "" || seen[strings.ToLower(name)] {
			return
		}
		seen[strings.ToLower(name)] = true
		out = append(out, name)
	}
	if s.Gate != nil {
		matched := false
		for _, l := range s.Gate.GetLabels() {
			if l == nil {
				continue
			}
			lid := ""
			if l.GetIntegration() != nil {
				lid = l.GetIntegration().GetValue()
			}
			if lid == id {
				add(l.GetName())
				matched = true
			}
		}
		if !matched {
			for _, l := range s.Gate.GetLabels() {
				if l == nil {
					continue
				}
				if l.GetIntegration() == nil || l.GetIntegration().GetValue() == "" {
					add(l.GetName())
				}
			}
		}
	}
	if len(out) == 0 {
		out = []string{DefaultTrustedLabel}
	}
	return out
}

func (s *Syncer) repoGate(project string) RepoGate {
	if s.RepoGate != nil {
		return s.RepoGate(project)
	}
	return RepoGate{Enabled: true}
}

func repoGateLabel(g RepoGate) string { return g.Label }

func repoGateUsers(g RepoGate) []*hubv1.TrustedUser { return g.Users }

func itemKeys(id string, items []*integrationv1.WorkItem) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if key := item.GetRef().GetKey(); key != nil {
			keys = append(keys, statedb.SyncKey(id, key.GetKind().String(), item.GetRef().GetKey().GetProject(), key.GetKey()))
		}
	}
	return keys
}

func itemTimeMs(ts *timestamppb.Timestamp, now time.Time) int64 {
	if ts != nil {
		return ts.AsTime().UnixMilli()
	}
	return now.UnixMilli()
}

// branchesRound polls the registered head branches (the running and
// verifying goals' branches, 03m) in batches of 50.
func (s *Syncer) branchesRound(ctx context.Context, a Adapter, id string, now time.Time) error {
	ctx, span := s.tracer().Start(ctx, "hub.sync.change_requests",
		trace.WithAttributes(attribute.String("integration", id)))
	defer span.End()
	s.mu.Lock()
	projects := map[string][]string{}
	for project, branches := range s.branches[id] {
		projects[project] = append([]string{}, branches...)
	}
	s.mu.Unlock()
	var errs []error
	for project, branches := range projects {
		for len(branches) > 0 {
			batch := branches
			if len(batch) > githubBatchLimit {
				batch = batch[:githubBatchLimit]
			}
			branches = branches[len(batch):]
			if _, err := s.VerifyBranches(ctx, id, project, batch, nil, ""); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// RegisterBranches adds head branches to the 60 s change-request poll
// (row 22 registers the running goals' branches).
func (s *Syncer) RegisterBranches(id, project string, branches []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.branches[id] == nil {
		s.branches[id] = map[string][]string{}
	}
	seen := map[string]bool{}
	for _, b := range s.branches[id][project] {
		seen[b] = true
	}
	for _, b := range branches {
		if !seen[b] {
			seen[b] = true
			s.branches[id][project] = append(s.branches[id][project], b)
		}
	}
}

// VerifyBranches reads change requests by head branch, head sha or linked
// item now (W6: at most one per 10 s per goal, enforced by the caller),
// upserts them, and returns the mapped rows.
func (s *Syncer) VerifyBranches(ctx context.Context, id, project string, branches, commits []string, linkedKey string) ([]*integrationv1.ChangeRequest, error) {
	a, ok := s.adapter(id)
	if !ok || a == nil {
		return nil, &CallError{Provider: &integrationv1.ProviderError{
			Kind:    integrationv1.ProviderErrorKind_PROVIDER_ERROR_KIND_UNSUPPORTED,
			Message: "no adapter for " + id,
		}}
	}
	now := s.now()
	out, err := a.ListChangeRequests(ctx, project, branches, commits, linkedKey)
	if err != nil {
		return nil, s.fail(ctx, id, project, ResourceChangeRequests, err, now)
	}
	s.mergeLimits(id, out.Meta)
	mirrors := make([]statedb.ChangeRequestMirror, 0, len(out.ChangeRequests))
	for _, cr := range out.ChangeRequests {
		body, err := protojson.Marshal(cr)
		if err != nil {
			return nil, err
		}
		mirrors = append(mirrors, statedb.ChangeRequestMirror{
			Integration: id, Project: project, ItemKey: cr.GetRef().GetKey().GetKey(),
			CRJSON: body, UpdateMs: itemTimeMs(cr.GetUpdateTime(), now),
		})
	}
	cursorOut := statedb.CursorValue{Resource: ResourceChangeRequests, Page: project}
	changes, err := statedb.UpsertChangeRequestPage(ctx, s.Store, id, mirrors,
		[]statedb.CursorValue{cursorOut}, rateLimitValues(out.Meta), now.UnixMilli())
	if err != nil {
		return nil, err
	}
	// The page committed: its mirror changes publish to Watch (the
	// Watch ingest slice).
	s.publishChangeRequests(ctx, changes)
	if s.Metrics != nil {
		s.Metrics.requests.Add(ctx, 1, metricWith("integration", id, "resource", ResourceChangeRequests, "status", "ok"))
	}
	st := s.stateFor(id)
	s.mu.Lock()
	st.lastComplete[ResourceChangeRequests] = now
	s.mu.Unlock()
	return out.ChangeRequests, nil
}
