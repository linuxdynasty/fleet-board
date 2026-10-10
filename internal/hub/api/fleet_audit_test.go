package api_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// auditRowText is every audit_log row with the action, each as one string
// of all its columns.
func (e *fleetEnv) auditRowText(t *testing.T, action string) []string {
	t.Helper()
	var out []string
	err := e.st.Read(context.Background(), func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(context.Background(), `SELECT id || '|' || create_time || '|' || hub_seq || '|' ||
			actor_id || '|' || actor_kind || '|' || actor_name || '|' || action || '|' || target || '|' || machine_id || '|' ||
			goal_id || '|' || command_id || '|' || request_id || '|' || outcome || '|' || reason || '|' || detail || '|' ||
			ifnull(occurred_time, '') FROM audit_log WHERE action = ? ORDER BY id`, action)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// #104: creating a token writes one machine.enroll_token row naming the
// caller and enroll_tokens/<id>, with the machine name, expected hostname
// and expiry; no part of the token, its secret or its hash is in it. A
// retried request_id writes no second row.
func TestCreateEnrollmentTokenWritesOneAuditRowWithoutTheToken(t *testing.T) {
	t.Parallel()
	e := newFleetEnv(t, localUser("alice"))
	req := enrollReq("6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f61", "laptop")
	req.Msg.ExpectedHostname = "laptop.lan"
	resp, err := e.client.CreateEnrollmentToken(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.client.CreateEnrollmentToken(context.Background(), req); err == nil {
		t.Fatal("a repeated request_id made a second token")
	}
	rows := e.auditRowText(t, "machine.enroll_token")
	if len(rows) != 1 {
		t.Fatalf("%d machine.enroll_token rows, want 1: %q", len(rows), rows)
	}
	exp := resp.Msg.GetExpireTime().AsTime().UTC().Format("2006-01-02T15:04:05Z")
	want := "|local:alice|LOCAL_USER|alice|machine.enroll_token|enroll_tokens/1||||6f1c2a4e-0d3b-4c5e-9f7a-1b2c3d4e5f61|OK||" +
		`{"machine_name":"laptop","expected_hostname":"laptop.lan","not_after":"` + exp + `"}|`
	if !strings.HasSuffix(rows[0], want) {
		t.Errorf("row %q\nwant it to end %q", rows[0], want)
	}
	tok := resp.Msg.GetToken()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(tok, "fbe1_"))
	if err != nil {
		t.Fatal(err)
	}
	secret := raw[33:]
	hash := sha256.Sum256(secret)
	for name, s := range map[string]string{
		"token": tok, "token body": strings.TrimPrefix(tok, "fbe1_")[:16],
		"secret (base64url)": base64.RawURLEncoding.EncodeToString(secret)[:16],
		"secret (hex)":       hex.EncodeToString(secret)[:16],
		"secret hash (hex)":  hex.EncodeToString(hash[:])[:12],
	} {
		if strings.Contains(rows[0], s) {
			t.Errorf("the audit row holds the %s", name)
		}
	}
}
