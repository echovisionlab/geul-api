package aidocumentadapter

import (
	"errors"
	"fmt"
	"strconv"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func canonicalEnum(descriptor contentv1.ContentFieldDescriptor, value protoreflect.EnumNumber, field protoreflect.FieldDescriptor) (string, error) {
	enum := field.Enum()
	if enum == nil {
		return "", errors.New("stored enum descriptor is missing")
	}
	stored := enum.Values().ByNumber(value)
	if stored == nil {
		return "", fmt.Errorf("unknown stored enum number %d", value)
	}
	name := string(stored.Name())
	for _, candidate := range descriptor.Values {
		text := fmt.Sprint(candidate)
		if pageGeneratedEnumName(field, text) == name {
			return text, nil
		}
	}
	return "", fmt.Errorf("enum value %q is not in generated descriptor", name)
}

func setEnum(field protoreflect.FieldDescriptor, canonical string) (protoreflect.EnumNumber, error) {
	name := pageGeneratedEnumName(field, canonical)
	if value := field.Enum().Values().ByName(protoreflect.Name(name)); value != nil {
		return value.Number(), nil
	}
	return 0, fmt.Errorf("enum value %q is not supported", canonical)
}

func numberText(value protoreflect.Value, kind protoreflect.Kind) string {
	switch kind {
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return strconv.FormatInt(value.Int(), 10)
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return strconv.FormatUint(value.Uint(), 10)
	default:
		return strconv.FormatFloat(value.Float(), 'g', -1, 64)
	}
}

func projectScalarValue(
	value protoreflect.Value,
	field protoreflect.FieldDescriptor,
	descriptor contentv1.ContentFieldDescriptor,
) (core.Value, error) {
	switch descriptor.Type {
	case "string", "uri", "uuid", "editor_color", "hex_color":
		return core.Text(value.String()), nil
	case "boolean":
		return core.Boolean(value.Bool()), nil
	case "integer", "number":
		return core.Number(numberText(value, field.Kind())), nil
	case "enum", "enum_int":
		canonical, err := canonicalEnum(descriptor, value.Enum(), field)
		if err != nil {
			return core.Value{}, err
		}
		if descriptor.Type == "enum_int" {
			return core.Number(canonical), nil
		}
		return core.Text(canonical), nil
	default:
		return core.Value{}, fmt.Errorf("unsupported scalar field type %q", descriptor.Type)
	}
}

func scalarProtoValue(
	field protoreflect.FieldDescriptor,
	descriptor contentv1.ContentFieldDescriptor,
	value core.Value,
) (protoreflect.Value, error) {
	switch descriptor.Type {
	case "string", "uri", "uuid", "editor_color", "hex_color":
		return protoreflect.ValueOfString(value.Text), nil
	case "boolean":
		return protoreflect.ValueOfBool(value.Boolean), nil
	case "integer", "number":
		return pageScalarValue(field, value)
	case "enum", "enum_int":
		enum, err := setEnum(field, value.Text)
		if err != nil {
			return protoreflect.Value{}, err
		}
		return protoreflect.ValueOfEnum(enum), nil
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported scalar field type %q", descriptor.Type)
	}
}

func projectProps(message protoreflect.Message, fields []contentv1.ContentFieldDescriptor) ([]core.FieldValue, []core.FileBinding, error) {
	propsField := findMessageField(message, "props")
	if propsField == nil || !message.Has(propsField) {
		return nil, nil, nil
	}
	props := message.Get(propsField).Message()
	var values []core.FieldValue
	var files []core.FileBinding
	for _, descriptor := range fields {
		field := findMessageField(props, descriptor.Name)
		if field == nil {
			continue
		}
		value, bindings, present, err := projectDescriptorValue(props, field, descriptor, core.FieldID(descriptor.Name), nil)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, bindings...)
		if present && !descriptorIsFile(descriptor) {
			values = append(values, core.FieldValue{ID: core.FieldID(descriptor.Name), Value: value})
		}
	}
	return values, files, nil
}

