package aidocument

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Projection is the canonical read result. A field absent from a node
// is missing; a present text field whose value is "" is explicitly empty.
type Projection struct {
	Protocol         string            `json:"-"`
	Profile          Domain            `json:"document_type"`
	Catalog          string            `json:"catalog_fingerprint"`
	Document         DocumentReference `json:"document_id"`
	DocumentRevision Revision          `json:"document_revision"`
	TargetRevision   *Revision         `json:"target_revision,omitempty"`
	SourceLocale     Locale            `json:"source_locale"`
	Locale           Locale            `json:"locale"`
	LocaleRole       LocaleRole        `json:"locale_role"`
	LocaleExists     bool              `json:"locale_exists"`
	Mode             ReadMode          `json:"mode"`
	// Query is the outline search that selected Nodes, if any.
	Query string `json:"query,omitempty"`
	Nodes []Node `json:"blocks"`
	// Previews holds each outline node's locale text preview: its start, or
	// the text around the match for a search. EncodeProjection writes it into
	// the outline block; other read modes return full values instead.
	Previews map[BlockID]string `json:"-"`
	Next     *Cursor            `json:"next_cursor"`
}

// projectionFields is Projection without its methods, for encoding outline
// blocks in their own shape.
type projectionFields Projection

// outlineBlock is one block of an outline read: where it sits and a preview of
// its text, without its field values.
type outlineBlock struct {
	ID        BlockID           `json:"block_id"`
	Kind      BlockKind         `json:"kind"`
	Parent    BlockID           `json:"parent_block_id,omitempty"`
	Order     int               `json:"order"`
	Preview   string            `json:"preview,omitempty"`
	Relations []outlineRelation `json:"relations,omitempty"`
}

type outlineRelation struct {
	ID    RelationID            `json:"relation_id"`
	Items []outlineRelationItem `json:"items"`
}

type outlineRelationItem struct {
	ID    RelationItemID   `json:"item_id"`
	Kind  RelationItemKind `json:"kind"`
	Order int              `json:"order"`
}

func newOutlineBlock(node Node, preview string) outlineBlock {
	block := outlineBlock{ID: node.ID, Kind: node.Kind, Parent: node.Parent, Order: node.Order, Preview: preview}
	for _, relation := range node.Relations {
		items := make([]outlineRelationItem, 0, len(relation.Items))
		for _, item := range relation.Items {
			items = append(items, outlineRelationItem{ID: item.ID, Kind: item.Kind, Order: item.Order})
		}
		block.Relations = append(block.Relations, outlineRelation{ID: relation.ID, Items: items})
	}
	return block
}

func EncodeOpenMetadata(metadata OpenMetadata) ([]byte, error) {
	if err := metadata.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(metadata)
}

func (p Projection) Identity() DocumentIdentity {
	return DocumentIdentity{Domain: p.Profile, Reference: p.Document}
}

func (p Projection) validate() error {
	if p.Protocol != ProtocolVersion {
		return fmt.Errorf("unsupported protocol %q", p.Protocol)
	}
	if err := p.Identity().validate(); err != nil {
		return err
	}
	if err := validateOpaque("catalog fingerprint", p.Catalog, 256); err != nil {
		return err
	}
	if err := validateOpaque("document revision", string(p.DocumentRevision), 256); err != nil {
		return err
	}
	if p.TargetRevision != nil {
		if err := validateOpaque("target revision", string(*p.TargetRevision), 256); err != nil {
			return err
		}
	}
	if err := validateLocale(p.SourceLocale); err != nil {
		return err
	}
	if err := validateLocale(p.Locale); err != nil {
		return err
	}
	if p.LocaleRole != deriveLocaleRole(p.SourceLocale, p.Locale) {
		return errors.New("locale role does not match source and requested locales")
	}
	if err := validateLocaleState(p.LocaleRole, p.LocaleExists, p.TargetRevision); err != nil {
		return err
	}
	if p.LocaleRole == LocaleRoleNonSource && !p.LocaleExists {
		if err := validateAbsentLocaleValues(p.Nodes); err != nil {
			return err
		}
	}
	if !p.Mode.valid() {
		return fmt.Errorf("unsupported read mode %q", p.Mode)
	}
	return validateProjectedNodes(p.Nodes)
}

