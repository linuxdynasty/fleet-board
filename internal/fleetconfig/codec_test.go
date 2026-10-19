package fleetconfig_test

import (
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
	"google.golang.org/protobuf/proto"
)

func TestEncodeDecodeMinimal(t *testing.T) {
	input := &hubv1.FleetConfig{FormatVersion: 1, Groups: []*hubv1.GroupConfig{{Label: "yes"}}}
	want := "format_version: 1\ngroups:\n  - label: \"yes\"\n"
	got, err := fleetconfig.Encode(input)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if string(got) != want {
		t.Fatalf("Encode = %q, want %q", got, want)
	}
	decoded, err := fleetconfig.Decode(got)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !proto.Equal(decoded, input) {
		t.Fatalf("Decode(Encode) changed the message")
	}
	if !strings.HasSuffix(string(got), "\n") {
		t.Fatal("YAML must end with one newline")
	}
}

func TestEncodeRequiresVersionOne(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		config *hubv1.FleetConfig
	}{
		{"nil", nil},
		{"missing", &hubv1.FleetConfig{}},
		{"unknown", &hubv1.FleetConfig{FormatVersion: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if output, err := fleetconfig.Encode(tc.config); err == nil || output != nil {
				t.Fatal("Encode accepted an unsupported format version")
			}
		})
	}
}

func TestEncodeErrorIsStable(t *testing.T) {
	config := &hubv1.FleetConfig{FormatVersion: 1, Repos: []*hubv1.RepoSpec{{Repo: &typev1.RepoRef{CodeHost: &typev1.IntegrationId{}}}}, Routines: []*hubv1.RoutineSpec{{Account: &typev1.AccountId{}}}}
	_, first := fleetconfig.Encode(config)
	if first == nil {
		t.Fatal("Encode accepted hub IDs")
	}
	for range 20 {
		_, again := fleetconfig.Encode(config)
		if again == nil || again.Error() != first.Error() {
			t.Fatalf("Encode error changed: %v, %v", first, again)
		}
	}
}
