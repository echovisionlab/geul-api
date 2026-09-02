// Package aidocument defines the schema-independent compact document contract
// shared by AI authoring clients and owning-domain adapters.
package aidocument

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const ProtocolVersion = "dcdp/1"

type Domain string

const (
	DomainPost          Domain = "post"
	DomainPage          Domain = "page"
	DomainWork          Domain = "work"
	DomainProgramEvent  Domain = "program_event"
	DomainMenu          Domain = "menu"
	DomainEmailTemplate Domain = "email_template"
	DomainEmailLayout   Domain = "email_layout"
	DomainCampaign      Domain = "campaign"
	DomainForm          Domain = "form"
	DomainPrivacy       Domain = "privacy"
	DomainTerms         Domain = "terms"
	DomainPostSeries    Domain = "post_series"
)

var supportedDomains = [...]Domain{
	DomainPost,
	DomainPage,
	DomainWork,
	DomainProgramEvent,
	DomainMenu,
	DomainEmailTemplate,
	DomainEmailLayout,
	DomainCampaign,
	DomainForm,
	DomainPrivacy,
	DomainTerms,
	DomainPostSeries,
}

var supportedDomainSet = func() map[Domain]struct{} {
	result := make(map[Domain]struct{}, len(supportedDomains))
	for _, domain := range supportedDomains {
		result[domain] = struct{}{}
	}
	return result
}()

// SupportedDomains returns the stable DCDP domain order without exposing the
// package-owned backing array to mutation.
func SupportedDomains() []Domain {
	return append([]Domain(nil), supportedDomains[:]...)
}

type DocumentReference string
type Revision string
type Locale string
type BlockID string
type BlockKind string
type RelationID string
type RelationItemID string
type RelationItemKind string
type FieldID string
type FileReference string
type Cursor string

type DocumentIdentity struct {
	Domain    Domain
	Reference DocumentReference
}

func (i DocumentIdentity) validate() error {
	if _, ok := supportedDomainSet[i.Domain]; !ok {
		return fmt.Errorf("unsupported document domain %q", i.Domain)
	}
	if err := validateOpaque("document reference", string(i.Reference), 256); err != nil {
		return err
	}
	return nil
}

func validateLocale(locale Locale) error {
	value := string(locale)
	if value == "" || len(value) > 35 {
		return fmt.Errorf("locale must contain 1 to 35 characters")
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || (character == '-' && index > 0 && index < len(value)-1) {
			continue
		}
		return fmt.Errorf("locale %q is not a canonical locale identifier", locale)
	}
	return nil
}

func validateOpaque(name, value string, max int) error {
	if value == "" || len(value) > max {
		return fmt.Errorf("%s must contain 1 to %d characters", name, max)
	}
	if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\t") {
		return fmt.Errorf("%s contains whitespace outside its opaque value", name)
	}
	return nil
}

// Stable protocol identities must not be array positions or paths derived
// from positions. Domain adapters have to map positional persistence shapes to
// stable semantic handles before entering this package.
func validateStableID(name, value string, max int) error {
	if err := validateOpaque(name, value, max); err != nil {
		return err
	}
	if strings.ContainsAny(value, "[]") {
		return fmt.Errorf("%s %q contains a positional path", name, value)
	}
	for _, segment := range strings.FieldsFunc(value, func(character rune) bool {
		return character == '/' || character == '.' || character == ':'
	}) {
		allDigits := segment != ""
		for _, character := range segment {
			if character < '0' || character > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return fmt.Errorf("%s %q contains a positional index", name, value)
		}
	}
	return nil
}

type LocaleRole string

const (
	LocaleRoleSource    LocaleRole = "source"
	LocaleRoleNonSource LocaleRole = "non_source"
)

func deriveLocaleRole(source, requested Locale) LocaleRole {
	if source == requested {
		return LocaleRoleSource
	}
	return LocaleRoleNonSource
}

type ValueKind string

const (
	ValueKindText    ValueKind = "t"
	ValueKindBoolean ValueKind = "b"
	ValueKindNumber  ValueKind = "n"
	ValueKindInline  ValueKind = "i"
	ValueKindList    ValueKind = "l"
	ValueKindObject  ValueKind = "o"
)

// Value is a closed typed tree. Lists and objects preserve nested catalog
// structure without admitting arbitrary JSON. A ListItem ID is either absent
// for an atomic positional list or present for every item in a stable list.
type Value struct {
	Kind    ValueKind
	Text    string
	Boolean bool
	Inline  []InlineItem
	List    []ListItem
	Object  []ObjectField
}

type ListItem struct {
	ID    RelationItemID
	Value Value
}

type ObjectField struct {
	ID    FieldID
	Value Value
}

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

type InlineKind string

