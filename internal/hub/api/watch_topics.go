package api

import (
	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// This file is the Watch topic table (02c, 02d, 02k row 12): which topics a
// stream can subscribe to, the scope each needs, and its filter catalogue.
// A topic the running build does not serve (ROUTINES until row 27, the v3
// DEVICES and NOTIFICATION_TARGETS) has no row: subscribing to it is
// INVALID_ARGUMENT naming the topic, never silently empty (02k).

// watchTopicDef is one servable Watch topic.
type watchTopicDef struct {
	topic hubv1.WatchTopic
	// scope is the authorizeScope argument: the topic's [SCOPE] tag.
	scope string
	// filtered is whether subscriptions take a filter. Approvals and
	// operations lists take none yet, and FLEET_SUMMARY has none (02c),
	// so those refuse any non-empty filter.
	filtered bool
	fields   map[string]filterField
	// latestWins marks the delivery classes the log never carries (02c):
	// a covered resume re-sends the current value as INITIAL.
	latestWins bool
	// initial reads the current resources for a send_initial
	// subscription. Nil means live only (operations have no List).
	initial watchInitial
}

// watchTopicOrder is the topic-number order of every served topic, so the
// default subscription is deterministic.
var watchTopicOrder = []hubv1.WatchTopic{
	hubv1.WatchTopic_WATCH_TOPIC_MACHINES,
	hubv1.WatchTopic_WATCH_TOPIC_SESSIONS,
	hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES,
	hubv1.WatchTopic_WATCH_TOPIC_GOALS,
	hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS,
	hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS,
	hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS,
	hubv1.WatchTopic_WATCH_TOPIC_TRIAGE,
	hubv1.WatchTopic_WATCH_TOPIC_APPROVALS,
	hubv1.WatchTopic_WATCH_TOPIC_SETTINGS,
	hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS,
	hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS,
	hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY,
	hubv1.WatchTopic_WATCH_TOPIC_AUDIT,
	hubv1.WatchTopic_WATCH_TOPIC_REPOS,
}

var watchTopics = map[hubv1.WatchTopic]*watchTopicDef{
	hubv1.WatchTopic_WATCH_TOPIC_MACHINES: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_MACHINES, scope: "READ",
		filtered: true, fields: machineFilterFields, initial: initialMachines,
	},
	hubv1.WatchTopic_WATCH_TOPIC_SESSIONS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_SESSIONS, scope: "READ",
		filtered: true, fields: sessionFilterFields, initial: initialSessions,
	},
	hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_PANE_SUMMARIES, scope: "READ",
		filtered: true, fields: paneFilterFields, latestWins: true, initial: initialPanes,
	},
	hubv1.WatchTopic_WATCH_TOPIC_GOALS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_GOALS, scope: "READ",
		filtered: true, fields: goalFilterFields, initial: initialGoals,
	},
	hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_WORK_ITEMS, scope: "READ",
		filtered: true, fields: workItemFilterFields, initial: initialWorkItems,
	},
	hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_CHANGE_REQUESTS, scope: "READ",
		filtered: true, fields: changeRequestFilterFields, initial: initialChangeRequests,
	},
	hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_ACCOUNTS, scope: "READ",
		filtered: true, fields: accountFilterFields, initial: initialAccounts,
	},
	hubv1.WatchTopic_WATCH_TOPIC_TRIAGE: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_TRIAGE, scope: "APPROVALS",
		filtered: true, fields: triageFilterFields, initial: initialTriage,
	},
	hubv1.WatchTopic_WATCH_TOPIC_APPROVALS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_APPROVALS, scope: "APPROVALS",
		filtered: false, initial: initialApprovals,
	},
	hubv1.WatchTopic_WATCH_TOPIC_SETTINGS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_SETTINGS, scope: "READ",
		filtered: true, fields: settingsFilterFields, initial: initialSettings,
	},
	hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_OPERATIONS, scope: "APPROVALS",
		filtered: false, initial: nil,
	},
	hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_INTEGRATIONS, scope: "READ",
		filtered: true, fields: integrationFilterFields, initial: initialIntegrations,
	},
	hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_FLEET_SUMMARY, scope: "APPROVALS",
		filtered: false, latestWins: true, initial: initialSummary,
	},
	hubv1.WatchTopic_WATCH_TOPIC_AUDIT: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_AUDIT, scope: "READ",
		filtered: true, fields: auditFilterFields, initial: initialAudit,
	},
	hubv1.WatchTopic_WATCH_TOPIC_REPOS: {
		topic: hubv1.WatchTopic_WATCH_TOPIC_REPOS, scope: "READ",
		filtered: true, fields: repoFilterFields, initial: initialRepos,
	},
}

// paneFilterFields is the 02c PaneSummary field table: panes filter by
// their session or machine, never by PaneKey.
var paneFilterFields = map[string]filterField{
	"session": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
	"machine": {kind: kindString, ops: map[filterOp]bool{opEq: true, opIn: true}},
}

// nodeMatchesPane evaluates a parsed pane filter against one summary.
func nodeMatchesPane(n *filterNode, p *hubv1.PaneSummary) bool {
	if n == nil {
		return true
	}
	switch n.op {
	case nodeAtom:
		return atomMatchesPane(n.atom, p)
	case nodeNot:
		return !nodeMatchesPane(n.kids[0], p)
	case nodeAnd:
		for _, k := range n.kids {
			if !nodeMatchesPane(k, p) {
				return false
			}
		}
		return true
	case nodeOr:
		for _, k := range n.kids {
			if nodeMatchesPane(k, p) {
				return true
			}
		}
		return false
	}
	return false
}

// atomMatchesPane evaluates one pane comparison. The parse already checked
// the field and the operator, so an unknown field here is unreachable and
// matches nothing.
func atomMatchesPane(a filterAtom, p *hubv1.PaneSummary) bool {
	switch a.field {
	case "session":
		return matchStrings(a.op, p.GetSession().GetValue(), a.strs)
	case "machine":
		return matchStrings(a.op, p.GetMachine().GetValue(), a.strs)
	}
	return false
}
