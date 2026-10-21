package fleetcli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

type machineAddFlags struct {
	json             bool
	requestID        string
	name             string
	ttl              string
	expectedHostname string
	labels           string
	startDrained     bool
	previousMachine  string
}

type machineFlags struct {
	json            bool
	requestID       string
	expectedVersion uint64
	yes             bool
	displayName     string
	labels          string
	reason          string
}

func (i *invocation) addMachineCommands(root *cobra.Command) {
	parent := &cobra.Command{Use: "machine", Short: "Add and retire machines", Args: cobra.NoArgs}
	var af machineAddFlags
	add := &cobra.Command{Use: "add", Short: "Mint an enrollment token", Args: cobra.NoArgs, RunE: func(c *cobra.Command, _ []string) error {
		return i.machineAdd(c, &af)
	}}
	bindWriteFlags(add.Flags(), &af.json, &af.requestID, nil, nil, nil)
	add.Flags().StringVar(&af.name, "name", "", "display name the machine enrolls with (required)")
	add.Flags().StringVar(&af.ttl, "ttl", "", "token life, like 15m (default 15m, at most 24h)")
	add.Flags().StringVar(&af.expectedHostname, "expected-hostname", "", "hostname the machine must report")
	add.Flags().StringVar(&af.labels, "labels", "", "comma hub labels the machine starts with")
	add.Flags().BoolVar(&af.startDrained, "start-drained", false, "the machine takes no goal until undrained")
	add.Flags().StringVar(&af.previousMachine, "previous-machine", "", "re-enroll id of a revoked machine")
	var nf, lf, rf, ff machineFlags
	rename := &cobra.Command{Use: "rename ID", Short: "Rename a machine", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.machineRename(c, &nf, argv[0])
	}}
	label := &cobra.Command{Use: "label ID", Short: "Set a machine's hub labels", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.machineLabel(c, &lf, argv[0])
	}}
	revoke := &cobra.Command{Use: "revoke ID", Short: "Cut a machine off now", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.machineRevoke(c, &rf, argv[0])
	}}
	forget := &cobra.Command{Use: "forget ID", Short: "Forget a revoked machine", Args: cobra.ExactArgs(1), RunE: func(c *cobra.Command, argv []string) error {
		return i.machineForget(c, &ff, argv[0])
	}}
	bindWriteFlags(rename.Flags(), &nf.json, &nf.requestID, nil, &nf.expectedVersion, nil)
	rename.Flags().StringVar(&nf.displayName, "display-name", "", "new owner-chosen name (required)")
	bindWriteFlags(label.Flags(), &lf.json, &lf.requestID, nil, &lf.expectedVersion, nil)
	label.Flags().StringVar(&lf.labels, "labels", "", "comma hub labels, replacing the old set (required)")
	bindWriteFlags(revoke.Flags(), &rf.json, &rf.requestID, nil, &rf.expectedVersion, &rf.yes)
	revoke.Flags().StringVar(&rf.reason, "reason", "", "audit-log reason, up to 512 bytes")
	bindWriteFlags(forget.Flags(), &ff.json, &ff.requestID, nil, &ff.expectedVersion, &ff.yes)
	parent.AddCommand(add, rename, label, revoke, forget)
	root.AddCommand(parent)
}

// splitLabels reads --labels as a comma list. An explicitly empty list
// clears the labels; every label is capped like other owner text.
func splitLabels(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var labels []string
	for _, l := range strings.Split(raw, ",") {
		l = strings.TrimSpace(l)
		if l == "" || !validText(l, 256) {
			return nil, errUsage
		}
		labels = append(labels, l)
	}
	return labels, nil
}

