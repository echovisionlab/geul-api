package aidocumentadapter

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	pageSectionSettingsField core.FieldID   = "settings"
	pageSectionDataField     core.FieldID   = "data"
	pageSectionLocaleField   core.FieldID   = "locale-data"
	pageColumnBlockKind      core.BlockKind = "page-column"
	pageColumnRatioField     core.FieldID   = "ratio"
)

// PageCodec is the Page-only generated section/tree adapter. Generated proto
// descriptors provide the closed shape; BatchFromPageProto remains the final
// authority for defaults, enums, bounds, topology and File constraints.
type PageCodec struct {
	rich       *RichTextCodec
	catalog    core.Catalog
	baseCases  map[core.BlockKind]protoreflect.FieldDescriptor
	localeCase map[core.BlockKind]protoreflect.FieldDescriptor
}

func NewPageCodec() (*PageCodec, error) {
	rich, err := NewRichTextCodec(contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE)
	if err != nil {
		return nil, err
	}
	baseOneof := (&contentv1.PageSection{}).ProtoReflect().Descriptor().Oneofs().ByName("value")
	localeOneof := (&contentv1.PageSectionLocale{}).ProtoReflect().Descriptor().Oneofs().ByName("value")
	if baseOneof == nil || localeOneof == nil {
		return nil, errors.New("generated Page section value descriptors are required")
	}
	codec := &PageCodec{
		rich: rich, baseCases: make(map[core.BlockKind]protoreflect.FieldDescriptor),
		localeCase: make(map[core.BlockKind]protoreflect.FieldDescriptor),
	}
	for index := 0; index < baseOneof.Fields().Len(); index++ {
		field := baseOneof.Fields().Get(index)
		codec.baseCases[pageKind(field.JSONName())] = field
	}
	for index := 0; index < localeOneof.Fields().Len(); index++ {
		field := localeOneof.Fields().Get(index)
		codec.localeCase[pageKind(field.JSONName())] = field
	}
	catalog := rich.Catalog()
	catalog.BlockKinds = append(catalog.BlockKinds, pageColumnBlockKind)
	catalog.Fields = append(catalog.Fields, core.FieldRule{
		BlockKind: pageColumnBlockKind, Field: pageColumnRatioField,
		ValueKind: core.ValueKindNumber, Ownership: core.FieldOwnershipShared,
	})
	for kind, field := range codec.baseCases {
		catalog.BlockKinds = append(catalog.BlockKinds, kind)
		settingsSchema, err := pageMessageSchema((&contentv1.PageSectionSettings{}).ProtoReflect().Descriptor(), core.FieldOwnershipShared)
		if err != nil {
			return nil, err
		}
		catalog.Fields = append(catalog.Fields, core.FieldRule{
			BlockKind: kind, Field: pageSectionSettingsField, ValueKind: core.ValueKindObject,
			Ownership: core.FieldOwnershipShared, Schema: &settingsSchema,
		})
		dataSchema, err := pageMessageSchema(field.Message(), core.FieldOwnershipShared)
		if err != nil {
			return nil, fmt.Errorf("page section %q: %w", kind, err)
		}
		dataSchema.Fields = pageSchemaWithout(dataSchema.Fields, "blocks")
		if kind == "columns" {
			pageRemoveNestedSchemaField(&dataSchema, "props", "columns")
		}
		if len(dataSchema.Fields) != 0 {
			catalog.Fields = append(catalog.Fields, core.FieldRule{
				BlockKind: kind, Field: pageSectionDataField, ValueKind: core.ValueKindObject,
				Ownership: core.FieldOwnershipShared, Schema: &dataSchema,
			})
		}
		if localeField := codec.localeCase[kind]; localeField != nil {
			localeSchema, err := pageMessageSchema(localeField.Message(), core.FieldOwnershipLocale)
			if err != nil {
				return nil, fmt.Errorf("page locale section %q: %w", kind, err)
			}
			localeSchema.Fields = pageSchemaWithout(localeSchema.Fields, "blocks")
			if len(localeSchema.Fields) != 0 {
				catalog.Fields = append(catalog.Fields, core.FieldRule{
					BlockKind: kind, Field: pageSectionLocaleField, ValueKind: core.ValueKindObject,
					Ownership: core.FieldOwnershipLocale, Translatable: true, Schema: &localeSchema,
				})
			}
		}
	}
	catalog.Fingerprint = contentv1.ContentBlockCatalogFingerprint
	codec.catalog = catalog
	return codec, nil
}

