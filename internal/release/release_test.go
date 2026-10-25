package release_test

import (
	"strings"
	"testing"

	"github.com/linuxdynasty/fleet-board/internal/release"
)

// A tampered archive must fail the checksum and install nothing. These are
// the first slice: parse the SHA256SUMS file and verify bytes against it.

func TestParseChecksums(t *testing.T) {
	t.Parallel()
	good := strings.Repeat("a1", 32) + "  fleetd-darwin-arm64.tar.gz\n" +
		strings.Repeat("b2", 32) + "  fleet-agent-darwin-arm64.tar.gz\n"
	cases := []struct {
		name    string
		data    string
		want    map[string]string
		wantErr bool
	}{
		{name: "two entries parse", data: good, want: map[string]string{
			"fleetd-darwin-arm64.tar.gz":      strings.Repeat("a1", 32),
			"fleet-agent-darwin-arm64.tar.gz": strings.Repeat("b2", 32),
		}},
		{name: "empty file is refused", data: "", wantErr: true},
		{name: "a line without a hash is refused", data: "notahash  foo.tar.gz\n", wantErr: true},
		{name: "a short hash is refused", data: "abcd  foo.tar.gz\n", wantErr: true},
		{name: "an absolute path is refused", data: strings.Repeat("a", 64) + "  /tmp/evil.tar.gz\n", wantErr: true},
		{name: "a parent escape is refused", data: strings.Repeat("a", 64) + "  ../evil.tar.gz\n", wantErr: true},
		{name: "a duplicate name is refused", data: strings.Repeat("a", 64) + "  a.tar.gz\n" + strings.Repeat("b", 64) + "  a.tar.gz\n", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := release.ParseChecksums([]byte(tc.data))
			if tc.wantErr {
				if err == nil {
					t.Fatal("ParseChecksums succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseChecksums: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("ParseChecksums = %v, want %v", got, tc.want)
			}
			for k, v := range tc.want {
				if got[k] != v {
					t.Errorf("ParseChecksums[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestVerifyBytes(t *testing.T) {
	t.Parallel()
	// sha256("hello") worked out with sha256sum, not with the code under test.
	const helloSHA = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	cases := []struct {
		name    string
		data    string
		wantHex string
		wantErr bool
	}{
		{name: "matching bytes pass", data: "hello", wantHex: helloSHA},
		{name: "tampered bytes fail", data: "hella", wantHex: helloSHA, wantErr: true},
		{name: "a bad hex string fails", data: "hello", wantHex: "zzzz", wantErr: true},
		{name: "an empty want fails", data: "hello", wantHex: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := release.VerifyBytes([]byte(tc.data), tc.wantHex)
			if tc.wantErr && err == nil {
				t.Error("VerifyBytes succeeded, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("VerifyBytes: %v", err)
			}
		})
	}
}
