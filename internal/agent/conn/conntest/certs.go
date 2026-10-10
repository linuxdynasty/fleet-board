package conntest

import "testing"

// Serials are the certificate serials the hub stored for machineID, oldest
// first (cert_serials.serial, lowercase hex): one per Enroll and one per
// signed renewal.
func (h *Hub) Serials(t *testing.T, machineID string) []string {
	t.Helper()
	return column[string](t, h, `SELECT serial FROM cert_serials WHERE machine_id = ? ORDER BY created_time, rowid`, machineID)
}

// Renewals are the serials of the renewed certificates the hub stored for
// machineID (cert_renewals.serial), one per distinct CSR key.
func (h *Hub) Renewals(t *testing.T, machineID string) []string {
	t.Helper()
	return column[string](t, h, `SELECT serial FROM cert_renewals WHERE machine_id = ? ORDER BY created_time, rowid`, machineID)
}