const (
	InlineKindText        InlineKind = "t"
	InlineKindBold        InlineKind = "b"
	InlineKindItalic      InlineKind = "em"
	InlineKindUnderline   InlineKind = "u"
	InlineKindStrike      InlineKind = "s"
	InlineKindCode        InlineKind = "code"
	InlineKindTextColor   InlineKind = "fg"
	InlineKindBackground  InlineKind = "bg"
	InlineKindLink        InlineKind = "a"
	InlineKindHardBreak   InlineKind = "br"
	InlineKindMath        InlineKind = "math"
	InlineKindPlaceholder InlineKind = "ph"
)

// InlineItem is the closed compact rich-text vocabulary. It preserves common
// inline semantics without exposing an editor-native document representation.
type InlineItem struct {
	Kind     InlineKind
	Text     string
	Target   string
	Children []InlineItem
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
	switch i.Kind {
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

type FieldValue struct {
	ID    FieldID
	Value Value
}

type FileBinding struct {
	Field FieldID
	Path  []FieldPathSegment
	File  FileReference
}

type RelationItem struct {
	ID        RelationItemID
	Kind      RelationItemKind
	Order     int
	Shared    []FieldValue
	Localized []FieldValue
	Files     []FileBinding
}

type Relation struct {
	ID    RelationID
	Items []RelationItem
}

type Node struct {
	ID        BlockID
	Kind      BlockKind
	Parent    BlockID
	Order     int
	Shared    []FieldValue
	Localized []FieldValue
	Files     []FileBinding
	Relations []Relation
}

type FieldOwnership string

const (
	FieldOwnershipShared FieldOwnership = "shared"
	// FieldOwnershipSource is projected with shared values but changes the
	// translation source. Only the current source locale may mutate it.
	FieldOwnershipSource FieldOwnership = "source"
	FieldOwnershipLocale FieldOwnership = "locale"
)

type FieldRule struct {
	BlockKind    BlockKind
	Field        FieldID
	ValueKind    ValueKind
	Ownership    FieldOwnership
	Translatable bool
	File         bool
	// Schema closes recursively typed object/list values. Nil preserves the
	// scalar rule represented by the fields above.
	Schema *FieldSchema
}

type ListIdentityKind string

const (
	ListIdentityPositional ListIdentityKind = "positional"
	ListIdentityValue      ListIdentityKind = "value"
	ListIdentityField      ListIdentityKind = "field"
	ListIdentityFixed      ListIdentityKind = "fixed"
)

// FieldSchema is the recursive, generated catalog projection consumed by the
// generic DCDP validator. It describes shape and stable list identity only;
// owning-domain required/default constraints stay at the compiler boundary.
type FieldSchema struct {
	Kind         ValueKind
	Ownership    FieldOwnership
	Translatable bool
	File         bool
	Item         *FieldSchema
	Fields       []NestedFieldRule
	Identity     ListIdentityRule
}

type NestedFieldRule struct {
	Field  FieldID
	Schema FieldSchema
}

type ListIdentityRule struct {
	Kind    ListIdentityKind
	Field   FieldID
	Handles []RelationItemID
}

type RelationRule struct {
	BlockKind BlockKind
	Relation  RelationID
	ItemKinds []RelationItemKind
}

type RelationFieldRule struct {
	BlockKind    BlockKind
	Relation     RelationID
	ItemKind     RelationItemKind
	Field        FieldID
	ValueKind    ValueKind
	Ownership    FieldOwnership
	Translatable bool
	File         bool
	Schema       *FieldSchema
}

type Catalog struct {
	Fingerprint    string
	BlockKinds     []BlockKind
	Fields         []FieldRule
	Relations      []RelationRule
	RelationFields []RelationFieldRule
}

type Document struct {
	Identity         DocumentIdentity
	DocumentRevision Revision
	TargetRevision   *Revision
	SourceLocale     Locale
	Locale           Locale
	LocaleExists     bool
	Catalog          Catalog
	Nodes            []Node
}

func (d Document) Role() LocaleRole { return deriveLocaleRole(d.SourceLocale, d.Locale) }

func (d Document) validate() error {
	if err := d.Identity.validate(); err != nil {
		return err
	}
	if err := validateOpaque("document revision", string(d.DocumentRevision), 256); err != nil {
		return err
	}
	if err := validateLocale(d.SourceLocale); err != nil {
		return fmt.Errorf("source locale: %w", err)
	}
	if err := validateLocale(d.Locale); err != nil {
		return fmt.Errorf("requested locale: %w", err)
	}
	if d.Role() == LocaleRoleSource && !d.LocaleExists {
		return errors.New("source locale must exist")
	}
	if d.Role() == LocaleRoleSource && d.TargetRevision != nil {
		return errors.New("source locale cannot carry a target revision")
	}
	if d.Role() == LocaleRoleNonSource && d.LocaleExists {
		if d.TargetRevision == nil {
			return errors.New("existing target locale requires a target revision")
		}
		if err := validateOpaque("target revision", string(*d.TargetRevision), 256); err != nil {
			return err
		}
	}
	if d.Role() == LocaleRoleNonSource && !d.LocaleExists && d.TargetRevision != nil {
		return errors.New("absent target locale cannot carry a target revision")
	}
	if d.Role() == LocaleRoleNonSource && !d.LocaleExists {
		for _, node := range d.Nodes {
			if len(node.Localized) != 0 {
				return errors.New("absent translation locale cannot contain locale-owned values")
			}
			for _, relation := range node.Relations {
				for _, item := range relation.Items {
					if len(item.Localized) != 0 {
						return errors.New("absent translation locale cannot contain relation-item locale-owned values")
					}
				}
			}
		}
	}
	if err := d.Catalog.validate(); err != nil {
		return err
	}
	if err := validateNodes(d.Nodes); err != nil {
		return err
	}
	return validateDocumentCatalogValues(d.Catalog, d.Nodes)
}

func (c Catalog) validate() error {
	if err := validateOpaque("catalog fingerprint", c.Fingerprint, 256); err != nil {
		return err
	}
	kinds := make(map[BlockKind]struct{}, len(c.BlockKinds))
	for _, kind := range c.BlockKinds {
		if err := validateStableID("block kind", string(kind), 80); err != nil {
			return err
		}
		if _, duplicate := kinds[kind]; duplicate {
			return fmt.Errorf("duplicate block kind %q", kind)
		}
		kinds[kind] = struct{}{}
	}
	rules := make(map[string]struct{}, len(c.Fields))
	for _, rule := range c.Fields {
		if _, ok := kinds[rule.BlockKind]; !ok {
			return fmt.Errorf("field %q refers to unknown block kind %q", rule.Field, rule.BlockKind)
		}
		if err := validateStableID("field ID", string(rule.Field), 120); err != nil {
			return err
		}
		key := string(rule.BlockKind) + "\x00" + string(rule.Field)
		if _, duplicate := rules[key]; duplicate {
			return fmt.Errorf("duplicate field rule %q on %q", rule.Field, rule.BlockKind)
		}
		rules[key] = struct{}{}
		if err := validateFieldRuleShape(rule.Field, rule.ValueKind, rule.Ownership, rule.Translatable, rule.File); err != nil {
			return err
		}
		if rule.Schema != nil {
			if rule.Schema.Kind != rule.ValueKind || rule.Schema.Ownership != rule.Ownership || rule.Schema.Translatable != rule.Translatable || rule.Schema.File != rule.File {
				return fmt.Errorf("field %q recursive schema root does not match its field rule", rule.Field)
			}
			if err := validateFieldSchema(*rule.Schema, 0); err != nil {
				return fmt.Errorf("field %q schema: %w", rule.Field, err)
			}
		}
	}
	relations := make(map[string]map[RelationItemKind]struct{}, len(c.Relations))
	for _, rule := range c.Relations {
		if _, ok := kinds[rule.BlockKind]; !ok {
			return fmt.Errorf("relation %q refers to unknown block kind %q", rule.Relation, rule.BlockKind)
		}
		if err := validateStableID("relation ID", string(rule.Relation), 120); err != nil {
			return err
		}
		key := relationRuleKey(rule.BlockKind, rule.Relation)
		if _, duplicate := relations[key]; duplicate {
			return fmt.Errorf("duplicate relation rule %q on %q", rule.Relation, rule.BlockKind)
		}
		itemKinds := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			if err := validateStableID("relation item kind", string(kind), 80); err != nil {
				return err
			}
			if _, duplicate := itemKinds[kind]; duplicate {
				return fmt.Errorf("duplicate relation item kind %q on relation %q", kind, rule.Relation)
			}
			itemKinds[kind] = struct{}{}
		}
		if len(itemKinds) == 0 {
			return fmt.Errorf("relation %q must allow at least one item kind", rule.Relation)
		}
		relations[key] = itemKinds
	}
	relationFields := make(map[string]struct{}, len(c.RelationFields))
	for _, rule := range c.RelationFields {
		itemKinds, ok := relations[relationRuleKey(rule.BlockKind, rule.Relation)]
		if !ok {
			return fmt.Errorf("relation field %q refers to unknown relation %q", rule.Field, rule.Relation)
		}
		if _, ok := itemKinds[rule.ItemKind]; !ok {
			return fmt.Errorf("relation field %q refers to unsupported item kind %q", rule.Field, rule.ItemKind)
		}
		if err := validateStableID("relation field ID", string(rule.Field), 120); err != nil {
			return err
		}
		key := relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)
		if _, duplicate := relationFields[key]; duplicate {
			return fmt.Errorf("duplicate relation field rule %q", rule.Field)
		}
		relationFields[key] = struct{}{}
		if err := validateFieldRuleShape(rule.Field, rule.ValueKind, rule.Ownership, rule.Translatable, rule.File); err != nil {
			return err
		}
		if rule.Schema != nil {
			if rule.Schema.Kind != rule.ValueKind || rule.Schema.Ownership != rule.Ownership || rule.Schema.Translatable != rule.Translatable || rule.Schema.File != rule.File {
				return fmt.Errorf("relation field %q recursive schema root does not match its field rule", rule.Field)
			}
			if err := validateFieldSchema(*rule.Schema, 0); err != nil {
				return fmt.Errorf("relation field %q schema: %w", rule.Field, err)
			}
		}
	}
	return nil
}

