package fleetconfig

import (
	"errors"
	"fmt"
	"sort"

	"google.golang.org/protobuf/reflect/protoreflect"

	hubv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/hub/v1"
)

// These are hub or fleet record identifiers. Provider-owned identifiers such
// as TrustedUser.provider_user_id are plain strings and remain portable.
var forbiddenIDs = map[protoreflect.FullName]bool{
	"fleet.type.v1.MachineId":           true,
	"fleet.type.v1.AccountId":           true,
	"fleet.type.v1.SessionId":           true,
	"fleet.type.v1.GoalId":              true,
	"fleet.type.v1.PromptId":            true,
	"fleet.type.v1.ProbeId":             true,
	"fleet.type.v1.CommandId":           true,
	"fleet.type.v1.AttachId":            true,
	"fleet.type.v1.IntegrationId":       true,
	"fleet.type.v1.DeviceId":            true,
	"fleet.type.v1.PrincipalId":         true,
	"fleet.hub.v1.TriageItemId":         true,
	"fleet.hub.v1.ApprovalId":           true,
	"fleet.hub.v1.RoutineId":            true,
	"fleet.hub.v1.RepoId":               true,
	"fleet.hub.v1.NotificationTargetId": true,
	"fleet.hub.v1.AuditEntryId":         true,
	"fleet.hub.v1.SettingsId":           true,
	"fleet.hub.v1.PasskeyId":            true,
	"fleet.hub.v1.ApiTokenId":           true,
	"fleet.hub.v1.LoginSessionId":       true,
	"fleet.hub.v1.SuggestionId":         true,
	"fleet.hub.v1.InsightReportId":      true,
}

func isForbiddenID(descriptor protoreflect.MessageDescriptor) bool {
	return descriptor != nil && forbiddenIDs[descriptor.FullName()]
}

type limits struct {
	bytes int
	nodes int
}

func (l *limits) node() error {
	l.nodes--
	if l.nodes < 0 {
		return errors.New("fleet config: node count exceeds 100000")
	}
	return nil
}

func checkMessage(message protoreflect.Message, path string, depth int, budget *limits) error {
	if depth > maxDepth {
		return errors.New("fleet config: nesting exceeds 64 levels")
	}
	if err := budget.node(); err != nil {
		return err
	}
	if len(message.GetUnknown()) != 0 {
		return invalidAt("unknown protobuf fields", path)
	}
	var result error
	visit := func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		fieldPath := path + "." + string(field.Name())
		if depth+1 > maxDepth {
			result = errors.New("fleet config: nesting exceeds 64 levels")
			return false
		}
		if result = budget.node(); result != nil { // mapping key
			return false
		}
		if isForbiddenID(field.Message()) {
			result = invalidAt("hub ID is not portable", fieldPath)
			return false
		}
		if field.IsList() && value.List().Len() >= budget.nodes {
			result = errors.New("fleet config: node count exceeds 100000")
			return false
		}
		if field.Kind() == protoreflect.EnumKind {
			if field.IsList() {
				list := value.List()
				for i := 0; i < list.Len(); i++ {
					if field.Enum().Values().ByNumber(list.Get(i).Enum()) == nil {
						result = invalidAt("unknown enum", fieldPath)
						return false
					}
				}
			} else if field.Enum().Values().ByNumber(value.Enum()) == nil {
				result = invalidAt("unknown enum", fieldPath)
				return false
			}
		}
		if field.Kind() == protoreflect.StringKind || field.Kind() == protoreflect.BytesKind {
			if field.IsList() {
				list := value.List()
				for i := 0; i < list.Len(); i++ {
					budget.bytes -= valueSize(field.Kind(), list.Get(i))
					if budget.bytes < 0 {
						result = errors.New("fleet config: document exceeds 4 MiB")
						return false
					}
				}
			} else {
				budget.bytes -= valueSize(field.Kind(), value)
			}
			if budget.bytes < 0 {
				result = errors.New("fleet config: document exceeds 4 MiB")
				return false
			}
		}
		if field.IsMap() {
			result = errors.New("fleet config: protobuf maps are not supported")
			return false
		}
		if field.IsList() {
			if result = budget.node(); result != nil { // sequence
				return false
			}
			list := value.List()
			if list.Len() > 0 && depth+2 > maxDepth {
				result = errors.New("fleet config: nesting exceeds 64 levels")
				return false
			}
			if list.Len() > budget.nodes {
				result = errors.New("fleet config: node count exceeds 100000")
				return false
			}
			if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
				budget.nodes -= list.Len()
				return true
			}
			for i := 0; i < list.Len(); i++ {
				if isScalarWellKnown(field.Message()) {
					if result = budget.node(); result != nil {
						return false
					}
					if len(list.Get(i).Message().GetUnknown()) != 0 {
						result = invalidAt("unknown protobuf fields", fieldPath)
						return false
					}
					continue
				}
				if result = checkMessage(list.Get(i).Message(), fmt.Sprintf("%s[%d]", fieldPath, i), depth+2, budget); result != nil {
					return false
				}
			}
			return true
		}
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			result = budget.node() // scalar
			return result == nil
		}
		if isScalarWellKnown(field.Message()) {
			result = budget.node()
			if result == nil && len(value.Message().GetUnknown()) != 0 {
				result = invalidAt("unknown protobuf fields", fieldPath)
			}
			return result == nil
		}
		result = checkMessage(value.Message(), fieldPath, depth+1, budget)
		return result == nil
	}
	var fields []protoreflect.FieldDescriptor
	message.Range(func(field protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		fields = append(fields, field)
		return true
	})
	sort.Slice(fields, func(i, j int) bool { return fields[i].Number() < fields[j].Number() })
	for _, field := range fields {
		if !visit(field, message.Get(field)) {
			break
		}
	}
	return result
}