func (c *PageCodec) Catalog() core.Catalog { return c.catalog }

func (c *PageCodec) Project(document *contentv1.LocalizedPageDocument) ([]core.Node, error) {
	if document == nil || document.GetBase() == nil || document.GetLocaleOverlay() == nil {
		return nil, errors.New("localized Page document is required")
	}
	if document.GetBlockCatalogFingerprint() != contentv1.ContentBlockCatalogFingerprint {
		return nil, errors.New("localized Page catalog fingerprint mismatch")
	}
	localized := make(map[string]*contentv1.PageSectionLocale, len(document.GetLocaleOverlay().GetSections()))
	for _, section := range document.GetLocaleOverlay().GetSections() {
		localized[section.GetSectionId()] = section
	}
	columnParents := make(map[string]string)
	for _, stored := range document.GetBase().GetNodes() {
		for _, column := range stored.GetSection().GetColumns().GetProps().GetColumns() {
			columnParents[column.GetId()] = stored.GetSection().GetId()
		}
	}
	nodes := make([]core.Node, 0, len(document.GetBase().GetNodes()))
	for _, stored := range document.GetBase().GetNodes() {
		if stored.GetSection() == nil || stored.GetPlacement() == nil {
			return nil, errors.New("page node requires section and placement")
		}
		kind, base, err := c.sectionMessage(stored.GetSection().ProtoReflect())
		if err != nil {
			return nil, err
		}
		parent := stored.GetPlacement().GetParentSectionId()
		if stored.GetPlacement().GetColumnId() != "" {
			if columnParents[stored.GetPlacement().GetColumnId()] != parent {
				return nil, fmt.Errorf("page section %q references an unknown parent column", stored.GetSection().GetId())
			}
			parent = stored.GetPlacement().GetColumnId()
		}
		node := core.Node{
			ID: core.BlockID(stored.GetSection().GetId()), Kind: kind,
			Parent: core.BlockID(parent), Order: int(stored.GetPlacement().GetIndex()),
		}
		settings, files, err := pageProjectMessage(stored.GetSection().GetSettings().ProtoReflect(), nil)
		if err != nil {
			return nil, fmt.Errorf("project Page section %q settings: %w", node.ID, err)
		}
		node.Shared = append(node.Shared, core.FieldValue{ID: pageSectionSettingsField, Value: settings})
		data, dataFiles, err := pageProjectMessage(base, map[string]bool{"blocks": true})
		if err != nil {
			return nil, fmt.Errorf("project Page section %q: %w", node.ID, err)
		}
		if kind == "columns" {
			pageRemoveNestedObjectField(&data, "props", "columns")
		}
		if len(data.Object) != 0 {
			node.Shared = append(node.Shared, core.FieldValue{ID: pageSectionDataField, Value: data})
		}
		node.Files = append(node.Files, pagePrefixFiles(pageSectionSettingsField, files)...)
		node.Files = append(node.Files, pagePrefixFiles(pageSectionDataField, dataFiles)...)
		localeSection := localized[stored.GetSection().GetId()]
		if localeSection != nil {
			localeKind, localeMessage, localeErr := c.localeSectionMessage(localeSection.ProtoReflect())
			if localeErr != nil || localeKind != kind {
				return nil, fmt.Errorf("project Page locale section %q: kind mismatch: %w", node.ID, localeErr)
			}
			localeData, _, localeErr := pageProjectMessage(localeMessage, map[string]bool{"blocks": true})
			if localeErr != nil {
				return nil, localeErr
			}
			if len(localeData.Object) != 0 {
				node.Localized = append(node.Localized, core.FieldValue{ID: pageSectionLocaleField, Value: localeData})
			}
		}
		nodes = append(nodes, node)
		if stored.GetSection().GetColumns() != nil {
			for index, column := range stored.GetSection().GetColumns().GetProps().GetColumns() {
				nodes = append(nodes, core.Node{
					ID: core.BlockID(column.GetId()), Kind: pageColumnBlockKind,
					Parent: core.BlockID(stored.GetSection().GetId()), Order: index,
					Shared: []core.FieldValue{{ID: pageColumnRatioField, Value: core.Number(strconv.FormatFloat(column.GetRatio(), 'g', -1, 64))}},
				})
			}
		}
		if kind == "rich-text" {
			children, err := c.projectRichTextChildren(document.GetLocale(), stored, localeSection)
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, children...)
		}
	}
	return nodes, nil
}

