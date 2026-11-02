// Package aidocument defines the schema-independent compact document contract
// shared by AI authoring clients and owning-domain adapters.
package aidocument

const ProtocolVersion = "dcdp/1"

type Domain string

const (
	DomainPost          Domain = "post"
	DomainPage          Domain = "page"
	DomainWork          Domain = "work"
	DomainProgramEvent  Domain = "program_event"
	DomainRelease       Domain = "release"
	DomainArtist        Domain = "artist"
	DomainLabel         Domain = "label"
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
	DomainRelease,
	DomainArtist,
	DomainLabel,
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

type InlineKind string

const (
	InlineKindSiteVariable InlineKind = "siteVariable"
	InlineKindText         InlineKind = "t"
	InlineKindBold         InlineKind = "b"
	InlineKindItalic       InlineKind = "em"
	InlineKindUnderline    InlineKind = "u"
	InlineKindStrike       InlineKind = "s"
	InlineKindCode         InlineKind = "code"
	InlineKindTextColor    InlineKind = "fg"
	InlineKindBackground   InlineKind = "bg"
	InlineKindLink         InlineKind = "a"
	InlineKindHardBreak    InlineKind = "br"
	InlineKindMath         InlineKind = "math"
	InlineKindPlaceholder  InlineKind = "ph"
)

// InlineItem is the closed compact rich-text vocabulary. It preserves common
// inline semantics without exposing an editor-native document representation.
type InlineItem struct {
	Linked   bool
	Kind     InlineKind
	Text     string
	Target   string
	Children []InlineItem
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
// generic DCDP validator. Generated constraints are also exposed for discovery;
// owning-domain required/default constraints stay at the compiler boundary.
type FieldSchema struct {
	EnumValues       []any
	ExclusiveMinimum *float64
	Minimum          *float64
	Maximum          *float64
	Required         bool
	Kind             ValueKind
	Ownership        FieldOwnership
	Translatable     bool
	File             bool
	Item             *FieldSchema
	Fields           []NestedFieldRule
	Identity         ListIdentityRule
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

type BlockKindRule struct {
	Kind           BlockKind
	Category       string
	AllowedParents []BlockKind
}

type Catalog struct {
	BlockRules     []BlockKindRule
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