func projectLocaleMessage(
	message protoreflect.Message,
	block contentv1.ContentBlockDescriptor,
	table contentv1.ContentTableDescriptor,
) ([]core.FieldValue, error) {
	values, _, err := projectProps(message, block.Fields)
	if err != nil {
		return nil, err
	}
	switch block.Content {
	case "none":
		return values, nil
	case "inline":
		field := findMessageField(message, "content")
		if field == nil {
			return nil, errors.New("inline content field is missing")
		}
		items, err := projectInlineList(message.Get(field).List())
		if err != nil {
			return nil, err
		}
		return append(values, core.FieldValue{ID: richTextContentField, Value: core.RichText(items...)}), nil
	case "locale_text":
		field := findMessageField(message, "content")
		if field == nil {
			return nil, errors.New("locale text content field is missing")
		}
		return append(values, core.FieldValue{ID: richTextContentField, Value: core.Text(message.Get(field).String())}), nil
	case "table":
		field := findMessageField(message, "content")
		if field == nil || !message.Has(field) {
			return values, nil
		}
		value, err := projectLocaleTable(message.Get(field).Message(), table)
		if err != nil {
			return nil, err
		}
		return append(values, core.FieldValue{ID: richTextTableLocaleField, Value: value}), nil
	default:
		return nil, fmt.Errorf("unsupported content shape %q", block.Content)
	}
}

func projectDescriptorValue(
	message protoreflect.Message,
	field protoreflect.FieldDescriptor,
	descriptor contentv1.ContentFieldDescriptor,
	root core.FieldID,
	path []core.FieldPathSegment,
) (core.Value, []core.FileBinding, bool, error) {
	if descriptor.Type == "array" {
		list := message.Get(field).List()
		if list.Len() == 0 && !message.Has(field) {
			return core.Value{}, nil, false, nil
		}
		items := make([]core.ListItem, 0, list.Len())
		var files []core.FileBinding
		for index := 0; index < list.Len(); index++ {
			itemValue, itemFiles, _, err := projectListItem(list.Get(index), field, *descriptor.Item, root, path)
			if err != nil {
				return core.Value{}, nil, false, err
			}
			handle, err := projectedListHandle(descriptor.ItemIdentity, index, itemValue)
			if err != nil {
				return core.Value{}, nil, false, err
			}
			items = append(items, core.ListItem{ID: handle, Value: itemValue})
			for _, binding := range itemFiles {
				binding.Path = insertPathSegment(binding.Path, len(path), core.ListPath(handle))
				files = append(files, binding)
			}
		}
		if descriptorIsFile(*descriptor.Item) {
			// File arrays expose bindings at their stable item paths, not values.
			return core.Value{}, files, false, nil
		}
		return core.List(items...), files, true, nil
	}
	if descriptor.Type == "object" {
		if !message.Has(field) {
			return core.Value{}, nil, false, nil
		}
		value, files, err := projectObject(message.Get(field).Message(), descriptor.Fields, root, path)
		return value, files, true, err
	}
	if descriptorIsFile(descriptor) {
		if !message.Has(field) {
			return core.Value{}, nil, false, nil
		}
		attachment, ok := message.Get(field).Message().Interface().(*contentv1.FileAttachment)
		if !ok {
			return core.Value{}, nil, false, errors.New("generated File attachment type mismatch")
		}
		if attachment.GetActiveFileId() == "" {
			return core.Value{}, nil, false, nil
		}
		return core.Value{}, []core.FileBinding{{Field: root, Path: append([]core.FieldPathSegment(nil), path...), File: core.FileReference(attachment.GetActiveFileId())}}, false, nil
	}
	if !message.Has(field) {
		return core.Value{}, nil, false, nil
	}
	projected, err := projectScalarValue(message.Get(field), field, descriptor)
	return projected, nil, err == nil, err
}

func insertPathSegment(path []core.FieldPathSegment, index int, segment core.FieldPathSegment) []core.FieldPathSegment {
	if index < 0 || index > len(path) {
		index = len(path)
	}
	result := make([]core.FieldPathSegment, 0, len(path)+1)
	result = append(result, path[:index]...)
	result = append(result, segment)
	result = append(result, path[index:]...)
	return result
}

