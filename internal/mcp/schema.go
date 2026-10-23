package mcp

import (
	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// maxDepth bounds nested messages, so a message that contains itself still has a
// finite schema; deeper levels are any JSON object.
const maxDepth = 6

// Schema is a JSON Schema for md as protojson reads it, with proto field names.
// Fields the API marks OUTPUT_ONLY are left out; REQUIRED ones are required.
func Schema(md protoreflect.MessageDescriptor) map[string]any {
	return message(md, 0)
}

func message(md protoreflect.MessageDescriptor, depth int) map[string]any {
	switch md.FullName() {
	case "google.protobuf.Timestamp":
		return map[string]any{"type": "string", "format": "date-time"}
	case "google.protobuf.Duration":
		return map[string]any{"type": "string", "description": `seconds with an "s" suffix, e.g. "300s"`}
	case "google.protobuf.Struct":
		return map[string]any{"type": "object"}
	}
	if depth >= maxDepth {
		return map[string]any{"type": "object"}
	}
	props := map[string]any{}
	var required []string
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		behaviors := fieldBehaviors(f)
		if behaviors[annotations.FieldBehavior_OUTPUT_ONLY] {
			continue
		}
		props[string(f.Name())] = field(f, depth)
		if behaviors[annotations.FieldBehavior_REQUIRED] && f.Name() != "idempotency_key" {
			required = append(required, string(f.Name()))
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func field(f protoreflect.FieldDescriptor, depth int) map[string]any {
	if f.IsMap() {
		return map[string]any{"type": "object", "additionalProperties": single(f.MapValue(), depth)}
	}
	s := single(f, depth)
	if f.IsList() {
		return map[string]any{"type": "array", "items": s}
	}
	return s
}

func single(f protoreflect.FieldDescriptor, depth int) map[string]any {
	switch f.Kind() {
	case protoreflect.BoolKind:
		return map[string]any{"type": "boolean"}
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		return map[string]any{"type": "integer"}
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		// protojson writes 64-bit integers as strings and reads either.
		return map[string]any{"type": []string{"integer", "string"}}
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return map[string]any{"type": "number"}
	case protoreflect.StringKind:
		return map[string]any{"type": "string"}
	case protoreflect.BytesKind:
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case protoreflect.EnumKind:
		vals := f.Enum().Values()
		names := make([]string, 0, vals.Len())
		for i := 0; i < vals.Len(); i++ {
			names = append(names, string(vals.Get(i).Name()))
		}
		return map[string]any{"type": "string", "enum": names}
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return message(f.Message(), depth+1)
	}
	return map[string]any{}
}

func fieldBehaviors(f protoreflect.FieldDescriptor) map[annotations.FieldBehavior]bool {
	out := map[annotations.FieldBehavior]bool{}
	if f.Options() == nil {
		return out
	}
	bs, _ := proto.GetExtension(f.Options(), annotations.E_FieldBehavior).([]annotations.FieldBehavior)
	for _, b := range bs {
		out[b] = true
	}
	return out
}