func validateFieldSchema(schema FieldSchema, depth int) error {
	if depth > 32 {
		return errors.New("field schema nesting exceeds 32 levels")
	}
	if err := validateFieldRuleShape("nested", schema.Kind, schema.Ownership, schema.Translatable, schema.File); err != nil {
		return err
	}
	if schema.File {
		if schema.Item != nil || len(schema.Fields) != 0 || schema.Identity.Kind != "" {
			return errors.New("file schema cannot contain child shape")
		}
		return nil
	}
	switch schema.Kind {
	case ValueKindList:
		if schema.Item == nil || len(schema.Fields) != 0 {
			return errors.New("list schema requires exactly one item schema")
		}
		if err := validateListIdentity(schema.Identity); err != nil {
			return err
		}
		return validateFieldSchema(*schema.Item, depth+1)
	case ValueKindObject:
		if schema.Item != nil || schema.Identity.Kind != "" || len(schema.Fields) == 0 {
			return errors.New("object schema requires fields and cannot declare list shape")
		}
		seen := make(map[FieldID]struct{}, len(schema.Fields))
		for _, field := range schema.Fields {
			if err := validateStableID("nested field ID", string(field.Field), 120); err != nil {
				return err
			}
			if _, duplicate := seen[field.Field]; duplicate {
				return fmt.Errorf("duplicate nested field %q", field.Field)
			}
			seen[field.Field] = struct{}{}
			if err := validateFieldSchema(field.Schema, depth+1); err != nil {
				return fmt.Errorf("nested field %q: %w", field.Field, err)
			}
		}
		return nil
	default:
		if schema.Item != nil || len(schema.Fields) != 0 || schema.Identity.Kind != "" {
			return fmt.Errorf("scalar schema %q cannot contain nested shape", schema.Kind)
		}
		return nil
	}
}

