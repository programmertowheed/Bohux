// Package secrets keeps secret values out of logs, traces, and error strings
// (CLAUDE.md rule 3). Values themselves live in OpenBao (ADR-0008, ADR-0014); this
// package only ever handles them on their way there.
package secrets

import (
	"fmt"
	"log/slog"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// Redacted returns a copy of m with every field marked `debug_redact = true` cleared,
// at any depth. m is not modified.
func Redacted(m proto.Message) proto.Message {
	if m == nil {
		return nil
	}
	clone := proto.Clone(m)
	clearRedacted(clone.ProtoReflect())
	return clone
}

func clearRedacted(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if opts, ok := fd.Options().(*descriptorpb.FieldOptions); ok && opts.GetDebugRedact() {
			m.Clear(fd)
			return true
		}
		switch {
		case fd.IsList() && fd.Message() != nil:
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				clearRedacted(l.Get(i).Message())
			}
		case fd.IsMap() && fd.MapValue().Message() != nil:
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				clearRedacted(mv.Message())
				return true
			})
		case !fd.IsList() && !fd.IsMap() && fd.Message() != nil:
			clearRedacted(v.Message())
		}
		return true
	})
}

// Message wraps a proto message so that logging or formatting it shows only its
// non-secret fields. Use it whenever a message that might hold a secret is logged:
//
//	slog.Info("set secret", "req", secrets.Message{M: req})
type Message struct {
	M proto.Message
}

// String renders the redacted message as compact JSON.
func (w Message) String() string {
	r := Redacted(w.M)
	if r == nil {
		return "<nil>"
	}
	b, err := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(r)
	if err != nil {
		return "<unprintable " + string(r.ProtoReflect().Descriptor().FullName()) + ">"
	}
	return string(b)
}

// GoString is String, so %#v cannot reach the raw fields.
func (w Message) GoString() string { return w.String() }

// Format makes every fmt verb (%v, %+v, %s, %q, …) use String.
func (w Message) Format(f fmt.State, _ rune) {
	_, _ = f.Write([]byte(w.String()))
}

// LogValue is String, for slog.
func (w Message) LogValue() slog.Value { return slog.StringValue(w.String()) }
