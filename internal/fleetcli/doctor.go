package fleetcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
)

var errUnsupportedMode = errors.New("unsupported hub authentication mode")

func (i *invocation) doctor(ctx context.Context, c hubv1connect.FleetServiceClient) ([]byte, error) {
	if !isTerminal(i.in.Stdin) {
		return nil, errNotTerminal
	}
	budget := i.networkBudget()
	started := time.Now()
	firstCtx, firstCancel := context.WithTimeout(ctx, budget)
	server, err := c.GetServerInfo(firstCtx, connect.NewRequest(&hubv1.GetServerInfoRequest{}))
	remaining := budget - time.Since(started)
	phaseErr := firstCtx.Err()
	firstCancel()
	if err != nil {
		return nil, err
	}
	if phaseErr != nil {
		return nil, phaseErr
	}
	if remaining <= 0 {
		return nil, context.DeadlineExceeded
	}
	if server.Msg.GetAuthMode() != hubv1.AuthMode_AUTH_MODE_LOCAL_TRUSTED {
		return nil, errUnsupportedMode
	}
	secret, err := readSecret(ctx, i.in.Stdin, i.in.Stderr)
	if err != nil {
		return nil, err
	}
	defer clear(secret)
	commandCtx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	system, err := c.GetSystemStatus(commandCtx, connect.NewRequest(&hubv1.GetSystemStatusRequest{}))
	if err != nil {
		return nil, err
	}
	storeRequest := connect.NewRequest(&hubv1.GetStoreStatusRequest{})
	storeRequest.Header().Set("X-Fleet-Approval-Secret", string(secret))
	store, err := c.GetStoreStatus(commandCtx, storeRequest)
	storeRequest.Header().Del("X-Fleet-Approval-Secret")
	if err != nil {
		return nil, err
	}
	if system.Msg.GetStatus() == nil || store.Msg.GetStatus() == nil {
		return nil, errMalformed
	}
	if i.json {
		a, err := (protojson.MarshalOptions{}).Marshal(system.Msg)
		if err != nil {
			return nil, err
		}
		b, err := (protojson.MarshalOptions{}).Marshal(store.Msg)
		if err != nil {
			return nil, err
		}
		d, err := (protojson.MarshalOptions{}).Marshal(server.Msg)
		if err != nil {
			return nil, err
		}
		return append([]byte(fmt.Sprintf("{\"system\":%s,\"store\":%s,\"server\":%s}",
			json.RawMessage(a), json.RawMessage(b), json.RawMessage(d))), '\n'), nil
	}
	return plainDoctor(system.Msg, store.Msg, server.Msg), nil
}

func plainDoctor(system *hubv1.GetSystemStatusResponse, store *hubv1.GetStoreStatusResponse,
	server *hubv1.GetServerInfoResponse,
) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "System: %s\nAuth: %s\nAPI version: %d\nHub version: %s\n",
		system.GetStatus().GetOverall(), server.GetAuthMode(), server.GetApiVersion(), safeCell(server.GetHubVersion()))
	for _, db := range store.GetStatus().GetDatabases() {
		fmt.Fprintf(&b, "Database: %s %d bytes schema %d\n", safeCell(db.GetName()), db.GetSizeBytes(), db.GetSchemaVersion())
	}
	fmt.Fprintf(&b, "Free disk: %d bytes\n", store.GetStatus().GetStateFreeDiskBytes())
	backup := store.GetStatus().GetBackup().GetLastBackupTime()
	if backup == nil {
		b.WriteString("Last backup: unknown\n")
	} else {
		fmt.Fprintf(&b, "Last backup: %s\n", backup.AsTime().UTC().Format(time.RFC3339))
	}
	return []byte(b.String())
}
