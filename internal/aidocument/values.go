package aidocument

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

func Text(value string) Value  { return Value{Kind: ValueKindText, Text: value} }
func Boolean(value bool) Value { return Value{Kind: ValueKindBoolean, Boolean: value} }
func Number(canonicalDecimal string) Value {
	return Value{Kind: ValueKindNumber, Text: canonicalDecimal}
}
func RichText(items ...InlineItem) Value {
	return Value{Kind: ValueKindInline, Inline: append([]InlineItem{}, items...)}
}
func List(items ...ListItem) Value {
	return Value{Kind: ValueKindList, List: append([]ListItem{}, items...)}
}
func Object(fields ...ObjectField) Value {
	return Value{Kind: ValueKindObject, Object: append([]ObjectField{}, fields...)}
}
func PositionalItem(value Value) ListItem { return ListItem{Value: value} }
func StableItem(id RelationItemID, value Value) ListItem {
	return ListItem{ID: id, Value: value}
}
func ObjectValue(field FieldID, value Value) ObjectField {
	return ObjectField{ID: field, Value: value}
}

func SiteVariable(key string, linked bool) InlineItem {
	return InlineItem{Kind: InlineKindSiteVariable, Text: key, Linked: linked}
}

func InlineText(value string) InlineItem { return InlineItem{Kind: InlineKindText, Text: value} }
func Bold(children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindBold, Children: append([]InlineItem{}, children...)}
}
func Italic(children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindItalic, Children: append([]InlineItem{}, children...)}
}
func Underline(children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindUnderline, Children: append([]InlineItem{}, children...)}
}
func Strike(children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindStrike, Children: append([]InlineItem{}, children...)}
}
func InlineCode(children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindCode, Children: append([]InlineItem{}, children...)}
}
func TextColor(color string, children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindTextColor, Target: color, Children: append([]InlineItem{}, children...)}
}
func BackgroundColor(color string, children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindBackground, Target: color, Children: append([]InlineItem{}, children...)}
}
func Link(target string, children ...InlineItem) InlineItem {
	return InlineItem{Kind: InlineKindLink, Target: target, Children: append([]InlineItem{}, children...)}
}
func HardBreak() InlineItem { return InlineItem{Kind: InlineKindHardBreak} }
func InlineMath(expression string) InlineItem {
	return InlineItem{Kind: InlineKindMath, Text: expression}
}
func Placeholder(handle string) InlineItem {
	return InlineItem{Kind: InlineKindPlaceholder, Text: handle}
}

// ValidateInlineItems applies the protocol's closed inline vocabulary and
// canonical payload rules at transport and domain conversion boundaries.
func ValidateInlineItems(items []InlineItem) error {
	for _, item := range items {
		if err := item.validate(0); err != nil {
			return err
		}
	}
	return nil
}

func (v Value) validate() error { return v.validateDepth(0) }

