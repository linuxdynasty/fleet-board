package fleetcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

var errMalformed = errors.New("incomplete response")

func (i *invocation) validWorkKey(c *cobra.Command) bool {
	f := c.Flags()
	w := i.work
	if !f.Changed("kind") || !f.Changed("project") || !f.Changed("key") ||
		f.Changed("integration") == f.Changed("provider") {
		return false
	}
	if w.key == "" || (f.Changed("integration") && w.integration == "") ||
		(f.Changed("provider") && w.provider == "") {
		return false
	}
	for _, s := range []string{w.project, w.key, w.integration, w.provider} {
		if !validText(s, 1024) {
			return false
		}
	}
	_, ok := kindValue(w.kind)
	return ok
}

func kindValue(s string) (typev1.WorkItemKind, bool) {
	for _, name := range []string{"ISSUE", "TICKET", "CHANGE_REQUEST", "ROUTINE", "OTHER"} {
		if s == name {
			v, ok := typev1.WorkItemKind_value["WORK_ITEM_KIND_"+name]
			return typev1.WorkItemKind(v), ok
		}
	}
	return 0, false
}

func (i *invocation) read(ctx context.Context, c hubv1connect.FleetServiceClient, name string, show bool) ([]byte, error) {
	switch name {
	case "status":
		a, err := c.GetFleetSummary(ctx, connect.NewRequest(&hubv1.GetFleetSummaryRequest{}))
		if err != nil {
			return nil, err
		}
		b, err := c.GetSystemStatus(ctx, connect.NewRequest(&hubv1.GetSystemStatusRequest{}))
		if err != nil {
			return nil, err
		}
		if a.Msg.GetSummary() == nil || b.Msg.GetStatus() == nil {
			return nil, errMalformed
		}
		if i.json {
			return aggregate("summary", a.Msg, "system", b.Msg)
		}
		return plainStatus(a.Msg, b.Msg), nil
	case "doctor":
		return i.doctor(ctx, c)
	case "machines":
		if show {
			r, err := c.GetMachine(ctx, connect.NewRequest(&hubv1.GetMachineRequest{Id: &typev1.MachineId{Value: i.id}}))
			if err != nil {
				return nil, err
			}
			if r.Msg.GetMachine() == nil {
				return nil, errMalformed
			}
			if i.json {
				return messageJSON(r.Msg)
			}
			return machineTable([]*hubv1.Machine{r.Msg.GetMachine()}, ""), nil
		}
		r, err := c.ListMachines(ctx, connect.NewRequest(&hubv1.ListMachinesRequest{PageSize: int32(i.flags.size), PageToken: i.flags.token, Filter: i.flags.filter, OrderBy: i.flags.order}))
		if err != nil {
			return nil, err
		}
		if i.json {
			return messageJSON(r.Msg)
		}
		return machineTable(r.Msg.GetMachines(), r.Msg.GetNextPageToken()), nil
	case "accounts":
		if show {
			r, err := c.GetAccount(ctx, connect.NewRequest(&hubv1.GetAccountRequest{Id: &typev1.AccountId{Value: i.id}}))
			if err != nil {
				return nil, err
			}
			if r.Msg.GetAccount() == nil {
				return nil, errMalformed
			}
			if i.json {
				return messageJSON(r.Msg)
			}
			return accountTable([]*hubv1.Account{r.Msg.GetAccount()}, ""), nil
		}
		r, err := c.ListAccounts(ctx, connect.NewRequest(&hubv1.ListAccountsRequest{PageSize: int32(i.flags.size), PageToken: i.flags.token, Filter: i.flags.filter, OrderBy: i.flags.order}))
		if err != nil {
			return nil, err
		}
		if i.json {
			return messageJSON(r.Msg)
		}
		return accountTable(r.Msg.GetAccounts(), r.Msg.GetNextPageToken()), nil
	case "goals":
		if show {
			r, err := c.GetGoal(ctx, connect.NewRequest(&hubv1.GetGoalRequest{Id: &typev1.GoalId{Value: i.id}}))
			if err != nil {
				return nil, err
			}
			if r.Msg.GetGoal() == nil {
				return nil, errMalformed
			}
			if i.json {
				return messageJSON(r.Msg)
			}
			return goalTable([]*hubv1.Goal{r.Msg.GetGoal()}, ""), nil
		}
		r, err := c.ListGoals(ctx, connect.NewRequest(&hubv1.ListGoalsRequest{PageSize: int32(i.flags.size), PageToken: i.flags.token, Filter: i.flags.filter, OrderBy: i.flags.order}))
		if err != nil {
			return nil, err
		}
		if i.json {
			return messageJSON(r.Msg)
		}
		return goalTable(r.Msg.GetGoals(), r.Msg.GetNextPageToken()), nil
	case "work":
		if show {
			kind, _ := kindValue(i.work.kind)
			key := &typev1.WorkItemKey{Kind: kind, Project: i.work.project, Key: i.work.key, ProviderName: i.work.provider}
			if i.work.integration != "" {
				key.Integration = &typev1.IntegrationId{Value: i.work.integration}
			}
			r, err := c.GetWorkItem(ctx, connect.NewRequest(&hubv1.GetWorkItemRequest{Id: key}))
			if err != nil {
				return nil, err
			}
			if r.Msg.GetWorkItem() == nil {
				return nil, errMalformed
			}
			if i.json {
				return messageJSON(r.Msg)
			}
			return workTable([]*hubv1.WorkItem{r.Msg.GetWorkItem()}, ""), nil
		}
		r, err := c.ListWorkItems(ctx, connect.NewRequest(&hubv1.ListWorkItemsRequest{PageSize: int32(i.flags.size), PageToken: i.flags.token, Filter: i.flags.filter, OrderBy: i.flags.order}))
		if err != nil {
			return nil, err
		}
		if i.json {
			return messageJSON(r.Msg)
		}
		return workTable(r.Msg.GetWorkItems(), r.Msg.GetNextPageToken()), nil
	}
	return nil, errUsage
}