func EncodeProjection(projection Projection) ([]byte, error) {
	if err := projection.validate(); err != nil {
		return nil, err
	}
	projection.Nodes = canonicalNodes(projection.Nodes)
	if projection.Mode != ReadOutline {
		return json.Marshal(projection)
	}
	blocks := make([]outlineBlock, len(projection.Nodes))
	for index, node := range projection.Nodes {
		blocks[index] = newOutlineBlock(node, projection.Previews[node.ID])
	}
	return json.Marshal(struct {
		projectionFields
		Blocks []outlineBlock `json:"blocks"`
	}{projectionFields(projection), blocks})
}

func EncodeApplyRequest(request ApplyRequest) ([]byte, error) {
	if err := request.validateEnvelope(); err != nil {
		return nil, err
	}
	if err := validateCanonicalOperations(request.Operations); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func DecodeApplyRequest(data []byte) (ApplyRequest, error) {
	var request ApplyRequest
	if err := decodeStrict(data, &request); err != nil {
		return ApplyRequest{}, err
	}
	request.Protocol = ProtocolVersion
	if err := request.validateEnvelope(); err != nil {
		return ApplyRequest{}, err
	}
	if err := validateCanonicalOperations(request.Operations); err != nil {
		return ApplyRequest{}, err
	}
	return request, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

// wireValue is the JSON shape of Value: type names the one payload it carries.
type wireValue struct {
	Type    ValueKind      `json:"type"`
	Text    *string        `json:"text,omitempty"`
	Number  *string        `json:"number,omitempty"`
	Boolean *bool          `json:"boolean,omitempty"`
	Content *[]InlineItem  `json:"content,omitempty"`
	Items   *[]ListItem    `json:"items,omitempty"`
	Fields  *[]ObjectField `json:"fields,omitempty"`
}

func (v Value) MarshalJSON() ([]byte, error) {
	if err := v.validate(); err != nil {
		return nil, err
	}
	wire := wireValue{Type: v.Kind}
	switch v.Kind {
	case ValueKindBoolean:
		wire.Boolean = &v.Boolean
	case ValueKindInline:
		wire.Content = nonNilSlice(v.Inline)
	case ValueKindList:
		wire.Items = nonNilSlice(v.List)
	case ValueKindObject:
		wire.Fields = nonNilSlice(v.Object)
	case ValueKindNumber:
		wire.Number = &v.Text
	default:
		wire.Text = &v.Text
	}
	return json.Marshal(wire)
}

func (v *Value) UnmarshalJSON(data []byte) error {
	*v = Value{}
	var wire wireValue
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("value: %w", err)
	}
	v.Kind = wire.Type
	present := countPresent(wire.Text != nil, wire.Number != nil, wire.Boolean != nil, wire.Content != nil, wire.Items != nil, wire.Fields != nil)
	var matches bool
	switch wire.Type {
	case ValueKindText:
		if matches = wire.Text != nil; matches {
			v.Text = *wire.Text
		}
	case ValueKindNumber:
		if matches = wire.Number != nil; matches {
			v.Text = *wire.Number
		}
	case ValueKindBoolean:
		if matches = wire.Boolean != nil; matches {
			v.Boolean = *wire.Boolean
		}
	case ValueKindInline:
		if matches = wire.Content != nil; matches {
			v.Inline = *wire.Content
		}
	case ValueKindList:
		if matches = wire.Items != nil; matches {
			v.List = *wire.Items
		}
	case ValueKindObject:
		if matches = wire.Fields != nil; matches {
			v.Object = *wire.Fields
		}
	default:
		return fmt.Errorf("unsupported value type %q", wire.Type)
	}
	if !matches || present != 1 {
		return fmt.Errorf("value of type %q needs exactly its %s field", wire.Type, valuePayloadName(wire.Type))
	}
	return v.validate()
}

func valuePayloadName(kind ValueKind) string {
	switch kind {
	case ValueKindInline:
		return "content"
	case ValueKindList:
		return "items"
	case ValueKindObject:
		return "fields"
	default:
		return string(kind)
	}
}

func countPresent(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

// nonNilSlice writes a nil slice as an empty JSON array.
func nonNilSlice[T any](items []T) *[]T {
	if items == nil {
		items = []T{}
	}
	return &items
}

// wireInlineItem is the JSON shape of InlineItem. type decides which of the
// other fields it carries.
type wireInlineItem struct {
	Type       InlineKind    `json:"type"`
	Text       *string       `json:"text,omitempty"`
	Key        *string       `json:"key,omitempty"`
	Linked     *bool         `json:"linked,omitempty"`
	Expression *string       `json:"expression,omitempty"`
	Handle     *string       `json:"handle,omitempty"`
	URL        *string       `json:"url,omitempty"`
	Color      *string       `json:"color,omitempty"`
	Content    *[]InlineItem `json:"content,omitempty"`
}

func (i InlineItem) MarshalJSON() ([]byte, error) {
	if err := i.validate(0); err != nil {
		return nil, err
	}
	wire := wireInlineItem{Type: i.Kind}
	switch i.Kind {
	case InlineKindText:
		wire.Text = &i.Text
	case InlineKindSiteVariable:
		wire.Key = &i.Text
		if i.Linked {
			wire.Linked = &i.Linked
		}
	case InlineKindMath:
		wire.Expression = &i.Text
	case InlineKindPlaceholder:
		wire.Handle = &i.Text
	case InlineKindBold, InlineKindItalic, InlineKindUnderline, InlineKindStrike, InlineKindCode:
		wire.Content = nonNilSlice(i.Children)
	case InlineKindTextColor, InlineKindBackground:
		wire.Color, wire.Content = &i.Target, nonNilSlice(i.Children)
	case InlineKindLink:
		wire.URL, wire.Content = &i.Target, nonNilSlice(i.Children)
	case InlineKindHardBreak:
	default:
		return nil, fmt.Errorf("unsupported inline item type %q", i.Kind)
	}
	return json.Marshal(wire)
}

func (i *InlineItem) UnmarshalJSON(data []byte) error {
	*i = InlineItem{}
	var wire wireInlineItem
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("inline item: %w", err)
	}
	i.Kind = wire.Type
	present := countPresent(wire.Text != nil, wire.Key != nil, wire.Linked != nil, wire.Expression != nil, wire.Handle != nil, wire.URL != nil, wire.Color != nil, wire.Content != nil)
	var valid bool
	switch wire.Type {
	case InlineKindText:
		valid = wire.Text != nil && present == 1
		i.Text = derefString(wire.Text)
	case InlineKindSiteVariable:
		valid = wire.Key != nil && present == 1+countPresent(wire.Linked != nil)
		i.Text = derefString(wire.Key)
		if wire.Linked != nil {
			if !*wire.Linked {
				return errors.New("site_variable linked must be true or left out")
			}
			i.Linked = true
		}
	case InlineKindMath:
		valid = wire.Expression != nil && present == 1
		i.Text = derefString(wire.Expression)
	case InlineKindPlaceholder:
		valid = wire.Handle != nil && present == 1
		i.Text = derefString(wire.Handle)
	case InlineKindBold, InlineKindItalic, InlineKindUnderline, InlineKindStrike, InlineKindCode:
		valid = wire.Content != nil && present == 1
	case InlineKindTextColor, InlineKindBackground:
		valid = wire.Color != nil && wire.Content != nil && present == 2
		i.Target = derefString(wire.Color)
	case InlineKindLink:
		valid = wire.URL != nil && wire.Content != nil && present == 2
		i.Target = derefString(wire.URL)
	case InlineKindHardBreak:
		valid = present == 0
	default:
		return fmt.Errorf("unsupported inline item type %q", wire.Type)
	}
	if !valid {
		return fmt.Errorf("inline item of type %q has missing or extra fields", wire.Type)
	}
	if wire.Content != nil {
		i.Children = *wire.Content
	}
	return i.validate(0)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (i ListItem) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID    RelationItemID `json:"item_id,omitempty"`
		Value Value          `json:"value"`
	}{i.ID, i.Value})
}

