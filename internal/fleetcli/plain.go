package fleetcli

import (
	"fmt"
	"strconv"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

func strconvQuote(s string) string { return strconv.QuoteToASCII(s) }

func machineTable(items []*hubv1.Machine, token string) []byte {
	lines := make([]string, 0, len(items))
	for _, m := range items {
		if m == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s", safeCell(m.GetId().GetValue()),
			safeCell(m.GetSpec().GetDisplayName()), m.GetStatus().GetState().String()))
	}
	return rows("ID  NAME  STATE", lines, token)
}

func accountTable(items []*hubv1.Account, token string) []byte {
	lines := make([]string, 0, len(items))
	for _, a := range items {
		if a == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s  %s", safeCell(a.GetId().GetValue()),
			safeCell(a.GetSpec().GetDisplayName()), safeCell(a.GetStatus().GetHarness().GetName()),
			safeCell(a.GetStatus().GetProvider()), a.GetSpec().GetMode().String()))
	}
	return rows("ID  NAME  HARNESS  PROVIDER  MODE", lines, token)
}

func goalTable(items []*hubv1.Goal, token string) []byte {
	lines := make([]string, 0, len(items))
	for _, g := range items {
		if g == nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s", safeCell(g.GetId().GetValue()),
			safeCell(g.GetSpec().GetMachine().GetValue()), g.GetStatus().GetPhase().String(),
			safeCell(g.GetSpec().GetHarness().GetName())))
	}
	return rows("ID  MACHINE  PHASE  HARNESS", lines, token)
}

func workTable(items []*hubv1.WorkItem, token string) []byte {
	lines := make([]string, 0, len(items))
	for _, w := range items {
		if w == nil {
			continue
		}
		key := w.GetId()
		lines = append(lines, fmt.Sprintf("%s  %s  %s  %s  %s  %s  %s",
			safeCell(key.GetIntegration().GetValue()), safeCell(key.GetProviderName()),
			key.GetKind().String(), safeCell(key.GetProject()), safeCell(key.GetKey()),
			w.GetStage().String(), safeCell(w.GetItem().GetTitle())))
	}
	return rows("INTEGRATION  PROVIDER  KIND  PROJECT  KEY  STAGE  TITLE", lines, token)
}
