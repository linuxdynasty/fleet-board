package fleetconfig

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestIDPathInventory(t *testing.T) {
	type idPath struct {
		name   string
		fields []protoreflect.FieldDescriptor
	}
	var found []idPath
	var walk func(protoreflect.MessageDescriptor, string, []protoreflect.FieldDescriptor, map[protoreflect.FullName]int)
	walk = func(message protoreflect.MessageDescriptor, path string, fields []protoreflect.FieldDescriptor, ancestors map[protoreflect.FullName]int) {
		if ancestors[message.FullName()] >= 2 {
			return
		}
		ancestors[message.FullName()]++
		defer func() { ancestors[message.FullName()]-- }()
		for i := 0; i < message.Fields().Len(); i++ {
			field := message.Fields().Get(i)
			if field.IsMap() {
				t.Errorf("new map field needs codec support: %s.%s", path, field.Name())
				continue
			}
			if field.Message() == nil {
				continue
			}
			if strings.HasSuffix(string(field.Message().Name()), "Id") && !isForbiddenID(field.Message()) {
				t.Errorf("new ID wrapper needs a portability decision: %s.%s", path, field.Name())
			}
			fieldPath := path + "." + string(field.Name())
			if field.IsList() {
				fieldPath += "[]"
			}
			chain := append(append([]protoreflect.FieldDescriptor(nil), fields...), field)
			if isForbiddenID(field.Message()) {
				found = append(found, idPath{fieldPath, chain})
				continue
			}
			walk(field.Message(), fieldPath, chain, ancestors)
		}
	}
	walk((&hubv1.FleetConfig{}).ProtoReflect().Descriptor(), "config", nil, map[protoreflect.FullName]int{})
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	var names []string
	for _, path := range found {
		names = append(names, path.name)
	}
	want := []string{
		"config.accounts[].spec.bindings[].machine",
		"config.hub.trusted_gate.labels[].integration",
		"config.hub.trusted_gate.users[].integration",
		"config.repos[].repo.code_host",
		"config.repos[].tracker_projects[].integration",
		"config.repos[].trusted_gate.users[].integration",
		"config.routines[].account",
		"config.routines[].machine",
		"config.routines[].owner.device",
		"config.routines[].owner.id",
		"config.routines[].owner.on_behalf_of.device",
		"config.routines[].owner.on_behalf_of.id",
		"config.routines[].repo.code_host",
	}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("reachable ID paths changed: got %v, want %v", names, want)
	}
	for _, path := range found {
		t.Run(path.name, func(t *testing.T) {
			config := &hubv1.FleetConfig{FormatVersion: 1}
			parent := config.ProtoReflect()
			for _, field := range path.fields {
				if field.IsList() {
					parent = parent.Mutable(field).List().AppendMutable().Message()
				} else {
					parent = parent.Mutable(field).Message()
				}
			}
			if _, err := Encode(config); err == nil {
				t.Fatal("Encode accepted a present empty hub ID wrapper")
			}
			jsonBytes, err := (protojson.MarshalOptions{UseProtoNames: true}).Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := Decode(jsonBytes); err == nil || decoded != nil {
				t.Fatal("Decode accepted a present empty hub ID wrapper")
			}
			valueField := parent.Descriptor().Fields().ByName("value")
			parent.Set(valueField, protoreflect.ValueOfString("hub-id"))
			if _, err := Encode(config); err == nil {
				t.Fatal("Encode accepted a nonempty hub ID wrapper")
			}
			jsonBytes, err = (protojson.MarshalOptions{UseProtoNames: true}).Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			if decoded, err := Decode(jsonBytes); err == nil || decoded != nil {
				t.Fatal("Decode accepted a nonempty hub ID wrapper")
			}
		})
	}
}

func TestEncodeRejectsUnknownWireFields(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config *hubv1.FleetConfig
	}{
		{"root", &hubv1.FleetConfig{FormatVersion: 1}},
		{"nested", &hubv1.FleetConfig{FormatVersion: 1, Groups: []*hubv1.GroupConfig{{Label: "safe"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "root" {
				tc.config.ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			} else {
				tc.config.GetGroups()[0].ProtoReflect().SetUnknown([]byte{0xa0, 0x06, 0x01})
			}
			if _, err := Encode(tc.config); err == nil {
				t.Fatal("Encode accepted unknown protobuf fields")
			}
		})
	}
}

func TestEncodeRejectsUnknownEnum(t *testing.T) {
	config := &hubv1.FleetConfig{FormatVersion: 1, FleetValues: &typev1.SettingValues{DefaultServer: typev1.DefaultServerMode(999).Enum()}}
	if _, err := Encode(config); err == nil {
		t.Fatal("Encode accepted unknown enum value")
	}
}