func (c *PageCodec) projectRichTextChildren(
	locale string,
	stored *contentv1.PageSectionNode,
	localized *contentv1.PageSectionLocale,
) ([]core.Node, error) {
	base := stored.GetSection().GetRichText().GetBlocks()
	if base == nil {
		base = &contentv1.RichTextBlockGraph{}
	}
	overlay := &contentv1.RichTextLocaleOverlay{Locale: locale}
	if localized != nil && localized.GetRichText().GetBlocks() != nil {
		overlay = localized.GetRichText().GetBlocks()
	}
	document := &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: c.rich.Catalog().Fingerprint,
		Profile:                 contentv1.RichTextProfile_RICH_TEXT_PROFILE_PAGE, Locale: locale,
		Base: base, LocaleOverlay: overlay,
	}
	nodes, err := c.rich.Project(document)
	if err != nil {
		return nil, fmt.Errorf("project Page rich text section %q: %w", stored.GetSection().GetId(), err)
	}
	for index := range nodes {
		if nodes[index].Parent == "" {
			nodes[index].Parent = core.BlockID(stored.GetSection().GetId())
		}
	}
	return nodes, nil
}

func (c *PageCodec) sectionMessage(message protoreflect.Message) (core.BlockKind, protoreflect.Message, error) {
	return pageOneofMessage(message, c.baseCases)
}

func (c *PageCodec) localeSectionMessage(message protoreflect.Message) (core.BlockKind, protoreflect.Message, error) {
	return pageOneofMessage(message, c.localeCase)
}

func pageOneofMessage(message protoreflect.Message, cases map[core.BlockKind]protoreflect.FieldDescriptor) (core.BlockKind, protoreflect.Message, error) {
	oneof := message.Descriptor().Oneofs().ByName("value")
	field := message.WhichOneof(oneof)
	if field == nil {
		return "", nil, errors.New("page section kind is required")
	}
	kind := pageKind(field.JSONName())
	if cases[kind] == nil {
		return "", nil, fmt.Errorf("unsupported Page section kind %q", kind)
	}
	return kind, message.Get(field).Message(), nil
}

func pageKind(value string) core.BlockKind {
	var result strings.Builder
	for index, character := range value {
		if unicode.IsUpper(character) && index > 0 {
			result.WriteByte('-')
		}
		result.WriteRune(unicode.ToLower(character))
	}
	return core.BlockKind(result.String())
}

func pageMessageSchema(message protoreflect.MessageDescriptor, ownership core.FieldOwnership) (core.FieldSchema, error) {
	schema := core.FieldSchema{Kind: core.ValueKindObject, Ownership: ownership, Translatable: ownership == core.FieldOwnershipLocale}
	fields := message.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		child, err := pageFieldSchema(field, ownership)
		if err != nil {
			return core.FieldSchema{}, fmt.Errorf("field %q: %w", field.JSONName(), err)
		}
		if child.Kind == core.ValueKindObject && len(child.Fields) == 0 {
			continue
		}
		schema.Fields = append(schema.Fields, core.NestedFieldRule{Field: core.FieldID(field.JSONName()), Schema: child})
	}
	return schema, nil
}