func projectListItem(value protoreflect.Value, listField protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, root core.FieldID, path []core.FieldPathSegment) (core.Value, []core.FileBinding, bool, error) {
	if descriptor.Type == "object" {
		projected, files, err := projectObject(value.Message(), descriptor.Fields, root, path)
		return projected, files, true, err
	}
	if descriptorIsFile(descriptor) {
		attachment, ok := value.Message().Interface().(*contentv1.FileAttachment)
		if !ok || attachment.GetActiveFileId() == "" {
			return core.Value{}, nil, false, nil
		}
		return core.Value{}, []core.FileBinding{{Field: root, Path: append([]core.FieldPathSegment(nil), path...), File: core.FileReference(attachment.GetActiveFileId())}}, false, nil
	}
	// Repeated scalars cannot report presence independently.
	projected, err := projectScalarValue(value, listField, descriptor)
	return projected, nil, err == nil, err
}

func projectObject(message protoreflect.Message, fields []contentv1.ContentFieldDescriptor, root core.FieldID, path []core.FieldPathSegment) (core.Value, []core.FileBinding, error) {
	var object []core.ObjectField
	var files []core.FileBinding
	for _, descriptor := range fields {
		field := findMessageField(message, descriptor.Name)
		if field == nil {
			continue
		}
		fieldPath := append(append([]core.FieldPathSegment(nil), path...), core.ObjectPath(core.FieldID(descriptor.Name)))
		value, bindings, present, err := projectDescriptorValue(message, field, descriptor, root, fieldPath)
		if err != nil {
			return core.Value{}, nil, err
		}
		files = append(files, bindings...)
		if present && !descriptorIsFile(descriptor) {
			object = append(object, core.ObjectValue(core.FieldID(descriptor.Name), value))
		}
	}
	return core.Object(object...), files, nil
}

func projectedListHandle(identity *contentv1.ContentArrayItemIdentityDescriptor, index int, value core.Value) (core.RelationItemID, error) {
	if identity == nil || identity.Strategy == "position" {
		return "", nil
	}
	switch identity.Strategy {
	case "fixed":
		if index >= len(identity.Values) {
			return "", errors.New("fixed list identity is shorter than the stored list")
		}
		return core.RelationItemID(identity.Values[index]), nil
	case "value":
		return core.RelationItemID(value.Text), nil
	case "field":
		for _, field := range value.Object {
			if string(field.ID) == identity.Field {
				return core.RelationItemID(field.Value.Text), nil
			}
		}
		return "", fmt.Errorf("list identity field %q is missing", identity.Field)
	default:
		return "", fmt.Errorf("unsupported list identity %q", identity.Strategy)
	}
}

func descriptorIsFile(descriptor contentv1.ContentFieldDescriptor) bool {
	return descriptor.Type == "file_attachment"
}

func setRichTextField(message protoreflect.Message, catalog contentv1.RichTextCatalogDescriptor, block contentv1.ContentBlockDescriptor, target core.FieldTarget, value core.Value) error {
	if target.Field == richTextContentField {
		content := findMessageField(message, "content")
		if content == nil {
			return errors.New("block does not expose inline content")
		}
		switch block.Content {
		case "inline":
			if value.Kind != core.ValueKindInline {
				return errors.New("inline content requires an inline value")
			}
			generated, err := inlineToGenerated(value.Inline)
			if err != nil {
				return err
			}
			list := message.Mutable(content).List()
			list.Truncate(0)
			for _, item := range generated {
				list.Append(protoreflect.ValueOfMessage(item.ProtoReflect()))
			}
			return nil
		case "locale_text":
			if value.Kind != core.ValueKindText {
				return errors.New("locale text content requires a text value")
			}
			message.Set(content, protoreflect.ValueOfString(value.Text))
			return nil
		default:
			return errors.New("block does not expose scalar content")
		}
	}
	if target.Field == richTextTableField || target.Field == richTextTableLocaleField {
		content := findMessageField(message, "content")
		if content == nil {
			return errors.New("block does not expose table content")
		}
		table := message.Mutable(content).Message()
		if len(target.Path) != 0 {
			return mutateTablePath(table, catalog, target, &value)
		}
		return setTableValue(table, value, target.Field == richTextTableLocaleField, catalog)
	}
	descriptor, ok := findContentDescriptor(block.Fields, string(target.Field))
	if !ok {
		return errors.New("field is not in generated block props")
	}
	propsField := findMessageField(message, "props")
	if propsField == nil {
		return errors.New("block props are missing")
	}
	props := message.Mutable(propsField).Message()
	field := findMessageField(props, descriptor.Name)
	if field == nil {
		return errors.New("generated props field is missing")
	}
	return setDescriptorAtPath(props, field, descriptor, target.Path, value)
}