func (i *ListItem) UnmarshalJSON(data []byte) error {
	*i = ListItem{}
	var wire struct {
		ID    RelationItemID `json:"item_id"`
		Value *Value         `json:"value"`
	}
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("list item: %w", err)
	}
	if wire.Value == nil {
		return errors.New("list item needs value")
	}
	i.ID, i.Value = wire.ID, *wire.Value
	return nil
}

func (f ObjectField) MarshalJSON() ([]byte, error) {
	return marshalNamedField(f.ID, f.Value)
}

func (f *ObjectField) UnmarshalJSON(data []byte) error {
	id, value, err := unmarshalNamedField(data)
	f.ID, f.Value = id, value
	return err
}

func (f FieldValue) MarshalJSON() ([]byte, error) {
	return marshalNamedField(f.ID, f.Value)
}

func (f *FieldValue) UnmarshalJSON(data []byte) error {
	id, value, err := unmarshalNamedField(data)
	f.ID, f.Value = id, value
	return err
}

func marshalNamedField(id FieldID, value Value) ([]byte, error) {
	return json.Marshal(struct {
		Field FieldID `json:"field"`
		Value Value   `json:"value"`
	}{id, value})
}

func unmarshalNamedField(data []byte) (FieldID, Value, error) {
	var wire struct {
		Field FieldID `json:"field"`
		Value *Value  `json:"value"`
	}
	if err := decodeStrict(data, &wire); err != nil {
		return "", Value{}, fmt.Errorf("field: %w", err)
	}
	if wire.Field == "" || wire.Value == nil {
		return "", Value{}, errors.New("field needs field and value")
	}
	return wire.Field, *wire.Value, nil
}

