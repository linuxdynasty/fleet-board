package conn

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// HubAddressesFile holds the hub's latest address list
// (HelloAck.hub_addresses), one host:port per line, at most 16. At start
// it replaces the enroll-time list in identity.json, which is never
// rewritten; enroll removes it.
const HubAddressesFile = "hub-addresses"

// loadHubAddresses reads HubAddressesFile: blank lines are skipped, and
// invalid lines are dropped and counted as hubAddresses drops them. A
// missing file is fs.ErrNotExist.
func loadHubAddresses(dir string) (addrs []string, dropped int, err error) {
	data, err := readPrivate(filepath.Join(dir, HubAddressesFile))
	if err != nil {
		return nil, 0, err
	}
	var lines []string
	for line := range strings.Lines(string(data)) {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	addrs = hubAddresses(lines)
	return addrs, len(lines) - len(addrs), nil
}

func saveHubAddresses(dir string, addrs []string) error {
	return writePrivate(dir, HubAddressesFile, []byte(strings.Join(addrs, "\n")+"\n"), true)
}

// configured is the address list a run starts with: the hub's latest in
// HubAddressesFile when it holds at least one valid line, else the one in
// identity.json. Either way each address is only a candidate: it still has
// to pass the pinned-CA handshake.
func (l *Loop) configured(ctx context.Context) []string {
	id := hubAddresses(l.opts.Identity.HubAddresses)
	addrs, dropped, err := loadHubAddresses(l.opts.Dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		l.log.InfoContext(ctx, "connect.addresses", "source", "identity", "count", len(id))
		return id
	case err != nil:
		l.log.WarnContext(ctx, "connect.addresses", "source", "identity", "count", len(id), "err_kind", errKind(err))
		return id
	case len(addrs) == 0:
		l.log.WarnContext(ctx, "connect.addresses", "source", "identity", "count", len(id), "dropped", dropped,
			"err_kind", "parse")
		return id
	}
	l.log.InfoContext(ctx, "connect.addresses", "source", HubAddressesFile, "count", len(addrs), "dropped", dropped)
	return addrs
}

// takeAddresses applies HelloAck.hub_addresses: an empty or invalid list
// changes nothing; one that differs from the list in use is saved, then
// used. A failed save leaves the stream up and the old list in use, so the
// next HelloAck with that list tries the save again.
func (l *Loop) takeAddresses(ctx context.Context, peer string, list []string) {
	addrs := hubAddresses(list)
	if len(addrs) == 0 || slices.Equal(addrs, l.addrs) {
		return
	}
	if err := saveHubAddresses(l.opts.Dir, addrs); err != nil {
		l.log.WarnContext(ctx, "connect.failed", "peer", peer, "step", "save", "err_kind", errKind(err))
		return
	}
	l.addrs = addrs
	l.log.InfoContext(ctx, "connect.addresses_saved", "peer", peer, "count", len(addrs))
}