// UnportableIDs lists the field paths of the hub ID wrappers a FleetConfig
// still carries: the sections fleet.yaml cannot render until portable
// references and hub-side translation land. Empty means the codec will not
// refuse the document for IDs. Exporters use it to fail with a clear
// diagnostic instead of an opaque codec error.
func UnportableIDs(config *hubv1.FleetConfig) []string {
	if config == nil {
		return nil
	}
	var out []string
	var walk func(message protoreflect.Message, path string)
	walk = func(message protoreflect.Message, path string) {
		for i := 0; i < message.Descriptor().Fields().Len(); i++ {
			field := message.Descriptor().Fields().Get(i)
			fieldPath := path + "." + string(field.Name())
			if field.IsList() {
				list := message.Get(field).List()
				if isForbiddenID(field.Message()) {
					for j := 0; j < list.Len(); j++ {
						out = append(out, fmt.Sprintf("%s[%d]", fieldPath, j))
					}
					continue
				}
				if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
					continue
				}
				if isScalarWellKnown(field.Message()) {
					continue
				}
				for j := 0; j < list.Len(); j++ {
					walk(list.Get(j).Message(), fmt.Sprintf("%s[%d]", fieldPath, j))
				}
				continue
			}
			if field.IsMap() || !message.Has(field) {
				continue
			}
			if isForbiddenID(field.Message()) {
				out = append(out, fieldPath)
				continue
			}
			if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
				continue
			}
			if isScalarWellKnown(field.Message()) {
				continue
			}
			walk(message.Get(field).Message(), fieldPath)
		}
	}
	walk(config.ProtoReflect(), "config")
	sort.Strings(out)
	return out
}

func isScalarWellKnown(descriptor protoreflect.MessageDescriptor) bool {
	if descriptor == nil {
		return false
	}
	switch descriptor.FullName() {
	case "google.protobuf.Duration", "google.protobuf.Timestamp", "google.protobuf.FieldMask":
		return true
	default:
		return false
	}
}

func valueSize(kind protoreflect.Kind, value protoreflect.Value) int {
	if kind == protoreflect.BytesKind {
		return len(value.Bytes())
	}
	return len(value.String())
}
