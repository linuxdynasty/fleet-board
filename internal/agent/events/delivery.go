package events

import (
	"errors"
	"fmt"
	"regexp"

	"google.golang.org/protobuf/reflect/protoreflect"

	agentv1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/agent/v1"
	typev1 "github.com/linuxdynasty/fleet-board/gen/go/fleet/type/v1"
)

// Errors of [Stamp].
var (
	// ErrNoBody means the event has no body, or one this build's table does
	// not list.
	ErrNoBody = errors.New("events: the event has no known body")
	// ErrNoKey means a COALESCE body lacks what its key names, or the key
	// names a placeholder no body carries yet (<profile>).
	ErrNoKey = errors.New("events: the coalesce_key cannot be filled from the body")
)

// Row is one Event.body arm's [DELIVERY] tag (02i, generated into
// delivery_table.go).
type Row struct {
	Number protoreflect.FieldNumber
	Name   string
	Class  agentv1.DeliveryClass
	// Key is the coalesce_key template, "" unless Class is COALESCE.
	Key string
}

type deliveryRow = Row

// Table is a copy of the generated table.
func Table() []Row { return append([]Row(nil), deliveryTable...) }

var byNumber = func() map[protoreflect.FieldNumber]Row {
	m := make(map[protoreflect.FieldNumber]Row, len(deliveryTable))
	for _, r := range deliveryTable {
		m[r.Number] = r
	}
	return m
}()

// fills reads each placeholder from the body. An empty result is no key.
var fills = map[string]func(m protoreflect.Message) string{
	"<pane>":          paneKey,
	"<probe_id>":      func(m protoreflect.Message) string { return nested(m, "probe_id", "value") },
	"<account_key>":   func(m protoreflect.Message) string { return nested(m, "account_key", "value") },
	"<harness>":       func(m protoreflect.Message) string { return nested(m, "harness", "name") },
	"<worktree path>": func(m protoreflect.Message) string { return str(m, "path") },
	"<tool>":          func(m protoreflect.Message) string { return str(m, "tool") },
	"<host>":          func(m protoreflect.Message) string { return str(m, "host") },
	// No body carries a profile yet (AccountIdentity, HarnessInfo): the row
	// that first emits those kinds decides it; until then they are refused.
	"<profile>": func(protoreflect.Message) string { return "" },
}

// KnownPlaceholder reports whether the key fill knows p (for the table test).
func KnownPlaceholder(p string) bool { _, ok := fills[p]; return ok }

var placeholder = regexp.MustCompile(`<[^>]*>`)

// Stamp sets ev.Delivery and ev.CoalesceKey from its body's row of the
// table, so the hub queues, sheds or coalesces it like the agent does (02i).
func Stamp(ev *agentv1.Event) error {
	m := ev.ProtoReflect()
	f := m.WhichOneof(m.Descriptor().Oneofs().ByName("body"))
	if f == nil {
		return ErrNoBody
	}
	r, ok := byNumber[f.Number()]
	if !ok {
		return fmt.Errorf("%w: field %d", ErrNoBody, f.Number())
	}
	body := m.Get(f).Message()
	key, missing := "", false
	if r.Key != "" {
		key = placeholder.ReplaceAllStringFunc(r.Key, func(p string) string {
			v := ""
			if fill := fills[p]; fill != nil {
				v = fill(body)
			}
			missing = missing || v == ""
			return v
		})
		if missing {
			return fmt.Errorf("%w: %s %s", ErrNoKey, r.Name, r.Key)
		}
	}
	ev.Delivery, ev.CoalesceKey = r.Class, key
	return nil
}

func field(m protoreflect.Message, name string) (protoreflect.FieldDescriptor, bool) {
	f := m.Descriptor().Fields().ByName(protoreflect.Name(name))
	return f, f != nil && m.Has(f)
}

func str(m protoreflect.Message, name string) string {
	if f, ok := field(m, name); ok && f.Kind() == protoreflect.StringKind {
		return m.Get(f).String()
	}
	return ""
}

func nested(m protoreflect.Message, name, inner string) string {
	if f, ok := field(m, name); ok && f.Message() != nil {
		return str(m.Get(f).Message(), inner)
	}
	return ""
}

// paneKey is the PaneKey's canonical string <server>/<server_start_id>/<pane_id>
// (02i, ids.proto), from a "pane" field that is a PaneKey or a TmuxPane.
func paneKey(m protoreflect.Message) string {
	f, ok := field(m, "pane")
	if !ok || f.Message() == nil {
		return ""
	}
	var k *typev1.PaneKey
	switch v := m.Get(f).Message().Interface().(type) {
	case *typev1.PaneKey:
		k = v
	case *agentv1.TmuxPane:
		k = v.GetKey()
	}
	if k.GetServer() == "" || k.GetServerStartId() == "" || k.GetPaneId() == "" {
		return ""
	}
	return k.GetServer() + "/" + k.GetServerStartId() + "/" + k.GetPaneId()
}
