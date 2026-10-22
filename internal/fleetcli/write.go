package fleetcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1/hubv1connect"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// secretHeader carries the local approval secret on the SETTINGS and ADMIN
// writes in local_trusted mode, the same header doctor uses for its one
// protected read. Reads never carry it.
const secretHeader = "X-Fleet-Approval-Secret"

// writeFunc builds write requests with the terminal secret attached, calls
// exactly the RPCs of one command, and returns the bytes for stdout.
type writeFunc func(context.Context, hubv1connect.FleetServiceClient, string) ([]byte, error)

// executeWrite runs one owner-configuration command: it refuses a present
// token, opens the loopback client, asks once for the approval secret on the
// terminal, and only writes stdout after every RPC succeeded.
func (i *invocation) executeWrite(ctx context.Context, fn writeFunc) error {
	if _, exists := i.in.LookupEnv("FLEET_TOKEN"); exists {
		return errToken
	}
	rawURL, exists := i.in.LookupEnv("FLEET_URL")
	if !exists {
		rawURL = "http://127.0.0.1:7080"
	}
	client, closeClient, err := newClient(rawURL)
	if err != nil {
		return errUsage
	}
	defer closeClient()
	ctx, cancel := context.WithTimeout(ctx, i.networkBudget())
	defer cancel()
	if !isTerminal(i.in.Stdin) {
		return fmt.Errorf("%w: %w", errCommand, errNotTerminal)
	}
	secret, err := readSecret(ctx, i.in.Stdin, i.in.Stderr)
	if err != nil {
		return fmt.Errorf("%w: %w", errCommand, err)
	}
	defer clear(secret)
	data, err := fn(ctx, client, string(secret))
	if err != nil {
		return fmt.Errorf("%w: %w", errCommand, err)
	}
	n, err := i.in.Stdout.Write(data)
	if err != nil || n != len(data) {
		return fmt.Errorf("%w: %w", errCommand, errOutput)
	}
	return nil
}

// withSecret builds one request carrying the terminal secret. The secret
// travels only in this header, never in output or diagnostics.
func withSecret[M any](msg *M, secret string) *connect.Request[M] {
	req := connect.NewRequest(msg)
	req.Header().Set(secretHeader, secret)
	return req
}

// defaultRequestID mints a fresh idempotency key when the owner did not name
// one: 16 random bytes as hex, inside the server's printable-ASCII rule.
func defaultRequestID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func checkRequestID(id string) error {
	if id == "" || len(id) > 64 || strings.Contains(id, " ") {
		return errUsage
	}
	// The server takes printable ASCII without spaces (a UUID or ULID).
	for i := 0; i < len(id); i++ {
		if id[i] < 0x21 || id[i] > 0x7e {
			return errUsage
		}
	}
	return nil
}

// parseMask splits --update-mask into dotted spec paths. An empty entry is
// bad usage; unknown paths are the server's UPDATE_MASK_INVALID to report.
func parseMask(raw string) ([]string, error) {
	var paths []string
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" || !validText(p, 256) {
			return nil, errUsage
		}
		paths = append(paths, p)
	}
	if len(paths) == 0 {
		return nil, errUsage
	}
	return paths, nil
}

// unionMask merges the explicit --update-mask with the mask derived from the
// value flags the owner actually set. An empty union means nothing would
// change, which is bad usage, not a server round trip.
func unionMask(explicit string, derived []string) ([]string, error) {
	seen := make(map[string]bool)
	var paths []string
	if explicit != "" {
		parsed, err := parseMask(explicit)
		if err != nil {
			return nil, err
		}
		for _, p := range parsed {
			if !seen[p] {
				seen[p] = true
				paths = append(paths, p)
			}
		}
	}
	for _, p := range derived {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, errUsage
	}
	return paths, nil
}

// specFromJSON overlays --spec-json onto a spec message: proto JSON field
// names, the same shape --json prints. Unknown fields are refused.
func specFromJSON(raw string, spec proto.Message) error {
	if raw == "" {
		return nil
	}
	if !validText(raw, 1<<20) {
		return errUsage
	}
	if err := (protojson.UnmarshalOptions{}).Unmarshal([]byte(raw), spec); err != nil {
		return errUsage
	}
	return nil
}

// splitRepoID cuts HOST/PATH into its host and path halves.
func splitRepoID(id string) (string, string, error) {
	if !validText(id, 1024) {
		return "", "", errUsage
	}
	host, path, ok := strings.Cut(id, "/")
	if !ok || host == "" || path == "" {
		return "", "", errUsage
	}
	return host, path, nil
}

// writeJSON prints one write response as the Connect JSON of the message,
// the same rendering the read commands use.
func writeJSON(msg proto.Message) ([]byte, error) { return messageJSON(msg) }

// bindWriteFlags adds the flags every owner-configuration command shares. A
// nil target leaves that flag off the command.
func bindWriteFlags(fs *pflag.FlagSet, json *bool, requestID, updateMask *string, expectedVersion *uint64, yes *bool) {
	if json != nil {
		fs.BoolVar(json, "json", false, "print the full response as JSON")
	}
	if requestID != nil {
		fs.StringVar(requestID, "request-id", "", "idempotency key (a fresh one is minted when empty)")
	}
	if updateMask != nil {
		fs.StringVar(updateMask, "update-mask", "", "comma spec paths to write, added to the paths from value flags")
	}
	if expectedVersion != nil {
		fs.Uint64Var(expectedVersion, "expected-version", 0, "refuse the write when the version moved on")
	}
	if yes != nil {
		fs.BoolVar(yes, "yes", false, "confirm this destructive command")
	}
}

// writeRequestID returns the owner's key or a fresh one, checked against the
// server's printable-ASCII rule before any secret is read.
func writeRequestID(raw string) (string, error) {
	if raw == "" {
		id, err := defaultRequestID()
		if err != nil {
			return "", fmt.Errorf("%w: %w", errCommand, err)
		}
		return id, nil
	}
	if err := checkRequestID(raw); err != nil {
		return "", err
	}
	return raw, nil
}

// parseConsentActs reads a comma list like PUSH,PR_OPEN,MERGE. Full enum
// names work too; anything else is bad usage.
func parseConsentActs(raw string) ([]typev1.ConsentAct, error) {
	if raw == "" {
		return nil, errUsage
	}
	var acts []typev1.ConsentAct
	for _, name := range strings.Split(raw, ",") {
		name = strings.TrimSpace(strings.ToUpper(name))
		short, ok := map[string]string{
			"PUSH": "CONSENT_ACT_PUSH", "PR_OPEN": "CONSENT_ACT_PR_OPEN", "MERGE": "CONSENT_ACT_MERGE",
		}[name]
		if !ok {
			short = name
		}
		v, ok := typev1.ConsentAct_value[short]
		if !ok || short == "CONSENT_ACT_UNSPECIFIED" {
			return nil, errUsage
		}
		acts = append(acts, typev1.ConsentAct(v))
	}
	if len(acts) == 0 {
		return nil, errUsage
	}
	return acts, nil
}