func (v Value) validateDepth(depth int) error {
	if depth > 32 {
		return errors.New("typed value nesting exceeds 32 levels")
	}
	switch v.Kind {
	case ValueKindText:
		if len(v.Inline) != 0 || len(v.List) != 0 || len(v.Object) != 0 {
			return errors.New("text value cannot contain nested values")
		}
		return nil
	case ValueKindBoolean:
		if v.Text != "" || len(v.Inline) != 0 || len(v.List) != 0 || len(v.Object) != 0 {
			return errors.New("boolean value cannot contain nested values")
		}
		return nil
	case ValueKindNumber:
		if v.Text == "" || strings.TrimSpace(v.Text) != v.Text || len(v.Inline) != 0 || len(v.List) != 0 || len(v.Object) != 0 {
			return errors.New("number must be a non-empty canonical decimal string")
		}
		number, err := strconv.ParseFloat(v.Text, 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return fmt.Errorf("invalid number %q", v.Text)
		}
		return nil
	case ValueKindInline:
		if v.Text != "" || len(v.List) != 0 || len(v.Object) != 0 {
			return errors.New("inline value cannot contain another value representation")
		}
		return ValidateInlineItems(v.Inline)
	case ValueKindList:
		if v.Text != "" || len(v.Inline) != 0 || len(v.Object) != 0 {
			return errors.New("list value cannot contain another value representation")
		}
		stable := len(v.List) > 0 && v.List[0].ID != ""
		seen := make(map[RelationItemID]struct{}, len(v.List))
		for index, item := range v.List {
			if (item.ID != "") != stable {
				return errors.New("list item handles must be either present for every item or absent for every item")
			}
			if stable {
				if err := validateStableID("list item handle", string(item.ID), 160); err != nil {
					return err
				}
				if _, duplicate := seen[item.ID]; duplicate {
					return fmt.Errorf("duplicate list item handle %q", item.ID)
				}
				seen[item.ID] = struct{}{}
			}
			if err := item.Value.validateDepth(depth + 1); err != nil {
				return fmt.Errorf("list item %d: %w", index, err)
			}
		}
		return nil
	case ValueKindObject:
		if v.Text != "" || len(v.Inline) != 0 || len(v.List) != 0 {
			return errors.New("object value cannot contain another value representation")
		}
		seen := make(map[FieldID]struct{}, len(v.Object))
		for _, field := range v.Object {
			if err := validateStableID("object field ID", string(field.ID), 120); err != nil {
				return err
			}
			if _, duplicate := seen[field.ID]; duplicate {
				return fmt.Errorf("duplicate object field %q", field.ID)
			}
			seen[field.ID] = struct{}{}
			if err := field.Value.validateDepth(depth + 1); err != nil {
				return fmt.Errorf("object field %q: %w", field.ID, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported value kind %q", v.Kind)
	}
}

func (i InlineItem) validate(depth int) error {
	if depth > 32 {
		return errors.New("inline nesting exceeds 32 levels")
	}
	children := func(required bool) error {
		if required && len(i.Children) == 0 {
			return fmt.Errorf("inline item %q requires children", i.Kind)
		}
		if i.Text != "" {
			return fmt.Errorf("inline item %q cannot contain scalar text", i.Kind)
		}
		for _, child := range i.Children {
			if err := child.validate(depth + 1); err != nil {
				return err
			}
		}
		return nil
	}
	if i.Linked && i.Kind != InlineKindSiteVariable {
		return errors.New("linked is only valid for siteVariable")
	}
	switch i.Kind {
	case InlineKindSiteVariable:
		if !contentv1.IsSiteVariableKey(i.Text) || i.Target != "" || len(i.Children) != 0 {
			return errors.New("siteVariable requires one of the six catalog keys")
		}
		if i.Linked && i.Text != "site.url" && !strings.HasPrefix(i.Text, "contact.") {
			return errors.New("siteVariable key cannot be linked")
		}
		return nil
	case InlineKindText:
		if i.Target != "" || len(i.Children) != 0 {
			return errors.New("inline text cannot contain a target or children")
		}
		return nil
	case InlineKindBold, InlineKindItalic, InlineKindUnderline, InlineKindStrike, InlineKindCode:
		if i.Target != "" {
			return fmt.Errorf("inline item %q cannot contain a target", i.Kind)
		}
		return children(true)
	case InlineKindTextColor, InlineKindBackground:
		if err := validateOpaque("inline mark parameter", i.Target, 64); err != nil {
			return err
		}
		return children(true)
	case InlineKindLink:
		if err := validateOpaque("inline link target", i.Target, 2048); err != nil {
			return err
		}
		return children(true)
	case InlineKindHardBreak:
		if i.Text != "" || i.Target != "" || len(i.Children) != 0 {
			return errors.New("hard break cannot contain payload")
		}
		return nil
	case InlineKindMath:
		if i.Text == "" || i.Target != "" || len(i.Children) != 0 {
			return errors.New("inline math requires only an expression")
		}
		return nil
	case InlineKindPlaceholder:
		if i.Target != "" || len(i.Children) != 0 {
			return errors.New("placeholder cannot contain a target or children")
		}
		return validateStableID("placeholder handle", i.Text, 160)
	default:
		return fmt.Errorf("unsupported inline item kind %q", i.Kind)
	}
}
