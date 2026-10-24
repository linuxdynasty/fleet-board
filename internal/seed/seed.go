package seed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/hub/statedb"
	"github.com/linuxdynasty/fleet-board/internal/store"
)

// Version is the agent wire version this seed was built against, pinned as
// a literal so a wire change fails [TestVersionFollowsTheWire] until the
// seed is rebuilt. An alias of protoversion.Max would move with the wire
// and never fire.
const Version = 1

// FleetID is the hub_identity fleet_id [Seed] writes.
const FleetID = "seed-fleet"

// MachineLaptop is the connected machine; MachineServer never connected.
const (
	MachineLaptop = "01jseedlap00000000000001"
	MachineServer = "01jseedsrv00000000000002"
)

// LaptopFacts is the MachineFacts JSON the laptop's snapshot stores.
const LaptopFacts = `{"hostname":"laptop.lan","os":"darwin"}`

// seedNow is the hub clock [Seed] stamps every row with, so two seeds hold
// the same times.
var seedNow = time.UnixMilli(1_790_001_000_000)

const (
	laptopInstall = "seed-install-1"
	serverInstall = "seed-install-2"
	laptopSerial  = "5eed01"
	serverSerial  = "5eed02"
)

// Seed builds a fresh state folder at dir: the hub identity, two enrolled
// machines, and the laptop's Hello and Snapshot so it reads ONLINE while
// the server stays ENROLLED. dir must not hold a state.db yet; a second
// Seed fails instead of replacing the fleet every test shares.
func Seed(ctx context.Context, dir string) error {
	if err := statedb.EnsureDir(dir); err != nil {
		return fmt.Errorf("seed: state folder: %w", err)
	}
	id := statedb.Identity{
		FleetID: FleetID, CACert: []byte("seed-ca-cert"), CAKey: []byte("seed-ca-key"),
		ServerCert: []byte("seed-server-cert"), ServerKey: []byte("seed-server-key"),
		AccountSalt: bytes32(7), SaltVersion: 1, SettingsEpoch: "seed-settings-epoch",
		CreatedTime: seedNow,
	}
	if err := statedb.Create(ctx, dir, id, statedb.CreateOptions{}); err != nil {
		return fmt.Errorf("seed: create state.db: %w", err)
	}
	st, err := statedb.Open(ctx, dir, nil)
	if err != nil {
		return fmt.Errorf("seed: open state.db: %w", err)
	}
	defer func() { _ = st.Close() }()
	if err := SeedRows(ctx, st); err != nil {
		return err
	}
	return nil
}

// SeedRows writes the shared fleet rows into an already-created store: two
// enrolled machines, and the laptop's Hello and Snapshot so it reads ONLINE
// while the server stays ENROLLED. A real hub needs a real CA identity, so
// the dashboard smoke runs `fleetd init` first and calls this for the rows;
// the fake seed identity above is for contract tests only. The rows are
// identical either way: same machines, same fixed clock.
func SeedRows(ctx context.Context, st *store.Store) error {
	// Fixed order: map iteration would hand the earlier hub_seq and audit
	// rows to whichever machine Go visits first.
	ordered := []struct {
		name string
		m    machine
	}{{"laptop", laptopMachine()}, {"server", serverMachine()}}
	for _, o := range ordered {
		if err := statedb.InsertEnrollToken(ctx, st, o.m.token); err != nil {
			return fmt.Errorf("seed: insert %s token: %w", o.name, err)
		}
		if _, err := statedb.Enroll(ctx, st, o.m.new, nil); err != nil {
			return fmt.Errorf("seed: enroll %s: %w", o.name, err)
		}
	}
	if _, err := statedb.Hello(ctx, st, statedb.HelloIn{
		MachineID: MachineLaptop, PeerSerial: laptopSerial, LastEpoch: 0, BootID: "seed-boot-1",
		InstallID: laptopInstall, Peer: "192.0.2.7:51000", Reason: statedb.ConnectStart,
		Now: seedNow.Add(time.Minute),
	}); err != nil {
		return fmt.Errorf("seed: laptop hello: %w", err)
	}
	at := seedNow.Add(2 * time.Minute)
	err := st.Write(ctx, func(tx *sql.Tx) error {
		if _, err := statedb.ApplySnapshot(ctx, tx, statedb.SnapshotIn{
			MachineID: MachineLaptop, Epoch: 1, Seq: 1, InstallID: laptopInstall,
			Facts: LaptopFacts, Resources: `{"load1":0.5}`,
			Policy: statedb.Policy{Hash: "seed1", SchemaVersion: "1"},
			Now:    at,
		}); err != nil {
			return fmt.Errorf("seed: laptop snapshot: %w", err)
		}
		_, _, err := statedb.MarkOnline(ctx, tx, MachineLaptop, at)
		return err
	})
	if err != nil {
		return fmt.Errorf("seed: laptop snapshot: %w", err)
	}
	return nil
}

func bytes32(b byte) []byte {
	out := make([]byte, 32)
	for i := range out {
		out[i] = b
	}
	return out
}

type machine struct {
	token statedb.EnrollToken
	new   statedb.NewMachine
}

func laptopMachine() machine {
	secret := sha256.Sum256([]byte{11, 1, 2, 3})
	return machine{
		token: statedb.EnrollToken{
			SecretHash: secret, MachineName: "laptop", ExpectedHostname: "laptop.lan",
			Labels: []string{"laptop"}, NotAfter: seedNow.Add(90 * 24 * time.Hour),
			Created: seedNow, Principal: "local:seed", RequestID: "seed-req-1",
			RequestHash: sha256.Sum256([]byte{11}),
		},
		new: statedb.NewMachine{
			Key:              statedb.EnrollKey{SecretHash: secret, InstallID: laptopInstall, CSRKeyHash: sha256.Sum256([]byte{21})},
			MachineID:        MachineLaptop,
			CertDER:          []byte("seed-cert-" + MachineLaptop),
			Serial:           laptopSerial,
			CertNotAfter:     seedNow.Add(90 * 24 * time.Hour),
			ReportedHostname: "laptop.lan",
			Facts:            `{"hostname":"laptop.lan"}`,
			Now:              seedNow,
		},
	}
}

func serverMachine() machine {
	secret := sha256.Sum256([]byte{12, 1, 2, 3})
	return machine{
		token: statedb.EnrollToken{
			SecretHash: secret, MachineName: "server", ExpectedHostname: "server.lan",
			NotAfter: seedNow.Add(90 * 24 * time.Hour),
			Created:  seedNow, Principal: "local:seed", RequestID: "seed-req-2",
			RequestHash: sha256.Sum256([]byte{12}),
		},
		new: statedb.NewMachine{
			Key:              statedb.EnrollKey{SecretHash: secret, InstallID: serverInstall, CSRKeyHash: sha256.Sum256([]byte{22})},
			MachineID:        MachineServer,
			CertDER:          []byte("seed-cert-" + MachineServer),
			Serial:           serverSerial,
			CertNotAfter:     seedNow.Add(90 * 24 * time.Hour),
			ReportedHostname: "server.lan",
			Facts:            `{"hostname":"server.lan"}`,
			Now:              seedNow,
		},
	}
}
