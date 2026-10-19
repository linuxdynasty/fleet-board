package fleetconfig_test

import (
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	"github.com/linuxdynasty/fleet-board/internal/fleetconfig"
	"google.golang.org/protobuf/proto"
)

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{
		"format_version: 1\n",
		"format_version: 1\ngroups: [{label: yes}]\n",
		"format_version: 1\nformatVersion: 1\n",
		"format_version: 1\ngroups: &a [*a]\n",
		"format_version: 1\ngroups: " + strings.Repeat("[", 70) + "{}" + strings.Repeat("]", 70),
		"format_version: 1\ngroups: [{priority: 18446744073709551616}]\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		config, err := fleetconfig.Decode(data)
		if err != nil {
			if config != nil || len(err.Error()) > 180 || strings.Contains(err.Error(), "canary-secret") {
				t.Fatalf("unsafe Decode error: %v", err)
			}
			return
		}
		if config == nil {
			t.Fatal("Decode returned nil without error")
		}
		encoded, err := fleetconfig.Encode(config)
		if err != nil {
			t.Fatalf("Decode accepted an unencodable config: %v", err)
		}
		if len(encoded) > 4<<20 {
			t.Fatal("Encode exceeded byte bound")
		}
	})
}

func FuzzEncode(f *testing.F) {
	seed, err := proto.Marshal(&hubv1.FleetConfig{FormatVersion: 1})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte{0x08, 0x01, 0xa0, 0x06, 0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4<<20 {
			return
		}
		var config hubv1.FleetConfig
		if err := proto.Unmarshal(data, &config); err != nil {
			return
		}
		encoded, err := fleetconfig.Encode(&config)
		if err != nil {
			return
		}
		if len(encoded) > 4<<20 {
			t.Fatal("Encode exceeded byte bound")
		}
		if _, err := fleetconfig.Decode(encoded); err != nil {
			t.Fatalf("Encode produced invalid YAML: %v", err)
		}
	})
}
