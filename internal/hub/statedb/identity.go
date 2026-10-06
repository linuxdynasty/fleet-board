package statedb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/linuxdynasty/fleet-board/internal/store"
)

// ErrNoIdentity means state.db has no hub_identity row. fleetd refuses to
// start rather than mint a new fleet id, CA or salt (02g, 03f).
var ErrNoIdentity = errors.New("statedb: state.db has no hub_identity row")

// Identity is the one hub_identity row (02g "Hub identity and restore").
// Certificates are DER and keys PKCS #8 DER. It holds the CA key and the
// account salt: never log or print it.
type Identity struct {
	FleetID       string
	CACert        []byte
	CAKey         []byte
	ServerCert    []byte
	ServerKey     []byte
	ServerNames   []string
	AccountSalt   []byte
	SaltVersion   int
	SettingsEpoch string
	// HubAddresses stays empty until plan row 3 fills it.
	HubAddresses []string
	CreatedTime  time.Time
}

func insertIdentity(id Identity) store.ApplyFunc {
	return func(tx *sql.Tx) error {
		names, err := json.Marshal(id.ServerNames)
		if err != nil {
			return fmt.Errorf("statedb: server names: %w", err)
		}
		addrs := id.HubAddresses
		if addrs == nil {
			addrs = []string{}
		}
		hub, err := json.Marshal(addrs)
		if err != nil {
			return fmt.Errorf("statedb: hub addresses: %w", err)
		}
		_, err = tx.Exec(`INSERT INTO hub_identity(id, fleet_id, ca_cert, ca_key, server_cert, server_key,
			server_names, account_salt, salt_version, settings_epoch, hub_addresses, created_time)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id.FleetID, id.CACert, id.CAKey, id.ServerCert, id.ServerKey, string(names),
			id.AccountSalt, id.SaltVersion, id.SettingsEpoch, string(hub), id.CreatedTime.UnixMilli())
		if err != nil {
			return fmt.Errorf("statedb: insert hub_identity: %w", err)
		}
		return nil
	}
}

// ReadIdentity reads the hub_identity row; a store without one is
// [ErrNoIdentity].
func ReadIdentity(ctx context.Context, s *store.Store) (Identity, error) {
	var id Identity
	err := s.Read(ctx, func(tx *sql.Tx) error {
		var names, hub string
		var created int64
		err := tx.QueryRowContext(ctx, `SELECT fleet_id, ca_cert, ca_key, server_cert, server_key, server_names,
			account_salt, salt_version, settings_epoch, hub_addresses, created_time FROM hub_identity WHERE id = 1`).
			Scan(&id.FleetID, &id.CACert, &id.CAKey, &id.ServerCert, &id.ServerKey, &names,
				&id.AccountSalt, &id.SaltVersion, &id.SettingsEpoch, &hub, &created)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNoIdentity
		}
		if err != nil {
			return fmt.Errorf("statedb: read hub_identity: %w", err)
		}
		if err := json.Unmarshal([]byte(names), &id.ServerNames); err != nil {
			return fmt.Errorf("statedb: hub_identity.server_names: %w", err)
		}
		if err := json.Unmarshal([]byte(hub), &id.HubAddresses); err != nil {
			return fmt.Errorf("statedb: hub_identity.hub_addresses: %w", err)
		}
		id.CreatedTime = time.UnixMilli(created)
		return nil
	})
	return id, err
}

// ErrServerCertChanged means the stored server certificate is no longer the
// one the caller replaced; nothing was written.
var ErrServerCertChanged = errors.New("statedb: the stored server certificate changed")

// ReplaceServerCert swaps the server certificate, key and SAN list in one
// writer transaction, only if the stored certificate is still old. The CA,
// fleet id and salt are untouched.
func ReplaceServerCert(ctx context.Context, s *store.Store, old, cert, key []byte, names []string) error {
	list, err := json.Marshal(names)
	if err != nil {
		return fmt.Errorf("statedb: server names: %w", err)
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE hub_identity SET server_cert = ?, server_key = ?, server_names = ?
			WHERE id = 1 AND server_cert = ?`, cert, key, string(list), old)
		if err != nil {
			return fmt.Errorf("statedb: replace server certificate: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.Join(ErrServerCertChanged, err)
		}
		return nil
	})
}