func pageFieldSchema(field protoreflect.FieldDescriptor, ownership core.FieldOwnership) (core.FieldSchema, error) {
	if field.IsList() {
		item, err := pageSingularSchema(field, ownership)
		if err != nil {
			return core.FieldSchema{}, err
		}
		identity := core.ListIdentityRule{Kind: core.ListIdentityPositional}
		if field.Kind() == protoreflect.MessageKind {
			if id := pageIdentityField(field.Message()); id != nil {
				identity = core.ListIdentityRule{Kind: core.ListIdentityField, Field: core.FieldID(id.JSONName())}
			}
		}
		return core.FieldSchema{Kind: core.ValueKindList, Ownership: ownership, Translatable: ownership == core.FieldOwnershipLocale, Item: &item, Identity: identity}, nil
	}
	return pageSingularSchema(field, ownership)
}

func pageSingularSchema(field protoreflect.FieldDescriptor, ownership core.FieldOwnership) (core.FieldSchema, error) {
	schema := core.FieldSchema{Ownership: ownership, Translatable: ownership == core.FieldOwnershipLocale}
	switch field.Kind() {
	case protoreflect.BoolKind:
		schema.Kind = core.ValueKindBoolean
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Uint32Kind, protoreflect.Uint64Kind,
		protoreflect.Sint32Kind, protoreflect.Sint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind, protoreflect.FloatKind, protoreflect.DoubleKind:
		schema.Kind = core.ValueKindNumber
	case protoreflect.StringKind:
		schema.Kind = core.ValueKindText
	case protoreflect.EnumKind:
		if pageEnumUsesNumber(field) {
			schema.Kind = core.ValueKindNumber
		} else {
			schema.Kind = core.ValueKindText
		}
	case protoreflect.MessageKind:
		if pageIsFileAttachment(field.Message()) {
			schema.File = true
			return schema, nil
		}
		return pageMessageSchema(field.Message(), ownership)
	default:
		return core.FieldSchema{}, fmt.Errorf("unsupported proto kind %s", field.Kind())
	}
	return schema, nil
}

func pageSchemaWithout(fields []core.NestedFieldRule, names ...core.FieldID) []core.NestedFieldRule {
	removed := make(map[core.FieldID]struct{}, len(names))
	for _, name := range names {
		removed[name] = struct{}{}
	}
	result := fields[:0]
	for _, field := range fields {
		if _, skip := removed[field.Field]; !skip {
			result = append(result, field)
		}
	}
	return result
}

func pageRemoveNestedSchemaField(schema *core.FieldSchema, object, field core.FieldID) {
	for index := range schema.Fields {
		if schema.Fields[index].Field == object {
			schema.Fields[index].Schema.Fields = pageSchemaWithout(schema.Fields[index].Schema.Fields, field)
			if len(schema.Fields[index].Schema.Fields) == 0 {
				schema.Fields = append(schema.Fields[:index], schema.Fields[index+1:]...)
			}
			return
		}
	}
}

func pageRemoveNestedObjectField(value *core.Value, object, field core.FieldID) {
	for index := range value.Object {
		if value.Object[index].ID != object || value.Object[index].Value.Kind != core.ValueKindObject {
			continue
		}
		fields := value.Object[index].Value.Object[:0]
		for _, candidate := range value.Object[index].Value.Object {
			if candidate.ID != field {
				fields = append(fields, candidate)
			}
		}
		value.Object[index].Value.Object = fields
		if len(fields) == 0 {
			value.Object = append(value.Object[:index], value.Object[index+1:]...)
		}
		return
	}
}