func validateListIdentity(identity ListIdentityRule) error {
	switch identity.Kind {
	case ListIdentityPositional:
		if identity.Field != "" || len(identity.Handles) != 0 {
			return errors.New("positional list identity cannot carry handles or a field")
		}
	case ListIdentityValue:
		if identity.Field != "" || len(identity.Handles) != 0 {
			return errors.New("value list identity cannot carry handles or a field")
		}
	case ListIdentityField:
		if err := validateStableID("list identity field", string(identity.Field), 120); err != nil {
			return err
		}
		if len(identity.Handles) != 0 {
			return errors.New("field list identity cannot carry fixed handles")
		}
	case ListIdentityFixed:
		if identity.Field != "" || len(identity.Handles) == 0 {
			return errors.New("fixed list identity requires handles and no field")
		}
		seen := make(map[RelationItemID]struct{}, len(identity.Handles))
		for _, handle := range identity.Handles {
			if err := validateStableID("fixed list handle", string(handle), 160); err != nil {
				return err
			}
			if _, duplicate := seen[handle]; duplicate {
				return fmt.Errorf("duplicate fixed list handle %q", handle)
			}
			seen[handle] = struct{}{}
		}
	default:
		return fmt.Errorf("unsupported list identity %q", identity.Kind)
	}
	return nil
}

func validateFieldRuleShape(field FieldID, valueKind ValueKind, ownership FieldOwnership, translatable, file bool) error {
	if file {
		if translatable || (ownership != FieldOwnershipShared && ownership != FieldOwnershipSource) || valueKind != "" {
			return fmt.Errorf("file field %q must be shared or source-owned and cannot declare a scalar kind", field)
		}
		return nil
	}
	if ownership != FieldOwnershipShared && ownership != FieldOwnershipSource && ownership != FieldOwnershipLocale {
		return fmt.Errorf("field %q has unsupported ownership %q", field, ownership)
	}
	if translatable && ownership != FieldOwnershipLocale {
		return fmt.Errorf("translatable field %q must be locale-owned", field)
	}
	if err := (Value{Kind: valueKind}).validateKindOnly(); err != nil {
		return fmt.Errorf("field %q: %w", field, err)
	}
	return nil
}