func (i *invocation) machineAdd(c *cobra.Command, f *machineAddFlags) error {
	if f.name == "" || !validText(f.name, 256) {
		return errUsage
	}
	labels, err := splitLabels(f.labels)
	if err != nil {
		return err
	}
	var ttl *durationpb.Duration
	if c.Flags().Changed("ttl") {
		ttl, err = parseDurationFlag(f.ttl)
		if err != nil {
			return err
		}
	}
	for _, s := range []string{f.name, f.ttl, f.expectedHostname, f.labels, f.previousMachine} {
		if !validText(s, 1024) {
			return errUsage
		}
	}
	if f.previousMachine != "" && !validText(f.previousMachine, 1024) {
		return errUsage
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	req := &hubv1.CreateEnrollmentTokenRequest{
		RequestId: requestID, MachineName: f.name, Ttl: ttl,
		ExpectedHostname: f.expectedHostname, Labels: labels, StartDrained: f.startDrained,
	}
	if f.previousMachine != "" {
		req.PreviousMachine = &typev1.MachineId{Value: f.previousMachine}
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.CreateEnrollmentToken(ctx, withSecret(req, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetToken() == "" || res.Msg.GetEnrollCommand() == "" {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		// The token is shown once, inside the ready-to-copy enroll line.
		out := res.Msg.GetEnrollCommand() + "\n"
		if res.Msg.GetExpireTime() != nil {
			out += "Expires: " + res.Msg.GetExpireTime().AsTime().UTC().Format("2006-01-02T15:04:05Z") + "\n"
		}
		return []byte(out), nil
	})
}

func (i *invocation) machineRename(c *cobra.Command, f *machineFlags, id string) error {
	if id == "" || !validText(id, 1024) {
		return errUsage
	}
	if !c.Flags().Changed("display-name") || f.displayName == "" || !validText(f.displayName, 256) {
		return errUsage
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.UpdateMachine(ctx, withSecret(&hubv1.UpdateMachineRequest{
			RequestId: requestID,
			Machine: &hubv1.Machine{
				Id:   &typev1.MachineId{Value: id},
				Spec: &hubv1.MachineSpec{DisplayName: f.displayName},
			},
			UpdateMask:      &fieldmaskpb.FieldMask{Paths: []string{"spec.display_name"}},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetMachine() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return machineLine(res.Msg.GetMachine())
	})
}

func (i *invocation) machineLabel(c *cobra.Command, f *machineFlags, id string) error {
	if id == "" || !validText(id, 1024) {
		return errUsage
	}
	if !c.Flags().Changed("labels") {
		return errUsage
	}
	labels, err := splitLabels(f.labels)
	if err != nil {
		return err
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.UpdateMachine(ctx, withSecret(&hubv1.UpdateMachineRequest{
			RequestId: requestID,
			Machine: &hubv1.Machine{
				Id:   &typev1.MachineId{Value: id},
				Spec: &hubv1.MachineSpec{Labels: labels},
			},
			UpdateMask:      &fieldmaskpb.FieldMask{Paths: []string{"spec.labels"}},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetMachine() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return machineLine(res.Msg.GetMachine())
	})
}

func (i *invocation) machineRevoke(c *cobra.Command, f *machineFlags, id string) error {
	if !f.yes {
		return errUsage
	}
	if id == "" || !validText(id, 1024) {
		return errUsage
	}
	if !validText(f.reason, 512) {
		return errUsage
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		res, err := client.RevokeMachine(ctx, withSecret(&hubv1.RevokeMachineRequest{
			RequestId:       requestID,
			Id:              &typev1.MachineId{Value: id},
			ExpectedVersion: f.expectedVersion,
			Reason:          f.reason,
		}, secret))
		if err != nil {
			return nil, err
		}
		if res.Msg.GetMachine() == nil {
			return nil, errMalformed
		}
		if f.json {
			return writeJSON(res.Msg)
		}
		return machineLine(res.Msg.GetMachine())
	})
}

func (i *invocation) machineForget(c *cobra.Command, f *machineFlags, id string) error {
	if !f.yes {
		return errUsage
	}
	if id == "" || !validText(id, 1024) {
		return errUsage
	}
	requestID, err := writeRequestID(f.requestID)
	if err != nil {
		return err
	}
	return i.executeWrite(c.Context(), func(ctx context.Context, client hubv1connect.FleetServiceClient, secret string) ([]byte, error) {
		_, err := client.DeleteMachine(ctx, withSecret(&hubv1.DeleteMachineRequest{
			RequestId:       requestID,
			Id:              &typev1.MachineId{Value: id},
			ExpectedVersion: f.expectedVersion,
		}, secret))
		if err != nil {
			return nil, err
		}
		if f.json {
			return []byte("{}\n"), nil
		}
		return []byte(fmt.Sprintf("forgot machine %s\n", id)), nil
	})
}

func machineLine(m *hubv1.Machine) ([]byte, error) {
	if m == nil {
		return nil, errMalformed
	}
	return []byte(fmt.Sprintf("machine %s version %d state %s\n",
		m.GetId().GetValue(), m.GetVersion(), m.GetStatus().GetState().String())), nil
}