type wireFileBinding struct {
	Field FieldID            `json:"field"`
	Path  []FieldPathSegment `json:"path,omitempty"`
	File  FileReference      `json:"file_id"`
}

func (f FileBinding) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireFileBinding(f))
}

func (f *FileBinding) UnmarshalJSON(data []byte) error {
	var wire wireFileBinding
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("file binding: %w", err)
	}
	f.Field, f.Path, f.File = wire.Field, wire.Path, wire.File
	return nil
}

type wireNode struct {
	ID        BlockID       `json:"block_id"`
	Kind      BlockKind     `json:"kind"`
	Parent    BlockID       `json:"parent_block_id,omitempty"`
	Order     int           `json:"order"`
	Shared    []FieldValue  `json:"shared_fields,omitempty"`
	Localized []FieldValue  `json:"localized_fields,omitempty"`
	Files     []FileBinding `json:"files,omitempty"`
	Relations []Relation    `json:"relations,omitempty"`
}

func (n Node) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireNode(n))
}

func (n *Node) UnmarshalJSON(data []byte) error {
	var wire wireNode
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("block: %w", err)
	}
	*n = Node(wire)
	return nil
}

type wireRelation struct {
	ID    RelationID     `json:"relation_id"`
	Items []RelationItem `json:"items"`
}

func (r Relation) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireRelation{r.ID, *nonNilSlice(r.Items)})
}

func (r *Relation) UnmarshalJSON(data []byte) error {
	var wire wireRelation
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("relation: %w", err)
	}
	*r = Relation(wire)
	return nil
}

type wireRelationItem struct {
	ID        RelationItemID   `json:"item_id"`
	Kind      RelationItemKind `json:"kind"`
	Order     int              `json:"order"`
	Shared    []FieldValue     `json:"shared_fields,omitempty"`
	Localized []FieldValue     `json:"localized_fields,omitempty"`
	Files     []FileBinding    `json:"files,omitempty"`
}

func (i RelationItem) MarshalJSON() ([]byte, error) {
	return json.Marshal(wireRelationItem(i))
}

func (i *RelationItem) UnmarshalJSON(data []byte) error {
	var wire wireRelationItem
	if err := decodeStrict(data, &wire); err != nil {
		return fmt.Errorf("relation item: %w", err)
	}
	*i = RelationItem(wire)
	return nil
}