func (v Value) validateKindOnly() error {
	switch v.Kind {
	case ValueKindText, ValueKindBoolean, ValueKindNumber, ValueKindInline, ValueKindList, ValueKindObject:
		return nil
	default:
		return fmt.Errorf("unsupported value kind %q", v.Kind)
	}
}

func validateNodes(nodes []Node) error {
	if err := validateProjectedNodes(nodes); err != nil {
		return err
	}
	ids := make(map[BlockID]struct{}, len(nodes))
	for _, node := range nodes {
		ids[node.ID] = struct{}{}
	}
	for _, node := range nodes {
		if node.Parent != "" {
			if _, ok := ids[node.Parent]; !ok {
				return fmt.Errorf("block %q has missing parent %q", node.ID, node.Parent)
			}
		}
		visited := map[BlockID]struct{}{node.ID: {}}
		parent := node.Parent
		for parent != "" {
			if _, cycle := visited[parent]; cycle {
				return fmt.Errorf("block %q is part of a parent cycle", node.ID)
			}
			visited[parent] = struct{}{}
			for _, candidate := range nodes {
				if candidate.ID == parent {
					parent = candidate.Parent
					break
				}
			}
		}
	}
	return nil
}

func validateProjectedNodes(nodes []Node) error {
	ids := make(map[BlockID]struct{}, len(nodes))
	parents := make(map[BlockID]BlockID, len(nodes))
	for _, node := range nodes {
		if err := validateStableID("block ID", string(node.ID), 160); err != nil {
			return err
		}
		if _, duplicate := ids[node.ID]; duplicate {
			return fmt.Errorf("duplicate block ID %q", node.ID)
		}
		ids[node.ID] = struct{}{}
		parents[node.ID] = node.Parent
		if node.Parent != "" {
			if err := validateStableID("parent block ID", string(node.Parent), 160); err != nil {
				return err
			}
		}
		if node.Order < 0 {
			return fmt.Errorf("block %q has negative order", node.ID)
		}
		if err := validateFieldValues(node.Shared); err != nil {
			return fmt.Errorf("block %q shared fields: %w", node.ID, err)
		}
		if err := validateFieldValues(node.Localized); err != nil {
			return fmt.Errorf("block %q localized fields: %w", node.ID, err)
		}
		files := make(map[string]struct{}, len(node.Files))
		for _, binding := range node.Files {
			key := nestedFieldKey(binding.Field, binding.Path)
			if _, duplicate := files[key]; duplicate {
				return fmt.Errorf("block %q has duplicate file target %q", node.ID, key)
			}
			files[key] = struct{}{}
			if err := validateStableID("file field ID", string(binding.Field), 120); err != nil {
				return err
			}
			for _, segment := range binding.Path {
				if err := segment.validate(); err != nil {
					return err
				}
			}
			if err := validateFileReference(binding.File); err != nil {
				return err
			}
		}
		relations := make(map[RelationID]struct{}, len(node.Relations))
		items := make(map[RelationItemID]struct{})
		for _, relation := range node.Relations {
			if err := validateStableID("relation ID", string(relation.ID), 120); err != nil {
				return err
			}
			if _, duplicate := relations[relation.ID]; duplicate {
				return fmt.Errorf("block %q has duplicate relation %q", node.ID, relation.ID)
			}
			relations[relation.ID] = struct{}{}
			for _, item := range relation.Items {
				if err := validateStableID("relation item ID", string(item.ID), 160); err != nil {
					return err
				}
				if err := validateStableID("relation item kind", string(item.Kind), 80); err != nil {
					return err
				}
				if _, duplicate := items[item.ID]; duplicate {
					return fmt.Errorf("block %q has duplicate relation item ID %q", node.ID, item.ID)
				}
				items[item.ID] = struct{}{}
				if item.Order < 0 {
					return fmt.Errorf("relation item %q has negative order", item.ID)
				}
				if err := validateFieldValues(item.Shared); err != nil {
					return fmt.Errorf("relation item %q shared fields: %w", item.ID, err)
				}
				if err := validateFieldValues(item.Localized); err != nil {
					return fmt.Errorf("relation item %q localized fields: %w", item.ID, err)
				}
				itemFiles := make(map[string]struct{}, len(item.Files))
				for _, binding := range item.Files {
					key := nestedFieldKey(binding.Field, binding.Path)
					if _, duplicate := itemFiles[key]; duplicate {
						return fmt.Errorf("relation item %q has duplicate file target %q", item.ID, key)
					}
					itemFiles[key] = struct{}{}
					if err := validateStableID("file field ID", string(binding.Field), 120); err != nil {
						return err
					}
					for _, segment := range binding.Path {
						if err := segment.validate(); err != nil {
							return err
						}
					}
					if err := validateFileReference(binding.File); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, node := range nodes {
		visited := map[BlockID]struct{}{node.ID: {}}
		parent := node.Parent
		for parent != "" {
			if _, included := ids[parent]; !included {
				break
			}
			if _, cycle := visited[parent]; cycle {
				return fmt.Errorf("block %q is part of a parent cycle", node.ID)
			}
			visited[parent] = struct{}{}
			parent = parents[parent]
		}
	}
	return nil
}

func validateDocumentCatalogValues(catalog Catalog, nodes []Node) error {
	kinds := make(map[BlockKind]struct{}, len(catalog.BlockKinds))
	for _, kind := range catalog.BlockKinds {
		kinds[kind] = struct{}{}
	}
	rules := make(map[string]FieldRule, len(catalog.Fields))
	for _, rule := range catalog.Fields {
		rules[fieldRuleKey(rule.BlockKind, rule.Field)] = rule
	}
	relations := make(map[string]map[RelationItemKind]struct{}, len(catalog.Relations))
	for _, rule := range catalog.Relations {
		allowed := make(map[RelationItemKind]struct{}, len(rule.ItemKinds))
		for _, kind := range rule.ItemKinds {
			allowed[kind] = struct{}{}
		}
		relations[relationRuleKey(rule.BlockKind, rule.Relation)] = allowed
	}
	relationFields := make(map[string]RelationFieldRule, len(catalog.RelationFields))
	for _, rule := range catalog.RelationFields {
		relationFields[relationFieldRuleKey(rule.BlockKind, rule.Relation, rule.ItemKind, rule.Field)] = rule
	}
	for _, node := range nodes {
		if _, ok := kinds[node.Kind]; !ok {
			return fmt.Errorf("block %q has unknown kind %q", node.ID, node.Kind)
		}
		for _, field := range node.Shared {
			rule, ok := rules[fieldRuleKey(node.Kind, field.ID)]
			if !ok || rule.File || (rule.Ownership != FieldOwnershipShared && rule.Ownership != FieldOwnershipSource) || rule.ValueKind != field.Value.Kind {
				return fmt.Errorf("block %q shared field %q does not match the catalog", node.ID, field.ID)
			}
			if rule.Schema != nil {
				if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
					return fmt.Errorf("block %q shared field %q: %w", node.ID, field.ID, err)
				}
			}
		}
		for _, field := range node.Localized {
			rule, ok := rules[fieldRuleKey(node.Kind, field.ID)]
			if !ok || rule.File || rule.Ownership != FieldOwnershipLocale || rule.ValueKind != field.Value.Kind {
				return fmt.Errorf("block %q localized field %q does not match the catalog", node.ID, field.ID)
			}
			if rule.Schema != nil {
				if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
					return fmt.Errorf("block %q localized field %q: %w", node.ID, field.ID, err)
				}
			}
		}
		for _, file := range node.Files {
			rule, ok := rules[fieldRuleKey(node.Kind, file.Field)]
			if !ok {
				return fmt.Errorf("block %q file field %q does not match the catalog", node.ID, file.Field)
			}
			resolved, err := resolveNestedFieldRule(rule.ValueKind, rule.Ownership, rule.Translatable, rule.File, rule.Schema, file.Path)
			if err != nil || !resolved.file {
				return fmt.Errorf("block %q file target %q does not match the catalog", node.ID, nestedFieldKey(file.Field, file.Path))
			}
		}
		for _, relation := range node.Relations {
			allowed, ok := relations[relationRuleKey(node.Kind, relation.ID)]
			if !ok {
				return fmt.Errorf("block %q relation %q does not match the catalog", node.ID, relation.ID)
			}
			for _, item := range relation.Items {
				if _, ok := allowed[item.Kind]; !ok {
					return fmt.Errorf("relation item %q kind %q does not match the catalog", item.ID, item.Kind)
				}
				for _, field := range item.Shared {
					rule, ok := relationFields[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, field.ID)]
					if !ok || rule.File || (rule.Ownership != FieldOwnershipShared && rule.Ownership != FieldOwnershipSource) || rule.ValueKind != field.Value.Kind {
						return fmt.Errorf("relation item %q shared field %q does not match the catalog", item.ID, field.ID)
					}
					if rule.Schema != nil {
						if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
							return fmt.Errorf("relation item %q shared field %q: %w", item.ID, field.ID, err)
						}
					}
				}
				for _, field := range item.Localized {
					rule, ok := relationFields[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, field.ID)]
					if !ok || rule.File || rule.Ownership != FieldOwnershipLocale || rule.ValueKind != field.Value.Kind {
						return fmt.Errorf("relation item %q localized field %q does not match the catalog", item.ID, field.ID)
					}
					if rule.Schema != nil {
						if err := validateValueForSchema(field.Value, *rule.Schema, 0); err != nil {
							return fmt.Errorf("relation item %q localized field %q: %w", item.ID, field.ID, err)
						}
					}
				}
				for _, file := range item.Files {
					rule, ok := relationFields[relationFieldRuleKey(node.Kind, relation.ID, item.Kind, file.Field)]
					if !ok {
						return fmt.Errorf("relation item %q file field %q does not match the catalog", item.ID, file.Field)
					}
					resolved, err := resolveNestedFieldRule(rule.ValueKind, rule.Ownership, rule.Translatable, rule.File, rule.Schema, file.Path)
					if err != nil || !resolved.file {
						return fmt.Errorf("relation item %q file target %q does not match the catalog", item.ID, nestedFieldKey(file.Field, file.Path))
					}
				}
			}
		}
	}
	return nil
}