func pageProjectMessage(message protoreflect.Message, skip map[string]bool) (core.Value, []core.FileBinding, error) {
	if !message.IsValid() {
		return core.Object(), nil, nil
	}
	fields := make([]core.ObjectField, 0)
	files := make([]core.FileBinding, 0)
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if skip[field.JSONName()] {
			return true
		}
		projected, nestedFiles, err := pageProjectField(field, value)
		if err != nil {
			files = append(files, core.FileBinding{Field: core.FieldID("!error"), File: core.FileReference(err.Error())})
			return false
		}
		if projected != nil {
			fields = append(fields, core.ObjectValue(core.FieldID(field.JSONName()), *projected))
		}
		for _, file := range nestedFiles {
			file.Path = append([]core.FieldPathSegment{core.ObjectPath(core.FieldID(field.JSONName()))}, file.Path...)
			files = append(files, file)
		}
		return true
	})
	for _, file := range files {
		if file.Field == "!error" {
			return core.Value{}, nil, errors.New(string(file.File))
		}
	}
	return core.Object(fields...), files, nil
}

func pageProjectField(field protoreflect.FieldDescriptor, value protoreflect.Value) (*core.Value, []core.FileBinding, error) {
	if field.IsList() {
		list := value.List()
		items := make([]core.ListItem, 0, list.Len())
		files := make([]core.FileBinding, 0)
		identity := pageIdentityField(field.Message())
		for index := 0; index < list.Len(); index++ {
			itemValue, itemFiles, err := pageProjectSingular(field, list.Get(index))
			if err != nil {
				return nil, nil, err
			}
			item := core.PositionalItem(itemValue)
			if identity != nil {
				handle := list.Get(index).Message().Get(identity).String()
				item = core.StableItem(core.RelationItemID(handle), itemValue)
				for fileIndex := range itemFiles {
					itemFiles[fileIndex].Path = append([]core.FieldPathSegment{core.ListPath(core.RelationItemID(handle))}, itemFiles[fileIndex].Path...)
				}
			}
			items = append(items, item)
			files = append(files, itemFiles...)
		}
		result := core.List(items...)
		return &result, files, nil
	}
	if field.Kind() == protoreflect.MessageKind && pageIsFileAttachment(field.Message()) {
		fileField := findMessageField(value.Message(), "activeFileId")
		if fileField == nil || !value.Message().Has(fileField) || value.Message().Get(fileField).String() == "" {
			return nil, nil, nil
		}
		return nil, []core.FileBinding{{File: core.FileReference(value.Message().Get(fileField).String())}}, nil
	}
	projected, files, err := pageProjectSingular(field, value)
	if err == nil && projected.Kind == core.ValueKindObject && len(projected.Object) == 0 {
		return nil, files, nil
	}
	return &projected, files, err
}

func pageProjectSingular(field protoreflect.FieldDescriptor, value protoreflect.Value) (core.Value, []core.FileBinding, error) {
	switch field.Kind() {
	case protoreflect.BoolKind:
		return core.Boolean(value.Bool()), nil, nil
	case protoreflect.StringKind:
		return core.Text(value.String()), nil, nil
	case protoreflect.EnumKind:
		enum := field.Enum().Values().ByNumber(value.Enum())
		canonical, err := pageCanonicalEnum(field, enum)
		if err != nil {
			return core.Value{}, nil, err
		}
		if pageEnumUsesNumber(field) {
			return core.Number(canonical), nil, nil
		}
		return core.Text(canonical), nil, nil
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind,
		protoreflect.Sfixed32Kind, protoreflect.Sfixed64Kind:
		return core.Number(strconv.FormatInt(value.Int(), 10)), nil, nil
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind, protoreflect.Fixed32Kind, protoreflect.Fixed64Kind:
		return core.Number(strconv.FormatUint(value.Uint(), 10)), nil, nil
	case protoreflect.FloatKind:
		return core.Number(strconv.FormatFloat(value.Float(), 'g', -1, 32)), nil, nil
	case protoreflect.DoubleKind:
		return core.Number(strconv.FormatFloat(value.Float(), 'g', -1, 64)), nil, nil
	case protoreflect.MessageKind:
		return pageProjectMessage(value.Message(), nil)
	default:
		return core.Value{}, nil, fmt.Errorf("unsupported proto kind %s", field.Kind())
	}
}

