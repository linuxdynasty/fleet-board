package fleetconfig_test

import (
	"strings"
	"testing"
	"time"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestDocumentByteLimit(t *testing.T) {
	const limit = 4 << 20
	prefix := "format_version: 1\n#"
	inside := []byte(prefix + strings.Repeat("x", limit-len(prefix)))
	if len(inside) != limit {
		t.Fatal("bad byte-limit fixture")
	}
	if _, err := fleetconfig.Decode(inside); err != nil {
		t.Fatalf("exactly 4 MiB should parse: %v", err)
	}
	if _, err := fleetconfig.Decode(append(inside, 'x')); err == nil {
		t.Fatal("more than 4 MiB was accepted")
	}
	if _, err := fleetconfig.Encode(&hubv1.FleetConfig{FormatVersion: 1, Groups: []*hubv1.GroupConfig{{Label: strings.Repeat("x", limit)}}}); err == nil {
		t.Fatal("output larger than 4 MiB was accepted")
	}
}

func TestNodeCountBoundary(t *testing.T) {
	// Root mapping, two keys, two values and each empty group map make
	// five nodes plus the number of groups.
	const prefix = "format_version: 1\ngroups:\n"
	inside := prefix + strings.Repeat("  - {}\n", 99995)
	config, err := fleetconfig.Decode([]byte(inside))
	if err != nil || len(config.GetGroups()) != 99995 {
		t.Fatalf("100000 nodes should parse: %v", err)
	}
	if _, err := fleetconfig.Decode([]byte(inside + "  - {}\n")); err == nil {
		t.Fatal("100001 nodes were accepted")
	}
	groups := make([]*hubv1.GroupConfig, 0, 99995)
	for range 99995 {
		groups = append(groups, &hubv1.GroupConfig{})
	}
	if _, err := fleetconfig.Encode(&hubv1.FleetConfig{FormatVersion: 1, Groups: groups}); err != nil {
		t.Fatalf("Encode rejected 100000 nodes: %v", err)
	}
	groups = append(groups, &hubv1.GroupConfig{})
	if _, err := fleetconfig.Encode(&hubv1.FleetConfig{FormatVersion: 1, Groups: groups}); err == nil {
		t.Fatal("Encode accepted 100001 nodes")
	}
}

func TestPathologicalNestingIsRefused(t *testing.T) {
	// The pinned yaml/v3 scanner itself stops flow nesting past 10,000.
	// The codec applies its tighter 64-level bound after node parsing.
	for _, n := range []int{65, 2000, 10001} {
		t.Run(strings.Repeat("deep", n/100+1), func(t *testing.T) {
			input := "format_version: 1\ngroups: " + strings.Repeat("[", n) + "{}" + strings.Repeat("]", n) + "\n"
			if _, err := fleetconfig.Decode([]byte(input)); err == nil {
				t.Fatalf("nesting %d was accepted", n)
			}
		})
	}
}

func TestDepthBoundary(t *testing.T) {
	makeDocument := func(levels int) string {
		owner := "{}"
		for range levels {
			owner = "{on_behalf_of: " + owner + "}"
		}
		return "format_version: 1\nroutines: [{owner: " + owner + "}]\n"
	}
	if _, err := fleetconfig.Decode([]byte(makeDocument(60))); err != nil {
		t.Fatalf("64-level document should parse: %v", err)
	}
	if _, err := fleetconfig.Decode([]byte(makeDocument(61))); err == nil {
		t.Fatal("65-level document was accepted")
	}
}

func TestDurationCountsAsOneYAMLScalar(t *testing.T) {
	// The five root nodes plus three nodes per routine (mapping, key,
	// duration scalar) stay below the 100000-node bound at 33331.
	routines := make([]*hubv1.RoutineSpec, 0, 33331)
	duration := durationpb.New(time.Second)
	for range 33331 {
		routines = append(routines, &hubv1.RoutineSpec{BlockedAfter: duration})
	}
	if _, err := fleetconfig.Encode(&hubv1.FleetConfig{FormatVersion: 1, Routines: routines}); err != nil {
		t.Fatalf("Encode overcounted duration internals: %v", err)
	}
	routines = append(routines, &hubv1.RoutineSpec{BlockedAfter: duration})
	if _, err := fleetconfig.Encode(&hubv1.FleetConfig{FormatVersion: 1, Routines: routines}); err == nil {
		t.Fatal("Encode accepted more than 100000 YAML nodes")
	}
}