func nestedFieldKey(field FieldID, path []FieldPathSegment) string {
	key := string(field)
	for _, segment := range path {
		if segment.Field != "" {
			key += "/field:" + string(segment.Field)
		} else {
			key += "/item:" + string(segment.Item)
		}
	}
	return key
}

func validateValueForSchema(value Value, schema FieldSchema, depth int) error {
	if depth > 32 {
		return errors.New("typed value nesting exceeds 32 levels")
	}
	if schema.File {
		return errors.New("file schema requires a typed file binding")
	}
	if value.Kind != schema.Kind {
		return fmt.Errorf("value kind %q does not match schema kind %q", value.Kind, schema.Kind)
	}
	if err := value.validateDepth(depth); err != nil {
		return err
	}
	switch schema.Kind {
	case ValueKindList:
		stable := schema.Identity.Kind != ListIdentityPositional
		if len(value.List) > 0 && (value.List[0].ID != "") != stable {
			return fmt.Errorf("list identity %q does not match item handles", schema.Identity.Kind)
		}
		if schema.Identity.Kind == ListIdentityFixed {
			if len(value.List) != len(schema.Identity.Handles) {
				return errors.New("fixed list length does not match its catalog handles")
			}
			for index, item := range value.List {
				if item.ID != schema.Identity.Handles[index] {
					return fmt.Errorf("fixed list item %d must use handle %q", index, schema.Identity.Handles[index])
				}
			}
		}
		for _, item := range value.List {
			if schema.Identity.Kind == ListIdentityValue {
				if item.Value.Kind != ValueKindText && item.Value.Kind != ValueKindNumber {
					return errors.New("value-identified list items must be canonical text or number scalars")
				}
				if item.ID != RelationItemID(item.Value.Text) {
					return errors.New("value-identified list item handle must equal its canonical scalar value")
				}
			}
			if schema.Identity.Kind == ListIdentityField {
				identity, ok := objectField(item.Value, schema.Identity.Field)
				if !ok || identity.Kind != ValueKindText || item.ID != RelationItemID(identity.Text) {
					return fmt.Errorf("field-identified list item handle must equal text field %q", schema.Identity.Field)
				}
			}
			if err := validateValueForSchema(item.Value, *schema.Item, depth+1); err != nil {
				return fmt.Errorf("list item %q: %w", item.ID, err)
			}
		}
	case ValueKindObject:
		rules := make(map[FieldID]FieldSchema, len(schema.Fields))
		for _, field := range schema.Fields {
			rules[field.Field] = field.Schema
		}
		for _, field := range value.Object {
			rule, ok := rules[field.ID]
			if !ok {
				return fmt.Errorf("object field %q is not in the catalog", field.ID)
			}
			if rule.File {
				return fmt.Errorf("object file field %q requires a typed file binding", field.ID)
			}
			if err := validateValueForSchema(field.Value, rule, depth+1); err != nil {
				return fmt.Errorf("object field %q: %w", field.ID, err)
			}
		}
	}
	return nil
}

