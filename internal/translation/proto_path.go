package translation

import (
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// CopyStableProtoPath copies one validated locale-unit path without replacing
// sibling target values. Repeated messages are addressed by schema-owned
// stable identity, never by the source array index used to reach them.
func CopyStableProtoPath(destination, source protoreflect.Message, path []string) error {
	if !destination.IsValid() || !source.IsValid() {
		return fmt.Errorf("stable proto source and destination are required")
	}
	if len(path) == 0 {
		return nil
	}
	field := source.Descriptor().Fields().ByName(protoreflect.Name(path[0]))
	if field == nil {
		return fmt.Errorf("unknown source field %q", path[0])
	}
	destinationField := destination.Descriptor().Fields().ByName(field.Name())
	if destinationField == nil {
		return fmt.Errorf("target field %q is unavailable", path[0])
	}
	if len(path) == 1 {
		return copyStableProtoLeaf(destination, destinationField, source, field)
	}
	if field.IsList() {
		if field.Kind() != protoreflect.MessageKind {
			return fmt.Errorf("stable list path %q does not address messages", strings.Join(path, "/"))
		}
		sourceList := source.Get(field).List()
		sourceItem, ok := stableProtoSourceListItem(sourceList, path[1])
		if !ok {
			return fmt.Errorf("stable list path %q is outside the source graph", strings.Join(path, "/"))
		}
		destinationList := destination.Mutable(destinationField).List()
		destinationItem, ok := stableProtoListItem(destinationList, sourceItem)
		if !ok {
			value := destinationList.NewElement()
			destinationItem = value.Message()
			if err := copyStableProtoIdentity(destinationItem, sourceItem); err != nil {
				return err
			}
			destinationList.Append(value)
			destinationItem = destinationList.Get(destinationList.Len() - 1).Message()
		}
		return CopyStableProtoPath(destinationItem, sourceItem, path[2:])
	}
	if field.Kind() != protoreflect.MessageKind || !source.Has(field) {
		return fmt.Errorf("stable path %q is absent from the source", strings.Join(path, "/"))
	}
	return CopyStableProtoPath(
		destination.Mutable(destinationField).Message(), source.Get(field).Message(), path[1:],
	)
}

func stableProtoSourceListItem(list protoreflect.List, segment string) (protoreflect.Message, bool) {
	for index := 0; index < list.Len(); index++ {
		candidate := list.Get(index).Message()
		if identity, ok := stableProtoIdentity(candidate); ok && identity == segment {
			return candidate, true
		}
	}
	return nil, false
}

func copyStableProtoLeaf(
	destination protoreflect.Message,
	destinationField protoreflect.FieldDescriptor,
	source protoreflect.Message,
	sourceField protoreflect.FieldDescriptor,
) error {
	if sourceField.IsList() {
		sourceList := source.Get(sourceField).List()
		destinationList := destination.Mutable(destinationField).List()
		destinationList.Truncate(0)
		for index := 0; index < sourceList.Len(); index++ {
			value := sourceList.Get(index)
			if sourceField.Kind() == protoreflect.MessageKind {
				cloned := proto.Clone(value.Message().Interface())
				value = protoreflect.ValueOfMessage(cloned.ProtoReflect())
			}
			destinationList.Append(value)
		}
		return nil
	}
	if sourceField.Kind() == protoreflect.MessageKind {
		if !source.Has(sourceField) {
			destination.Clear(destinationField)
			return nil
		}
		cloned := proto.Clone(source.Get(sourceField).Message().Interface())
		destination.Set(destinationField, protoreflect.ValueOfMessage(cloned.ProtoReflect()))
		return nil
	}
	destination.Set(destinationField, source.Get(sourceField))
	return nil
}

func stableProtoListItem(list protoreflect.List, source protoreflect.Message) (protoreflect.Message, bool) {
	sourceID, ok := stableProtoIdentity(source)
	if !ok {
		return nil, false
	}
	for index := 0; index < list.Len(); index++ {
		candidate := list.Get(index).Message()
		if candidateID, exists := stableProtoIdentity(candidate); exists && candidateID == sourceID {
			return candidate, true
		}
	}
	return nil, false
}

func stableProtoIdentity(message protoreflect.Message) (string, bool) {
	for _, name := range []protoreflect.Name{"row_id", "cell_id", "unit_id"} {
		field := message.Descriptor().Fields().ByName(name)
		if field == nil || field.Kind() != protoreflect.StringKind {
			continue
		}
		value := strings.TrimSpace(message.Get(field).String())
		return value, value != ""
	}
	return "", false
}

func copyStableProtoIdentity(destination, source protoreflect.Message) error {
	identity, ok := stableProtoIdentity(source)
	if !ok {
		return fmt.Errorf("stable list item identity is required")
	}
	for _, name := range []protoreflect.Name{"row_id", "cell_id", "unit_id"} {
		field := destination.Descriptor().Fields().ByName(name)
		if field != nil && field.Kind() == protoreflect.StringKind {
			destination.Set(field, protoreflect.ValueOfString(identity))
			return nil
		}
	}
	return fmt.Errorf("target stable list item identity is unavailable")
}