func clearRichTextField(message protoreflect.Message, catalog contentv1.RichTextCatalogDescriptor, block contentv1.ContentBlockDescriptor, target core.FieldTarget) error {
	if target.Field == richTextContentField || target.Field == richTextTableField || target.Field == richTextTableLocaleField {
		content := findMessageField(message, "content")
		if content != nil && len(target.Path) != 0 && message.Has(content) {
			return mutateTablePath(message.Get(content).Message(), catalog, target, nil)
		}
		if content != nil {
			message.Clear(content)
		}
		return nil
	}
	descriptor, ok := findContentDescriptor(block.Fields, string(target.Field))
	if !ok {
		return errors.New("field is not in generated block props")
	}
	propsField := findMessageField(message, "props")
	if propsField == nil || !message.Has(propsField) {
		return nil
	}
	props := message.Get(propsField).Message()
	field := findMessageField(props, descriptor.Name)
	if field == nil {
		return errors.New("generated props field is missing")
	}
	return clearDescriptorAtPath(props, field, descriptor, target.Path)
}

func setRichTextFile(message protoreflect.Message, block contentv1.ContentBlockDescriptor, target core.FieldTarget, file core.FileReference) error {
	descriptor, ok := findContentDescriptor(block.Fields, string(target.Field))
	if !ok {
		return errors.New("file field is not in generated block props")
	}
	propsField := findMessageField(message, "props")
	if propsField == nil {
		return errors.New("block props are missing")
	}
	props := message.Mutable(propsField).Message()
	field := findMessageField(props, descriptor.Name)
	if field == nil {
		return errors.New("generated props field is missing")
	}
	if len(target.Path) != 0 && target.Path[len(target.Path)-1].Item != "" {
		return setRichTextFileArrayItem(props, field, descriptor, target.Path, file)
	}
	targetMessage, targetField, targetDescriptor, err := resolveDescriptorPath(props, field, descriptor, target.Path)
	if err != nil {
		return err
	}
	if !descriptorIsFile(targetDescriptor) {
		return errors.New("target is not a generated File field")
	}
	if file == "" {
		targetMessage.Clear(targetField)
		return nil
	}
	attachment := targetMessage.Mutable(targetField).Message()
	oneof := attachment.Descriptor().Oneofs().ByName("state")
	active := oneof.Fields().ByName("active_file_id")
	if active == nil {
		return errors.New("generated File active state is missing")
	}
	attachment.Set(active, protoreflect.ValueOfString(string(file)))
	return nil
}

func setRichTextFileArrayItem(message protoreflect.Message, field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, path []core.FieldPathSegment, file core.FileReference) error {
	container, arrayField, arrayDescriptor, err := resolveDescriptorPath(message, field, descriptor, path[:len(path)-1])
	if err != nil {
		return err
	}
	if arrayDescriptor.Type != "array" || arrayDescriptor.Item == nil || !descriptorIsFile(*arrayDescriptor.Item) {
		return errors.New("target is not a generated File array item")
	}
	list := container.Mutable(arrayField).List()
	if list.Len() == 0 {
		if file == "" {
			return nil
		}
		if identity := arrayDescriptor.ItemIdentity; identity != nil && identity.Strategy == "fixed" {
			// A batch may attach every face to a previously omitted cubemap array.
			// The generated validator still rejects incomplete attachment states.
			for range identity.Values {
				list.Append(list.NewElement())
			}
		}
	}
	index, err := listIndexByHandle(list, arrayDescriptor, path[len(path)-1].Item)
	if err != nil {
		return err
	}
	attachment := list.Get(index).Message()
	state := attachment.Descriptor().Oneofs().ByName("state")
	if file == "" {
		if selected := attachment.WhichOneof(state); selected != nil {
			attachment.Clear(selected)
		}
		return nil
	}
	attachment.Set(state.Fields().ByName("active_file_id"), protoreflect.ValueOfString(string(file)))
	return nil
}

func findContentDescriptor(fields []contentv1.ContentFieldDescriptor, name string) (contentv1.ContentFieldDescriptor, bool) {
	for _, field := range fields {
		if field.Name == name {
			return field, true
		}
	}
	return contentv1.ContentFieldDescriptor{}, false
}

