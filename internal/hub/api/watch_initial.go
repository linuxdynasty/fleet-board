package api

import (
	"context"
	"database/sql"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/hub/approvals"
	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
)

// This file is the Watch initial state (02c): every send_initial
// subscription first delivers every matching resource as INITIAL. Each
// reader reuses its List's rows, views, 02c matcher and proto assembly, so
// the initial state and the lists can never disagree.

// watchInitial reads the current resources of one subscription.
type watchInitial func(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error)

func initialMachines(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListMachineRows(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		if !nodeMatchesMachine(node, r) {
			continue
		}
		m, err := machineProto(r)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_MACHINES,
			Resource:   &hubv1.WatchResponse_Machine{Machine: m},
		})
	}
	return out, nil
}

func initialSessions(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListSessionRows(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		if !nodeMatchesSession(node, r) {
			continue
		}
		p, err := sessionProto(r)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_SESSIONS,
			Resource:   &hubv1.WatchResponse_Session{Session: p},
		})
	}
	return out, nil
}

// initialPanes re-sends the current summary of each matching pane as
// INITIAL: pane summaries live only on the bus, never in the store (02c).
func initialPanes(_ context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	var out []*hubv1.WatchResponse
	for _, e := range s.bus.latestPanes() {
		p := e.change.msg.GetPaneSummary()
		if p == nil || !nodeMatchesPane(node, p) {
			continue
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES,
			Resource:   &hubv1.WatchResponse_PaneSummary{PaneSummary: p},
		})
	}
	return out, nil
}

func initialGoals(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListGoalRows(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := goalViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesGoal(node, v) {
			continue
		}
		g, err := goalProto(v)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_GOALS,
			Resource:   &hubv1.WatchResponse_Goal{Goal: g},
		})
	}
	return out, nil
}

func initialWorkItems(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListWorkItemRows(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := workItemViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesWorkItem(node, v) {
			continue
		}
		w, err := workItemProto(v)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS,
			Resource:   &hubv1.WatchResponse_WorkItem{WorkItem: w},
		})
	}
	return out, nil
}

func initialChangeRequests(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListChangeRequestRows(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := changeRequestViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesChangeRequest(node, v) {
			continue
		}
		c, err := changeRequestProto(v)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS,
			Resource:   &hubv1.WatchResponse_ChangeRequest{ChangeRequest: c},
		})
	}
	return out, nil
}

func initialAccounts(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListStoredAccounts(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := accountViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesAccount(node, v) {
			continue
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS,
			Resource:   &hubv1.WatchResponse_Account{Account: accountFromStored(v.stored, v.spec)},
		})
	}
	return out, nil
}

func initialTriage(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListStoredTriageItems(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v := triageView{stored: r}
		if !nodeMatchesTriage(node, v) {
			continue
		}
		item, err := triageItem(v.stored)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_TRIAGE,
			Resource:   &hubv1.WatchResponse_TriageItem{TriageItem: item},
		})
	}
	return out, nil
}

// initialApprovals lists every approval, expiring the due ones in the same
// writer job exactly as ListApprovals does. Watch subscriptions take no
// filter; the node is always nil.
func initialApprovals(ctx context.Context, s *watchSession, _ *filterNode) ([]*hubv1.WatchResponse, error) {
	now := s.now()
	var list []*hubv1.Approval
	err := s.store.Write(ctx, func(tx *sql.Tx) error {
		if _, err := approvals.ExpireDue(ctx, tx, now, 200); err != nil {
			return err
		}
		filter := approvals.Filter{PageSize: 500}
		for {
			page, next, err := approvals.List(ctx, tx, filter, now)
			if err != nil {
				return err
			}
			list = append(list, page...)
			if next == "" {
				return nil
			}
			filter.PageToken = next
		}
	})
	if err != nil {
		return nil, err
	}
	out := make([]*hubv1.WatchResponse, 0, len(list))
	for _, a := range list {
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_APPROVALS,
			Resource:   &hubv1.WatchResponse_Approval{Approval: a},
		})
	}
	return out, nil
}

func initialSettings(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListSettingsLayers(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := settingsViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesSettings(node, v) {
			continue
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_SETTINGS,
			Resource:   &hubv1.WatchResponse_Settings{Settings: settingsFromLayer(v.stored, v.spec)},
		})
	}
	return out, nil
}

func initialIntegrations(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListStoredIntegrations(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := integrationViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesIntegration(node, v) {
			continue
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS,
			Resource:   &hubv1.WatchResponse_Integration{Integration: integrationFromStored(v.stored, v.spec, v.status)},
		})
	}
	return out, nil
}

// initialSummary serves the current strip as INITIAL: the summary is
// derived, so a resume re-computes it instead of replaying a log (02c).
func initialSummary(ctx context.Context, s *watchSession, _ *filterNode) ([]*hubv1.WatchResponse, error) {
	summary, err := s.summarize(ctx)
	if err != nil {
		return nil, err
	}
	return []*hubv1.WatchResponse{{
		ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
		Topic:      hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY,
		Resource:   &hubv1.WatchResponse_FleetSummary{FleetSummary: summary},
	}}, nil
}

func initialAudit(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListStoredAuditEntries(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v := auditView{stored: r}
		if !nodeMatchesAudit(node, v) {
			continue
		}
		entry, err := auditEntryOf(v.stored)
		if err != nil {
			return nil, err
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_AUDIT,
			Resource:   &hubv1.WatchResponse_AuditEntry{AuditEntry: entry},
		})
	}
	return out, nil
}

func initialRepos(ctx context.Context, s *watchSession, node *filterNode) ([]*hubv1.WatchResponse, error) {
	rows, err := statedb.ListWatchedRepoRows(ctx, s.store)
	if err != nil {
		return nil, err
	}
	var out []*hubv1.WatchResponse
	for _, r := range rows {
		v, err := repoViewOf(r)
		if err != nil {
			return nil, err
		}
		if !nodeMatchesRepo(node, v) {
			continue
		}
		out = append(out, &hubv1.WatchResponse{
			ChangeType: hubv1.ChangeType_CHANGE_TYPE_INITIAL,
			Topic:      hubv1.WatchTopic_WATCH_TOPIC_REPOS,
			Resource:   &hubv1.WatchResponse_Repo{Repo: repoFromStored(v.stored, v.spec)},
		})
	}
	return out, nil
}
