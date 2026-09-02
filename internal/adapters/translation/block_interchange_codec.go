package translationadapter

import (
	"fmt"
	"sort"
	"strings"

	core "github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// projectBlockInterchangeTargets projects only values that are durably present
// in a sparse target overlay. The source plan remains the stable-unit
// authority: target Blocks and fields removed from the current source graph are
// ignored, while an explicitly present empty value remains a map entry.
func projectBlockInterchangeTargets(
	plan *core.ExtractionPlan,
	localized *contentv1.LocalizedRichTextDocument,
) (map[string]core.UnitResult, error) {
	if err := validateBlockInterchangeDocument(plan, localized, plan.TargetLocale); err != nil {
		return nil, err
	}

	known := blockInterchangeUnits(plan)
	targetBlocks := richTextLocaleBlocks(localized.GetLocaleOverlay())
	baseBlocks := richTextBaseBlocks(localized.GetBase())
	present := make(map[string]struct{}, len(known))
	extracted := make(map[string]core.Unit, len(known))

	for blockID, target := range targetBlocks {
		base := baseBlocks[blockID]
		if base == nil {
			continue
		}
		normalized, err := normalizeSparseTableBlock(target, base)
		if err != nil {
			return nil, err
		}
		units, err := core.ExtractRichTextUnits(normalized, core.RichTextUnitScope{
			EntityType: plan.EntityType, EntityID: plan.EntityID, SourceLocale: plan.TargetLocale,
			ContainerID: blockID, UnitPrefix: "block:" + blockID, PathPrefix: "block:" + blockID,
		})
		if err != nil {
			return nil, fmt.Errorf("project Block interchange target %s: %w", blockID, err)
		}
		for _, unit := range units {
			if _, ok := known[unit.UnitID]; ok {
				extracted[unit.UnitID] = unit
			}
		}
	}

	for handle, unit := range known {
		target := targetBlocks[unit.ContainerID]
		base := baseBlocks[unit.ContainerID]
		if target == nil || base == nil {
			continue
		}
		path, err := blockInterchangeUnitPath(unit)
		if err != nil {
			return nil, err
		}
		exists, err := sparseBlockPathExists(target.ProtoReflect(), base.ProtoReflect(), path)
		if err != nil {
			return nil, fmt.Errorf("project Block interchange unit %q: %w", handle, err)
		}
		if exists {
			present[handle] = struct{}{}
		}
	}

	result := make(map[string]core.UnitResult, len(present))
	for handle := range present {
		source := known[handle]
		if target, ok := extracted[handle]; ok {
			result[handle] = core.UnitResult{
				UnitID: handle, TranslatedText: target.SourceText,
				OriginalData: cloneOriginalData(source.OriginalData),
				TargetInline: cloneXLIFFInline(target.SourceInline),
			}
			continue
		}
		emptyInline := emptyXLIFFInline(source.SourceInline)
		text, err := core.ProjectXLIFFInline(emptyInline, source.OriginalData)
		if err != nil {
			return nil, fmt.Errorf("project explicitly empty Block interchange unit %q: %w", handle, err)
		}
		result[handle] = core.UnitResult{
			UnitID: handle, TranslatedText: text,
			OriginalData: cloneOriginalData(source.OriginalData), TargetInline: emptyInline,
		}
	}
	return result, nil
}

// ProjectRichTextInterchangeTargets is the shared adapter codec for projecting
// a raw sparse Rich Text target document onto the stable units in plan. Plans
// for nested domain containers must first be scoped so their Rich Text unit IDs
// and ContainerIDs address the supplied document's Blocks.
func ProjectRichTextInterchangeTargets(
	plan *core.ExtractionPlan,
	document *contentv1.LocalizedRichTextDocument,
) (map[string]core.UnitResult, error) {
	return projectBlockInterchangeTargets(plan, document)
}

// CloneInterchangeUnitResult returns a deep copy suitable for PATCH maps.
func CloneInterchangeUnitResult(result core.UnitResult) core.UnitResult {
	result.OriginalData = cloneOriginalData(result.OriginalData)
	result.TargetInline = cloneXLIFFInline(result.TargetInline)
	return result
}

// EmptyInterchangeTargetInline preserves inline code identity while clearing
// only translatable text. It is used when proto presence represents an
// explicitly stored empty target.
func EmptyInterchangeTargetInline(source []core.XLIFFInline) []core.XLIFFInline {
	return emptyXLIFFInline(source)
}

// InterchangeProtoPathPresent reports raw proto field presence without source
// fallback. Repeated Rich Text content fields use their schema-defined empty
// value as explicit presence.
func InterchangeProtoPathPresent(message protoreflect.Message, path []string) bool {
	if !message.IsValid() || len(path) == 0 {
		return false
	}
	for index := 0; index < len(path); index++ {
		field := message.Descriptor().Fields().ByName(protoreflect.Name(path[index]))
		if field == nil {
			return false
		}
		last := index == len(path)-1
		if field.IsList() {
			if last {
				return message.Has(field) || interchangeListPresence(message, field)
			}
			index++
			if index >= len(path) || field.Kind() != protoreflect.MessageKind {
				return false
			}
			list := message.Get(field).List()
			item, ok := interchangeListMessageByPathSegment(list, path[index])
			if !ok {
				return false
			}
			message = item
			continue
		}
		if last {
			if message.Has(field) {
				return true
			}
			return string(field.Name()) == "content" &&
				string(message.Descriptor().Name()) == "CodeBlockBlockLocale"
		}
		if field.Kind() != protoreflect.MessageKind || !message.Has(field) {
			return false
		}
		message = message.Get(field).Message()
	}
	return false
}

func interchangeListPresence(message protoreflect.Message, field protoreflect.FieldDescriptor) bool {
	if string(field.Name()) != "content" {
		return false
	}
	switch string(message.Descriptor().Name()) {
	case "ParagraphBlockLocale", "HeadingBlockLocale", "BulletListItemBlockLocale",
		"NumberedListItemBlockLocale", "CheckListItemBlockLocale", "QuoteBlockLocale",
		"RichTextTableCellLocale":
		return true
	default:
		return false
	}
}

// buildBlockInterchangePatch compiles a sparse locale overlay containing only
// Blocks touched by the imported unit set. Existing target-only presentation
// and untranslated values are retained; a newly addressed path is created
// from the current source shape without copying sibling source values.
func buildBlockInterchangePatch(
	plan *core.ExtractionPlan,
	source *contentv1.LocalizedRichTextDocument,
	current *contentv1.LocalizedRichTextDocument,
	imported map[string]core.UnitResult,
) (*contentv1.RichTextLocaleOverlay, error) {
	if err := validateBlockInterchangeDocument(plan, source, plan.SourceLocale); err != nil {
		return nil, err
	}
	if err := validateBlockInterchangeDocument(plan, current, plan.TargetLocale); err != nil {
		return nil, err
	}
	if source.GetProfile() != current.GetProfile() ||
		source.GetBlockCatalogFingerprint() != current.GetBlockCatalogFingerprint() ||
		!proto.Equal(source.GetBase(), current.GetBase()) {
		return nil, fmt.Errorf("block interchange source and target graphs do not match")
	}

	known := blockInterchangeUnits(plan)
	byBlock := make(map[string]map[string]core.UnitResult)
	for handle, value := range imported {
		unit, ok := known[handle]
		if !ok {
			continue
		}
		if value.UnitID != "" && value.UnitID != handle {
			return nil, fmt.Errorf("block interchange unit %q result identity does not match", handle)
		}
		if byBlock[unit.ContainerID] == nil {
			byBlock[unit.ContainerID] = make(map[string]core.UnitResult)
		}
		value.UnitID = handle
		byBlock[unit.ContainerID][handle] = value
	}

	overlay := &contentv1.RichTextLocaleOverlay{Locale: plan.TargetLocale}
	if len(byBlock) == 0 {
		return overlay, nil
	}
	sourceBlocks := richTextLocaleBlocks(source.GetLocaleOverlay())
	currentBlocks := richTextLocaleBlocks(current.GetLocaleOverlay())
	for _, sourceBlock := range source.GetLocaleOverlay().GetBlocks() {
		blockID := sourceBlock.GetBlockId()
		updates := byBlock[blockID]
		if len(updates) == 0 {
			continue
		}
		converted := proto.Clone(sourceBlocks[blockID]).(*contentv1.RichTextBlockLocale)
		if err := core.ApplyRichTextResults(converted, "block:"+blockID, updates); err != nil {
			return nil, fmt.Errorf("apply Block interchange patch %s: %w", blockID, err)
		}
		destination := &contentv1.RichTextBlockLocale{BlockId: blockID}
		if existing := currentBlocks[blockID]; existing != nil {
			destination = proto.Clone(existing).(*contentv1.RichTextBlockLocale)
		}
		if err := requireMatchingBlockLocaleKind(sourceBlock, destination); err != nil {
			return nil, err
		}

		handles := make([]string, 0, len(updates))
		for handle := range updates {
			handles = append(handles, handle)
		}
		sort.Strings(handles)
		for _, handle := range handles {
			path, err := blockInterchangeUnitPath(known[handle])
			if err != nil {
				return nil, err
			}
			if err := core.CopyStableProtoPath(destination.ProtoReflect(), converted.ProtoReflect(), path); err != nil {
				return nil, fmt.Errorf("copy Block interchange unit %q: %w", handle, err)
			}
		}
		overlay.Blocks = append(overlay.Blocks, destination)
	}
	if len(overlay.Blocks) != len(byBlock) {
		return nil, fmt.Errorf("block interchange patch references a Block outside the current source graph")
	}
	return overlay, nil
}

// BuildRichTextInterchangePatch compiles imported stable units into a sparse
// target overlay without materializing source values for untouched paths.
func BuildRichTextInterchangePatch(
	plan *core.ExtractionPlan,
	source *contentv1.LocalizedRichTextDocument,
	current *contentv1.LocalizedRichTextDocument,
	imported map[string]core.UnitResult,
) (*contentv1.RichTextLocaleOverlay, error) {
	return buildBlockInterchangePatch(plan, source, current, imported)
}

func validateBlockInterchangeDocument(plan *core.ExtractionPlan, document *contentv1.LocalizedRichTextDocument, locale string) error {
	if plan == nil || strings.TrimSpace(plan.EntityType) == "" || strings.TrimSpace(plan.EntityID) == "" ||
		strings.TrimSpace(plan.SourceLocale) == "" || strings.TrimSpace(plan.TargetLocale) == "" {
		return fmt.Errorf("block interchange plan identity is required")
	}
	if document == nil || document.GetBase() == nil || document.GetLocaleOverlay() == nil {
		return fmt.Errorf("block interchange localized document is required")
	}
	if document.GetLocale() != locale || document.GetLocaleOverlay().GetLocale() != locale {
		return fmt.Errorf("block interchange document locale does not match the plan")
	}
	return nil
}

func blockInterchangeUnits(plan *core.ExtractionPlan) map[string]core.Unit {
	units := make(map[string]core.Unit)
	if plan == nil {
		return units
	}
	for _, unit := range plan.Units {
		if unit.ContainerType == core.ContainerTypeBlock && strings.TrimSpace(unit.ContainerID) != "" {
			units[unit.UnitID] = unit
		}
	}
	return units
}

func blockInterchangeUnitPath(unit core.Unit) ([]string, error) {
	prefix := "block:" + unit.ContainerID + ":typed:"
	if !strings.HasPrefix(unit.UnitID, prefix) {
		return nil, fmt.Errorf("block interchange unit %q has an invalid stable path", unit.UnitID)
	}
	path := strings.Split(strings.TrimPrefix(unit.UnitID, prefix), "/")
	if len(path) == 0 || path[0] == "" {
		return nil, fmt.Errorf("block interchange unit %q has an empty stable path", unit.UnitID)
	}
	return path, nil
}

func richTextLocaleBlocks(overlay *contentv1.RichTextLocaleOverlay) map[string]*contentv1.RichTextBlockLocale {
	result := make(map[string]*contentv1.RichTextBlockLocale)
	for _, block := range overlay.GetBlocks() {
		if block != nil && strings.TrimSpace(block.GetBlockId()) != "" {
			result[block.GetBlockId()] = block
		}
	}
	return result
}

func richTextBaseBlocks(graph *contentv1.RichTextBlockGraph) map[string]*contentv1.RichTextBlock {
	result := make(map[string]*contentv1.RichTextBlock)
	for _, node := range graph.GetNodes() {
		if block := node.GetBlock(); block != nil && strings.TrimSpace(block.GetId()) != "" {
			result[block.GetId()] = block
		}
	}
	return result
}

func normalizeSparseTableBlock(target *contentv1.RichTextBlockLocale, base *contentv1.RichTextBlock) (*contentv1.RichTextBlockLocale, error) {
	result := proto.Clone(target).(*contentv1.RichTextBlockLocale)
	if target.GetTable() == nil {
		return result, nil
	}
	if base.GetTable() == nil || base.GetTable().GetContent() == nil {
		return nil, fmt.Errorf("block %s target kind does not match the current source graph", target.GetBlockId())
	}
	targetRows := make(map[string]*contentv1.RichTextTableRowLocale)
	for _, row := range target.GetTable().GetContent().GetRows() {
		targetRows[row.GetRowId()] = row
	}
	rows := make([]*contentv1.RichTextTableRowLocale, 0, len(base.GetTable().GetContent().GetRows()))
	for _, baseRow := range base.GetTable().GetContent().GetRows() {
		targetRow := targetRows[baseRow.GetId()]
		targetCells := make(map[string]*contentv1.RichTextTableCellLocale)
		if targetRow != nil {
			for _, cell := range targetRow.GetCells() {
				targetCells[cell.GetCellId()] = cell
			}
		}
		row := &contentv1.RichTextTableRowLocale{RowId: baseRow.GetId()}
		for _, baseCell := range baseRow.GetCells() {
			cell := &contentv1.RichTextTableCellLocale{CellId: baseCell.GetId()}
			if targetCell := targetCells[baseCell.GetId()]; targetCell != nil {
				cell = proto.Clone(targetCell).(*contentv1.RichTextTableCellLocale)
			}
			row.Cells = append(row.Cells, cell)
		}
		rows = append(rows, row)
	}
	if result.GetTable().Content == nil {
		result.GetTable().Content = &contentv1.RichTextTableLocale{}
	}
	result.GetTable().Content.Rows = rows
	return result, nil
}

func sparseBlockPathExists(target, base protoreflect.Message, path []string) (bool, error) {
	if len(path) == 0 {
		return true, nil
	}
	field := target.Descriptor().Fields().ByName(protoreflect.Name(path[0]))
	if field == nil {
		return false, fmt.Errorf("unknown target field %q", path[0])
	}
	var baseField protoreflect.FieldDescriptor
	if base.IsValid() {
		baseField = base.Descriptor().Fields().ByName(protoreflect.Name(path[0]))
	}
	if len(path) == 1 {
		if field.IsList() {
			return true, nil
		}
		if field.Kind() == protoreflect.MessageKind || field.HasPresence() {
			return target.Has(field), nil
		}
		return true, nil
	}
	if field.IsList() {
		if baseField == nil || !baseField.IsList() {
			return false, fmt.Errorf("invalid stable list path %q", strings.Join(path, "/"))
		}
		baseList := base.Get(baseField).List()
		if field.Kind() != protoreflect.MessageKind || baseField.Kind() != protoreflect.MessageKind {
			return false, nil
		}
		baseItem, ok := interchangeListMessageByPathSegment(baseList, path[1])
		if !ok {
			return false, nil
		}
		targetItem, ok := matchingListMessage(target.Get(field).List(), baseItem)
		if !ok {
			return false, nil
		}
		return sparseBlockPathExists(targetItem, baseItem, path[2:])
	}
	if field.Kind() != protoreflect.MessageKind || !target.Has(field) {
		return false, nil
	}
	var nextBase protoreflect.Message
	if baseField != nil && baseField.Kind() == protoreflect.MessageKind && base.Has(baseField) {
		nextBase = base.Get(baseField).Message()
	}
	return sparseBlockPathExists(target.Get(field).Message(), nextBase, path[1:])
}

func interchangeListMessageByPathSegment(list protoreflect.List, segment string) (protoreflect.Message, bool) {
	for index := 0; index < list.Len(); index++ {
		candidate := list.Get(index).Message()
		if identity, ok := stableMessageIdentity(candidate); ok && identity == segment {
			return candidate, true
		}
	}
	return nil, false
}

func matchingListMessage(list protoreflect.List, source protoreflect.Message) (protoreflect.Message, bool) {
	sourceID, sourceOK := stableMessageIdentity(source)
	if !sourceOK {
		return nil, false
	}
	for index := 0; index < list.Len(); index++ {
		candidate := list.Get(index).Message()
		if candidateID, ok := stableMessageIdentity(candidate); ok && candidateID == sourceID {
			return candidate, true
		}
	}
	return nil, false
}

func stableMessageIdentity(message protoreflect.Message) (string, bool) {
	if !message.IsValid() {
		return "", false
	}
	for _, name := range []protoreflect.Name{"row_id", "cell_id", "unit_id"} {
		field := message.Descriptor().Fields().ByName(name)
		if field != nil && field.Kind() == protoreflect.StringKind {
			value := strings.TrimSpace(message.Get(field).String())
			return value, value != ""
		}
	}
	switch message.Descriptor().Name() {
	case "RichTextTableRowBase", "RichTextTableCellBase":
		field := message.Descriptor().Fields().ByName("id")
		if field != nil && field.Kind() == protoreflect.StringKind {
			value := strings.TrimSpace(message.Get(field).String())
			return value, value != ""
		}
	}
	return "", false
}

func requireMatchingBlockLocaleKind(source, target *contentv1.RichTextBlockLocale) error {
	if target == nil || target.GetValue() == nil {
		return nil
	}
	sourceValue := source.ProtoReflect().WhichOneof(source.ProtoReflect().Descriptor().Oneofs().ByName("value"))
	targetValue := target.ProtoReflect().WhichOneof(target.ProtoReflect().Descriptor().Oneofs().ByName("value"))
	if sourceValue == nil || targetValue == nil || sourceValue.Name() != targetValue.Name() {
		return fmt.Errorf("block %s target kind does not match the current source graph", source.GetBlockId())
	}
	return nil
}

// CopyInterchangeProtoPath copies one validated stable proto path from source
// into destination. Existing sibling fields remain untouched; list elements
// are matched by their schema-owned stable identity.
func CopyInterchangeProtoPath(destination, source protoreflect.Message, path []string) error {
	return core.CopyStableProtoPath(destination, source, path)
}

func emptyXLIFFInline(source []core.XLIFFInline) []core.XLIFFInline {
	result := make([]core.XLIFFInline, 0, len(source))
	for _, node := range source {
		if node.Kind == core.XLIFFInlineText {
			continue
		}
		copy := node
		copy.Children = emptyXLIFFInline(node.Children)
		result = append(result, copy)
	}
	return result
}

func cloneXLIFFInline(source []core.XLIFFInline) []core.XLIFFInline {
	result := make([]core.XLIFFInline, len(source))
	for index, node := range source {
		result[index] = node
		result[index].Children = cloneXLIFFInline(node.Children)
	}
	return result
}

func cloneOriginalData(source []core.XLIFFOriginalData) []core.XLIFFOriginalData {
	return append([]core.XLIFFOriginalData(nil), source...)
}