func messageJSON(m proto.Message) ([]byte, error) {
	b, err := (protojson.MarshalOptions{}).Marshal(m)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

func aggregate(an string, a proto.Message, bn string, b proto.Message) ([]byte, error) {
	x, err := (protojson.MarshalOptions{}).Marshal(a)
	if err != nil {
		return nil, err
	}
	y, err := (protojson.MarshalOptions{}).Marshal(b)
	if err != nil {
		return nil, err
	}
	return append([]byte(fmt.Sprintf("{%q:%s,%q:%s}", an, json.RawMessage(x), bn, json.RawMessage(y))), '\n'), nil
}

func plainStatus(a *hubv1.GetFleetSummaryResponse, b *hubv1.GetSystemStatusResponse) []byte {
	s := a.GetSummary()
	return []byte(fmt.Sprintf("Machines: %d/%d online\nGoals: %d working, %d waiting, %d stuck, %d limit-wait\nNeeds you: %d\nSystem: %s\n",
		s.GetMachinesOnline(), s.GetMachinesTotal(), s.GetGoalsWorking(), s.GetGoalsWaiting(), s.GetGoalsStuck(), s.GetGoalsLimitWait(),
		s.GetNeedsYou(), b.GetStatus().GetOverall().String()))
}

func safeCell(s string) string {
	if s == "" {
		return "-"
	}
	return strconvQuote(s)
}

func footer(token string) string {
	if token == "" {
		return ""
	}
	return "Next page token: " + strconvQuote(token) + "\n"
}

func rows(header string, lines []string, token string) []byte {
	var b strings.Builder
	b.WriteString(header)
	b.WriteByte('\n')
	if len(lines) == 0 {
		b.WriteString("No results.\n")
	}
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	b.WriteString(footer(token))
	return []byte(b.String())
}