func pageCanonicalEnum(field protoreflect.FieldDescriptor, value protoreflect.EnumValueDescriptor) (string, error) {
	raw := string(value.Name())
	for _, canonical := range pageEnumCanonicalValues {
		if pageGeneratedEnumName(field, canonical) == raw {
			return canonical, nil
		}
	}
	return "", fmt.Errorf("enum value %q is not a user-facing Page value", raw)
}

var pageEnumCanonicalValues = func() []string {
	values := map[string]struct{}{"full": {}, "container": {}, "narrow": {}}
	var collect func([]contentv1.ContentFieldDescriptor)
	collect = func(fields []contentv1.ContentFieldDescriptor) {
		for _, field := range fields {
			for _, value := range field.Values {
				switch typed := value.(type) {
				case string:
					values[typed] = struct{}{}
				case float64:
					values[strconv.FormatFloat(typed, 'f', -1, 64)] = struct{}{}
				case int:
					values[strconv.Itoa(typed)] = struct{}{}
				}
			}
			if field.Item != nil {
				collect([]contentv1.ContentFieldDescriptor{*field.Item})
			}
			collect(field.Fields)
		}
	}
	catalog := contentv1.DescribePageCatalog()
	for _, section := range catalog.Sections {
		collect(section.Fields)
	}
	collect(catalog.ImmersiveUnitFields)
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}()

func pageEnumUsesNumber(field protoreflect.FieldDescriptor) bool {
	prefix := pageUpperSnake(field.JSONName())
	if field.IsList() {
		prefix += "_ITEM"
	}
	prefix += "_"
	values := field.Enum().Values()
	seen := false
	for index := 0; index < values.Len(); index++ {
		raw := string(values.Get(index).Name())
		if strings.HasSuffix(raw, "_UNSPECIFIED") {
			continue
		}
		token, ok := strings.CutPrefix(raw, prefix)
		if !ok || token == "" {
			return false
		}
		if _, err := strconv.ParseUint(token, 10, 64); err != nil {
			return false
		}
		seen = true
	}
	return seen
}

func pageUpperSnake(value string) string {
	var result strings.Builder
	for index, character := range value {
		if unicode.IsUpper(character) && index > 0 {
			result.WriteByte('_')
		}
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(unicode.ToUpper(character))
		} else if result.Len() > 0 {
			result.WriteByte('_')
		}
	}
	return strings.Trim(result.String(), "_")
}

func pageGeneratedEnumName(field protoreflect.FieldDescriptor, canonical string) string {
	token := pageUpperSnake(canonical)
	if token != "" && unicode.IsDigit(rune(token[0])) && !pageEnumUsesNumber(field) {
		token = "X_" + token
	}
	prefix := pageUpperSnake(field.JSONName())
	if field.IsList() {
		prefix += "_ITEM"
	}
	return prefix + "_" + token
}

func pagePrefixFiles(field core.FieldID, files []core.FileBinding) []core.FileBinding {
	for index := range files {
		files[index].Field = field
	}
	return files
}

func pageIdentityField(message protoreflect.MessageDescriptor) protoreflect.FieldDescriptor {
	if message == nil {
		return nil
	}
	for _, name := range []string{"id", "unitId", "rowId", "cellId"} {
		if field := findMessageFieldDescriptor(message, name); field != nil && field.Kind() == protoreflect.StringKind {
			return field
		}
	}
	return nil
}

func findMessageFieldDescriptor(message protoreflect.MessageDescriptor, jsonName string) protoreflect.FieldDescriptor {
	fields := message.Fields()
	for index := 0; index < fields.Len(); index++ {
		if fields.Get(index).JSONName() == jsonName {
			return fields.Get(index)
		}
	}
	return nil
}

func pageIsFileAttachment(message protoreflect.MessageDescriptor) bool {
	return message != nil && string(message.FullName()) == "api.content.v1.FileAttachment"
}
