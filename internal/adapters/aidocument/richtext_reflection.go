package aidocumentadapter

import (
	"errors"
	"fmt"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func (c *RichTextCodec) blockMessage(block protoreflect.Message) (core.BlockKind, protoreflect.Message, error) {
	oneof := block.Descriptor().Oneofs().ByName("value")
	field := block.WhichOneof(oneof)
	if field == nil {
		return "", nil, errors.New("rich Text block kind is required")
	}
	for kind, protoCase := range c.protoCases {
		if field.JSONName() == protoCase {
			return kind, block.Get(field).Message(), nil
		}
	}
	return "", nil, fmt.Errorf("unsupported Rich Text block case %q", field.JSONName())
}

func (c *RichTextCodec) localeBlockMessage(block protoreflect.Message) (core.BlockKind, protoreflect.Message, error) {
	return c.blockMessage(block)
}

func lowerCamelKind(kind string) string {
	parts := strings.Split(kind, "-")
	for index := 1; index < len(parts); index++ {
		if parts[index] != "" {
			parts[index] = strings.ToUpper(parts[index][:1]) + parts[index][1:]
		}
	}
	return strings.Join(parts, "")
}

// findCatalogField is the package-level generated catalog lookup used by
// domain registrations which compose their own generated document codec.
func findCatalogField(catalog core.Catalog, kind core.BlockKind, field core.FieldID) (core.FieldRule, bool) {
	for _, rule := range catalog.Fields {
		if rule.BlockKind == kind && rule.Field == field {
			return rule, true
		}
	}
	return core.FieldRule{}, false
}

func richTextFieldKey(kind core.BlockKind, field core.FieldID) string {
	return string(kind) + "\x00" + string(field)
}

func findMessageField(message protoreflect.Message, jsonName string) protoreflect.FieldDescriptor {
	fields := message.Descriptor().Fields()
	for index := 0; index < fields.Len(); index++ {
		if fields.Get(index).JSONName() == jsonName {
			return fields.Get(index)
		}
	}
	return nil
}

func initializeOneofMessage(message protoreflect.Message, oneofName protoreflect.Name, jsonName string) error {
	oneof := message.Descriptor().Oneofs().ByName(oneofName)
	if oneof == nil {
		return fmt.Errorf("oneof %q is missing", oneofName)
	}
	fields := oneof.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		if field.JSONName() == jsonName {
			message.Mutable(field)
			return nil
		}
	}
	return fmt.Errorf("oneof case %q is missing", jsonName)
}
