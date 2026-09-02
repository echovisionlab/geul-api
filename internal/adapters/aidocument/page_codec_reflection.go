package aidocumentadapter

import (
	"errors"
	"fmt"
	"strconv"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// Page recursive fields are generated protobuf paths. These helpers keep
// reflection and scalar assignment separate from Page operation compilation.
func pageSetPath(message protoreflect.Message, path []core.FieldPathSegment, value core.Value) error {
	container, field, err := pageResolvePath(message, path, true)
	if err != nil {
		return err
	}
	return pageAssignField(container, field, value)
}

func pageClearPath(message protoreflect.Message, path []core.FieldPathSegment) error {
	container, field, err := pageResolvePath(message, path, false)
	if err != nil {
		return err
	}
	if container != nil && container.IsValid() && field != nil {
		container.Clear(field)
	}
	return nil
}

func pageSetFilePath(message protoreflect.Message, path []core.FieldPathSegment, file string) error {
	container, field, err := pageResolvePath(message, path, true)
	if err != nil {
		return err
	}
	if field.Kind() != protoreflect.MessageKind || !pageIsFileAttachment(field.Message()) {
		return errors.New("page File target is not a generated FileAttachment")
	}
	if file == "" {
		container.Clear(field)
		return nil
	}
	attachment := container.Mutable(field).Message()
	active := findMessageField(attachment, "activeFileId")
	if active == nil {
		return errors.New("generated FileAttachment active_file_id is missing")
	}
	attachment.Set(active, protoreflect.ValueOfString(file))
	return nil
}

func pageResolvePath(message protoreflect.Message, path []core.FieldPathSegment, create bool) (protoreflect.Message, protoreflect.FieldDescriptor, error) {
	if len(path) == 0 {
		return nil, nil, errors.New("page recursive field path is required")
	}
	current := message
	for index := 0; index < len(path); index++ {
		segment := path[index]
		if segment.Field != "" {
			field := findMessageField(current, string(segment.Field))
			if field == nil {
				return nil, nil, fmt.Errorf("page field %q is not generated", segment.Field)
			}
			if index == len(path)-1 {
				return current, field, nil
			}
			if field.IsList() {
				if field.Kind() != protoreflect.MessageKind || index+1 >= len(path) || path[index+1].Item == "" {
					return nil, nil, fmt.Errorf("page field %q requires a stable list-item handle", segment.Field)
				}
				identity := pageIdentityField(field.Message())
				if identity == nil {
					return nil, nil, fmt.Errorf("page field %q has no stable item identity", segment.Field)
				}
				list := current.Get(field).List()
				var selected protoreflect.Message
				for itemIndex := 0; itemIndex < list.Len(); itemIndex++ {
					candidate := list.Get(itemIndex).Message()
					if candidate.Get(identity).String() == string(path[index+1].Item) {
						selected = candidate
						break
					}
				}
				if selected == nil || !selected.IsValid() {
					return nil, nil, fmt.Errorf("page list item %q does not exist", path[index+1].Item)
				}
				current = selected
				index++
				continue
			}
			if field.Kind() != protoreflect.MessageKind {
				return nil, nil, fmt.Errorf("page field %q is not an object", segment.Field)
			}
			if !current.Has(field) && !create {
				return nil, field, nil
			}
			current = current.Mutable(field).Message()
			continue
		}
		return nil, nil, errors.New("page list item paths must follow their repeated field and are not writable independently")
	}
	return nil, nil, errors.New("page field path is incomplete")
}

func pageAssignField(message protoreflect.Message, field protoreflect.FieldDescriptor, value core.Value) error {
	if field.IsList() {
		if value.Kind != core.ValueKindList {
			return errors.New("page repeated field requires a list value")
		}
		list := message.Mutable(field).List()
		list.Truncate(0)
		for _, item := range value.List {
			if field.Kind() == protoreflect.MessageKind {
				entry := list.NewElement().Message()
				if err := pageAssignMessage(entry, item.Value); err != nil {
					return err
				}
				list.Append(protoreflect.ValueOfMessage(entry))
			} else {
				scalar, err := pageScalarValue(field, item.Value)
				if err != nil {
					return err
				}
				list.Append(scalar)
			}
		}
		return nil
	}
	if field.Kind() == protoreflect.MessageKind {
		message.Clear(field)
		return pageAssignMessage(message.Mutable(field).Message(), value)
	}
	scalar, err := pageScalarValue(field, value)
	if err != nil {
		return err
	}
	message.Set(field, scalar)
	return nil
}

func pageAssignMessage(message protoreflect.Message, value core.Value) error {
	if value.Kind != core.ValueKindObject {
		return errors.New("page message field requires an object value")
	}
	for _, entry := range value.Object {
		field := findMessageField(message, string(entry.ID))
		if field == nil {
			return fmt.Errorf("page object field %q is not generated", entry.ID)
		}
		if err := pageAssignField(message, field, entry.Value); err != nil {
			return fmt.Errorf("page object field %q: %w", entry.ID, err)
		}
	}
	return nil
}

func pageScalarValue(field protoreflect.FieldDescriptor, value core.Value) (protoreflect.Value, error) {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return protoreflect.ValueOfBool(value.Boolean), nil
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(value.Text), nil
	case protoreflect.EnumKind:
		generated := pageGeneratedEnumName(field, value.Text)
		values := field.Enum().Values()
		for index := 0; index < values.Len(); index++ {
			candidate := values.Get(index)
			if string(candidate.Name()) == generated {
				return protoreflect.ValueOfEnum(candidate.Number()), nil
			}
		}
		return protoreflect.Value{}, fmt.Errorf("enum value %q is not generated", value.Text)
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		number, err := strconv.ParseInt(value.Text, 10, 32)
		return protoreflect.ValueOfInt32(int32(number)), err
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		number, err := strconv.ParseInt(value.Text, 10, 64)
		return protoreflect.ValueOfInt64(number), err
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		number, err := strconv.ParseUint(value.Text, 10, 32)
		return protoreflect.ValueOfUint32(uint32(number)), err
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		number, err := strconv.ParseUint(value.Text, 10, 64)
		return protoreflect.ValueOfUint64(number), err
	case protoreflect.FloatKind:
		number, err := strconv.ParseFloat(value.Text, 32)
		return protoreflect.ValueOfFloat32(float32(number)), err
	case protoreflect.DoubleKind:
		number, err := strconv.ParseFloat(value.Text, 64)
		return protoreflect.ValueOfFloat64(number), err
	default:
		return protoreflect.Value{}, fmt.Errorf("unsupported Page scalar kind %s", field.Kind())
	}
}
