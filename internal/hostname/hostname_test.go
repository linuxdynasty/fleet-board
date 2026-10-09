package hostname_test

import (
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/hostname"
)

// 03 W1: names compare lower case, without a trailing "." or ".local"; an
// empty reported hostname is a mismatch.
func TestMismatch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, expected, reported string
		mismatch                 bool
	}{
		{"same", "laptop.lan", "laptop.lan", false},
		{"case and trailing dot", "laptop.lan", "LAPTOP.LAN.", false},
		{".local on the reported side", "studio", "Studio.local", false},
		{".local and a dot on both sides", "Laptop.local.", "laptop.local", false},
		{"different", "laptop.lan", "desktop", true},
		{"empty reported", "laptop.lan", "", true},
		{"only .local reported", "laptop", ".local", true},
		{"only a dot reported", "laptop", ".", true},
		{"a suffix is not stripped twice", "laptop", "laptop.local.local", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := hostname.Mismatch(tc.expected, tc.reported); got != tc.mismatch {
				t.Errorf("Mismatch(%q, %q) = %v, want %v", tc.expected, tc.reported, got, tc.mismatch)
			}
		})
	}
}