func objectField(value Value, fieldID FieldID) (Value, bool) {
	if value.Kind != ValueKindObject {
		return Value{}, false
	}
	for _, field := range value.Object {
		if field.ID == fieldID {
			return field.Value, true
		}
	}
	return Value{}, false
}

func validateFieldValues(fields []FieldValue) error {
	seen := make(map[FieldID]struct{}, len(fields))
	for _, field := range fields {
		if err := validateStableID("field ID", string(field.ID), 120); err != nil {
			return err
		}
		if _, duplicate := seen[field.ID]; duplicate {
			return fmt.Errorf("duplicate field %q", field.ID)
		}
		seen[field.ID] = struct{}{}
		if err := field.Value.validate(); err != nil {
			return fmt.Errorf("field %q: %w", field.ID, err)
		}
	}
	return nil
}

func validateFileReference(reference FileReference) error {
	value := string(reference)
	if err := validateOpaque("file reference", value, 256); err != nil {
		return err
	}
	if strings.HasPrefix(strings.ToLower(value), "data:") {
		return errors.New("inline data file payloads are forbidden")
	}
	return nil
}

func canonicalNodes(nodes []Node) []Node {
	clones := append([]Node(nil), nodes...)
	ids := make(map[BlockID]struct{}, len(clones))
	for index := range clones {
		ids[clones[index].ID] = struct{}{}
		clones[index].Shared = append([]FieldValue(nil), clones[index].Shared...)
		clones[index].Localized = append([]FieldValue(nil), clones[index].Localized...)
		clones[index].Files = append([]FileBinding(nil), clones[index].Files...)
		clones[index].Relations = append([]Relation(nil), clones[index].Relations...)
		cloneFieldValues(clones[index].Shared)
		cloneFieldValues(clones[index].Localized)
		cloneFileBindings(clones[index].Files)
		sort.Slice(clones[index].Shared, func(a, b int) bool { return clones[index].Shared[a].ID < clones[index].Shared[b].ID })
		sort.Slice(clones[index].Localized, func(a, b int) bool { return clones[index].Localized[a].ID < clones[index].Localized[b].ID })
		sort.Slice(clones[index].Files, func(a, b int) bool {
			left := nestedFieldKey(clones[index].Files[a].Field, clones[index].Files[a].Path)
			right := nestedFieldKey(clones[index].Files[b].Field, clones[index].Files[b].Path)
			if left != right {
				return left < right
			}
			return clones[index].Files[a].File < clones[index].Files[b].File
		})
		for relationIndex := range clones[index].Relations {
			relation := &clones[index].Relations[relationIndex]
			relation.Items = append([]RelationItem(nil), relation.Items...)
			for itemIndex := range relation.Items {
				item := &relation.Items[itemIndex]
				item.Shared = append([]FieldValue(nil), item.Shared...)
				item.Localized = append([]FieldValue(nil), item.Localized...)
				item.Files = append([]FileBinding(nil), item.Files...)
				cloneFieldValues(item.Shared)
				cloneFieldValues(item.Localized)
				cloneFileBindings(item.Files)
				sort.Slice(item.Shared, func(a, b int) bool { return item.Shared[a].ID < item.Shared[b].ID })
				sort.Slice(item.Localized, func(a, b int) bool { return item.Localized[a].ID < item.Localized[b].ID })
				sort.Slice(item.Files, func(a, b int) bool {
					return nestedFieldKey(item.Files[a].Field, item.Files[a].Path) < nestedFieldKey(item.Files[b].Field, item.Files[b].Path)
				})
			}
			sort.SliceStable(relation.Items, func(a, b int) bool {
				if relation.Items[a].Order != relation.Items[b].Order {
					return relation.Items[a].Order < relation.Items[b].Order
				}
				return relation.Items[a].ID < relation.Items[b].ID
			})
		}
		sort.Slice(clones[index].Relations, func(a, b int) bool {
			return clones[index].Relations[a].ID < clones[index].Relations[b].ID
		})
	}
	children := make(map[BlockID][]Node, len(clones))
	roots := make([]Node, 0, len(clones))
	for _, node := range clones {
		if node.Parent == "" {
			roots = append(roots, node)
			continue
		}
		if _, parentIncluded := ids[node.Parent]; !parentIncluded {
			roots = append(roots, node)
			continue
		}
		children[node.Parent] = append(children[node.Parent], node)
	}
	less := func(items []Node) {
		sort.SliceStable(items, func(a, b int) bool {
			if items[a].Parent != items[b].Parent {
				return items[a].Parent < items[b].Parent
			}
			if items[a].Order != items[b].Order {
				return items[a].Order < items[b].Order
			}
			return items[a].ID < items[b].ID
		})
	}
	less(roots)
	for parent := range children {
		less(children[parent])
	}
	result := make([]Node, 0, len(clones))
	var appendSubtree func(Node)
	appendSubtree = func(node Node) {
		result = append(result, node)
		for _, child := range children[node.ID] {
			appendSubtree(child)
		}
	}
	for _, root := range roots {
		appendSubtree(root)
	}
	return result
}

func cloneFieldValues(values []FieldValue) {
	for index := range values {
		values[index].Value = cloneValue(values[index].Value)
	}
}

func cloneFileBindings(values []FileBinding) {
	for index := range values {
		values[index].Path = append([]FieldPathSegment(nil), values[index].Path...)
	}
}

func cloneValue(value Value) Value {
	copy := value
	copy.Inline = cloneInlineItems(value.Inline)
	copy.List = append([]ListItem(nil), value.List...)
	for index := range copy.List {
		copy.List[index].Value = cloneValue(copy.List[index].Value)
	}
	copy.Object = append([]ObjectField(nil), value.Object...)
	for index := range copy.Object {
		copy.Object[index].Value = cloneValue(copy.Object[index].Value)
	}
	return copy
}

func cloneInlineItems(items []InlineItem) []InlineItem {
	copy := append([]InlineItem(nil), items...)
	for index := range copy {
		copy[index].Children = cloneInlineItems(copy[index].Children)
	}
	return copy
}
