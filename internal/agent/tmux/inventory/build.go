package inventory

import (
	"slices"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/agent/tmux"
)

// serverKinds maps a server's socket name (PaneKey.server) to its kind.
var serverKinds = map[string]typev1.TmuxServerKind{
	"default":     typev1.TmuxServerKind_TMUX_SERVER_KIND_DEFAULT,
	"fleet":       typev1.TmuxServerKind_TMUX_SERVER_KIND_FLEET,
	"fleet-probe": typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE,
}

// buildServer turns one read into the TmuxServer the agent reports: running
// and observed, every session with its windows and panes, text bounded and
// tags checked. Sessions are in id order, windows by index, panes by index.
// What row 7 does not know stays unset (harness, activity, workdir, goal)
// or false (ready pattern, idle, available: row-7 ruling fork 9).
func buildServer(name string, inv tmux.Inventory, version string) *agentv1.TmuxServer {
	kind := serverKinds[name]
	srv := &agentv1.TmuxServer{
		Kind: kind, SocketName: name, ServerStartId: inv.StartID,
		Version: cleanText(version, maxNameBytes), Running: true, Observed: true,
	}
	byID := map[tmux.SessionID]*agentv1.TmuxSession{}
	for _, s := range inv.Sessions {
		ts := buildSession(s, kind == typev1.TmuxServerKind_TMUX_SERVER_KIND_PROBE)
		byID[s.ID] = ts
		srv.Sessions = append(srv.Sessions, ts)
	}
	windows := map[*agentv1.TmuxSession]map[tmux.WindowID]*agentv1.TmuxWindow{}
	for _, p := range inv.Panes {
		ts := byID[p.Session]
		if ts == nil {
			continue // not in the read's session list: the next read has it
		}
		if windows[ts] == nil {
			windows[ts] = map[tmux.WindowID]*agentv1.TmuxWindow{}
		}
		w := windows[ts][p.Window]
		if w == nil {
			w = &agentv1.TmuxWindow{
				Key:   &typev1.WindowKey{Server: name, ServerStartId: inv.StartID, WindowId: p.Window.String()},
				Index: uint32(max(p.WindowIndex, 0)), Name: cleanText(p.WindowName, maxNameBytes), //nolint:gosec // clamped at 0
				AutomaticRename: p.AutoRename, Active: p.WindowActive,
			}
			windows[ts][p.Window] = w
			ts.Windows = append(ts.Windows, w)
		}
		w.Panes = append(w.Panes, buildPane(name, inv.StartID, w.GetKey(), p))
	}
	for _, ts := range srv.GetSessions() {
		slices.SortStableFunc(ts.GetWindows(), func(a, b *agentv1.TmuxWindow) int { return int(a.GetIndex()) - int(b.GetIndex()) })
		for _, w := range ts.GetWindows() {
			slices.SortStableFunc(w.GetPanes(), func(a, b *agentv1.TmuxPane) int { return int(a.GetIndex()) - int(b.GetIndex()) })
		}
	}
	return srv
}

func buildSession(s tmux.SessionInfo, probe bool) *agentv1.TmuxSession {
	ts := &agentv1.TmuxSession{
		TmuxId: s.ID.String(), Name: cleanText(s.Name, maxNameBytes),
		AttachedClients: uint32(max(s.Clients, 0)), //nolint:gosec // clamped at 0
		// Internal by server or by the creator's mark, never by grouping
		// or a name (row-7 ruling fork 6).
		Internal:   probe || s.Tags.Internal != "",
		AttachView: s.Tags.Internal == attachViewMark,
	}
	if !s.Created.IsZero() {
		ts.CreateTime = timestamppb.New(s.Created)
	}
	if validID(s.Tags.SessionID) {
		ts.SessionId = &typev1.SessionId{Value: s.Tags.SessionID}
		ts.StartedByFleet = true
	}
	if validPrincipal(s.Tags.Owner) {
		ts.OwnerPrincipal = &typev1.Principal{Id: &typev1.PrincipalId{Value: s.Tags.Owner}}
	}
	return ts
}

func buildPane(server, start string, window *typev1.WindowKey, p tmux.PaneInfo) *agentv1.TmuxPane {
	tp := &agentv1.TmuxPane{
		Key:    &typev1.PaneKey{Server: server, ServerStartId: start, PaneId: p.ID.String()},
		Window: window, Index: uint32(max(p.Index, 0)), Active: p.Active, //nolint:gosec // clamped at 0
		CurrentCommand: cleanText(p.Command, maxNameBytes),
		Dead:           p.Dead,
		Cols:           uint32(max(p.Width, 0)),  //nolint:gosec // clamped at 0
		Rows:           uint32(max(p.Height, 0)), //nolint:gosec // clamped at 0
		Availability:   &typev1.Availability{},   // not idle, not available: row 8 and 18 own them (fork 9)
	}
	if p.Dead && p.DeadStatus != tmux.NoStatus {
		st := int32(p.DeadStatus) //nolint:gosec // an exit status fits
		tp.ExitStatus = &st
	}
	tags := map[string]string{}
	add := func(k, v string, ok func(string) bool) {
		if ok(v) {
			tags[k] = v
		}
	}
	add(tagSessionID, p.Tags.SessionID, validID)
	add(tagOwner, p.Tags.Owner, validPrincipal)
	add(tagPaneKey, p.Tags.PaneKey, validPaneKey)
	add(tagGoalID, p.Tags.GoalID, validID)
	if len(tags) > 0 {
		tp.FleetTags = tags
	}
	tp.StartedByFleet = tags[tagPaneKey] != ""
	return tp
}