func setDescriptorAtPath(message protoreflect.Message, field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, path []core.FieldPathSegment, value core.Value) error {
	targetMessage, targetField, targetDescriptor, err := resolveDescriptorPath(message, field, descriptor, path)
	if err != nil {
		return err
	}
	if targetDescriptor.Type != "object" && targetDescriptor.Type != "array" {
		return setDescriptorValue(targetMessage, targetField, targetDescriptor, value)
	}
	previous := proto.Clone(targetMessage.Interface()).ProtoReflect()
	if err := setDescriptorValue(targetMessage, targetField, targetDescriptor, value); err != nil {
		return err
	}
	return preserveDescriptorFiles(previous, targetMessage, targetField, targetDescriptor)
}

// File selectors live outside typed values. Preserve them only within object
// parents and stable array items that survive a typed replacement.
func preserveDescriptorFiles(previous, current protoreflect.Message, field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor) error {
	if !previous.Has(field) {
		return nil
	}
	if descriptorIsFile(descriptor) {
		current.Set(field, protoreflect.ValueOfMessage(proto.Clone(previous.Get(field).Message().Interface()).ProtoReflect()))
		return nil
	}
	switch descriptor.Type {
	case "object":
		if !current.Has(field) {
			return nil
		}
		before, after := previous.Get(field).Message(), current.Get(field).Message()
		for _, child := range descriptor.Fields {
			childField := findMessageField(before, child.Name)
			if childField != nil {
				if err := preserveDescriptorFiles(before, after, childField, child); err != nil {
					return err
				}
			}
		}
	case "array":
		identity := descriptor.ItemIdentity
		if identity == nil || identity.Strategy == "position" {
			return nil
		}
		before, after := previous.Get(field).List(), current.Mutable(field).List()
		if descriptorIsFile(*descriptor.Item) {
			// File-only arrays have no typed value and retain their fixed selectors.
			if identity.Strategy == "fixed" {
				for index := 0; index < before.Len(); index++ {
					after.Append(protoreflect.ValueOfMessage(proto.Clone(before.Get(index).Message().Interface()).ProtoReflect()))
				}
			}
			return nil
		}
		if descriptor.Item.Type != "object" {
			return nil
		}
		for index := 0; index < before.Len(); index++ {
			value, _, _, err := projectListItem(before.Get(index), field, *descriptor.Item, "", nil)
			if err != nil {
				return err
			}
			handle, err := projectedListHandle(identity, index, value)
			if err != nil {
				return err
			}
			currentIndex, err := listIndexByHandle(after, descriptor, handle)
			if err != nil {
				continue // A deleted stable item must not be recreated for its Files.
			}
			oldItem, newItem := before.Get(index).Message(), after.Get(currentIndex).Message()
			for _, child := range descriptor.Item.Fields {
				childField := findMessageField(oldItem, child.Name)
				if childField != nil {
					if err := preserveDescriptorFiles(oldItem, newItem, childField, child); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func clearDescriptorAtPath(message protoreflect.Message, field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, path []core.FieldPathSegment) error {
	if len(path) == 0 {
		message.Clear(field)
		return nil
	}
	targetMessage, targetField, _, err := resolveDescriptorPath(message, field, descriptor, path)
	if err != nil {
		return err
	}
	targetMessage.Clear(targetField)
	return nil
}

func resolveDescriptorPath(message protoreflect.Message, field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, path []core.FieldPathSegment) (protoreflect.Message, protoreflect.FieldDescriptor, contentv1.ContentFieldDescriptor, error) {
	currentMessage, currentField, currentDescriptor := message, field, descriptor
	for _, segment := range path {
		if segment.Field != "" {
			if currentDescriptor.Type != "object" {
				return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("field path traverses non-object generated value")
			}
			child, ok := findContentDescriptor(currentDescriptor.Fields, string(segment.Field))
			if !ok {
				return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("nested generated field is missing")
			}
			if currentField != nil {
				currentMessage = currentMessage.Mutable(currentField).Message()
			}
			currentField = findMessageField(currentMessage, child.Name)
			if currentField == nil {
				return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("nested protobuf field is missing")
			}
			currentDescriptor = child
			continue
		}
		if currentDescriptor.Type != "array" || currentDescriptor.Item == nil {
			return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("item path traverses non-array generated value")
		}
		if currentField == nil {
			return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("array field is missing from typed path")
		}
		list := currentMessage.Mutable(currentField).List()
		index, err := listIndexByHandle(list, currentDescriptor, segment.Item)
		if err != nil {
			return nil, nil, contentv1.ContentFieldDescriptor{}, err
		}
		if currentDescriptor.Item.Type != "object" {
			return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("scalar list items cannot be traversed")
		}
		currentMessage = list.Get(index).Message()
		currentDescriptor = *currentDescriptor.Item
		// The next path segment selects an object field; retain a synthetic
		// message field by resolving it in that iteration.
		currentField = nil
	}
	if currentField == nil {
		return nil, nil, contentv1.ContentFieldDescriptor{}, errors.New("path ends at a list object; select a typed field")
	}
	return currentMessage, currentField, currentDescriptor, nil
}

func listIndexByHandle(list protoreflect.List, descriptor contentv1.ContentFieldDescriptor, handle core.RelationItemID) (int, error) {
	identity := descriptor.ItemIdentity
	if identity == nil || identity.Strategy == "position" {
		return 0, errors.New("positional generated arrays cannot be addressed by item handle")
	}
	if identity.Strategy == "fixed" {
		for index, fixed := range identity.Values {
			if fixed == string(handle) && index < list.Len() {
				return index, nil
			}
		}
		return 0, errors.New("fixed generated array handle does not exist")
	}
	for index := 0; index < list.Len(); index++ {
		if identity.Strategy == "value" {
			if fmt.Sprint(list.Get(index).Interface()) == string(handle) {
				return index, nil
			}
			continue
		}
		if identity.Strategy == "field" {
			item := list.Get(index).Message()
			field := findMessageField(item, identity.Field)
			if field != nil && canonicalFieldText(item, field, *descriptor.Item) == string(handle) {
				return index, nil
			}
		}
	}
	return 0, errors.New("generated array item handle does not exist")
}

func canonicalFieldText(message protoreflect.Message, field protoreflect.FieldDescriptor, object contentv1.ContentFieldDescriptor) string {
	descriptor, ok := findContentDescriptor(object.Fields, field.JSONName())
	if !ok {
		return ""
	}
	if descriptor.Type == "enum" || descriptor.Type == "enum_int" {
		value, _ := canonicalEnum(descriptor, message.Get(field).Enum(), field)
		return value
	}
	return message.Get(field).String()
}

func setDescriptorValue(message protoreflect.Message, field protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, value core.Value) error {
	switch descriptor.Type {
	case "string", "uri", "uuid", "editor_color", "hex_color", "boolean", "integer", "number", "enum", "enum_int":
		converted, err := scalarProtoValue(field, descriptor, value)
		if err != nil {
			return err
		}
		message.Set(field, converted)
	case "object":
		message.Clear(field)
		object := message.Mutable(field).Message()
		for _, item := range value.Object {
			child, ok := findContentDescriptor(descriptor.Fields, string(item.ID))
			if !ok || descriptorIsFile(child) {
				continue
			}
			childField := findMessageField(object, child.Name)
			if childField == nil {
				return fmt.Errorf("generated object field %q is missing", child.Name)
			}
			if err := setDescriptorValue(object, childField, child, item.Value); err != nil {
				return err
			}
		}
	case "array":
		list := message.Mutable(field).List()
		list.Truncate(0)
		for _, item := range value.List {
			if err := appendDescriptorListValue(list, field, *descriptor.Item, item.Value); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported generated field type %q", descriptor.Type)
	}
	return nil
}

func appendDescriptorListValue(list protoreflect.List, listField protoreflect.FieldDescriptor, descriptor contentv1.ContentFieldDescriptor, value core.Value) error {
	if descriptor.Type == "object" {
		item := list.NewElement().Message()
		for _, fieldValue := range value.Object {
			child, ok := findContentDescriptor(descriptor.Fields, string(fieldValue.ID))
			if !ok || descriptorIsFile(child) {
				continue
			}
			field := findMessageField(item, child.Name)
			if field == nil {
				return fmt.Errorf("generated list object field %q is missing", child.Name)
			}
			if err := setDescriptorValue(item, field, child, fieldValue.Value); err != nil {
				return err
			}
		}
		list.Append(protoreflect.ValueOfMessage(item))
		return nil
	}
	element, err := scalarProtoValue(listField, descriptor, value)
	if err != nil {
		return err
	}
	list.Append(element)
	return nil
}
