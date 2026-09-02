package translation

import (
	"fmt"
	"strconv"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// RichTextUnitScope supplies the domain identity and stable path prefix for
// translation units extracted from one typed Rich Text Block.
type RichTextUnitScope struct {
	EntityType   string
	EntityID     string
	SourceLocale string
	ContainerID  string
	UnitPrefix   string
	PathPrefix   string
}

// ExtractRichTextUnits projects one typed Rich Text Block into stable XLIFF
// units while preserving styled runs, links, hard breaks, and inline math as
// protected inline codes.
func ExtractRichTextUnits(block *contentv1.RichTextBlockLocale, scope RichTextUnitScope) ([]Unit, error) {
	if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
		return nil, fmt.Errorf("typed Rich Text translation Block ID is required")
	}
	if strings.TrimSpace(scope.EntityType) == "" || strings.TrimSpace(scope.EntityID) == "" ||
		strings.TrimSpace(scope.SourceLocale) == "" || strings.TrimSpace(scope.ContainerID) == "" ||
		strings.TrimSpace(scope.UnitPrefix) == "" || strings.TrimSpace(scope.PathPrefix) == "" {
		return nil, fmt.Errorf("typed Rich Text translation scope is required")
	}

	units := make([]Unit, 0)
	for _, segment := range richTextSemanticSegments(block) {
		sourceText, originalData, sourceInline := richTextSemanticInline(segment.content)
		units = append(units, Unit{
			UnitID:        richTextUnitID(scope.UnitPrefix, segment.path),
			EntityType:    scope.EntityType,
			EntityID:      scope.EntityID,
			Path:          scope.PathPrefix + ":" + strings.Join(segment.path, "."),
			ContainerType: ContainerTypeBlock,
			ContainerID:   scope.ContainerID,
			FieldName:     "content",
			SourceText:    sourceText,
			SourceFormat:  SourceFormatPlainText,
			SourceLocale:  scope.SourceLocale,
			OriginalData:  originalData,
			SourceInline:  sourceInline,
		})
	}
	walkRichTextTranslationStrings(block.ProtoReflect(), nil, func(path []string, sourceText string) {
		units = append(units, Unit{
			UnitID:        richTextUnitID(scope.UnitPrefix, path),
			EntityType:    scope.EntityType,
			EntityID:      scope.EntityID,
			Path:          scope.PathPrefix + ":" + strings.Join(path, "."),
			ContainerType: ContainerTypeBlock,
			ContainerID:   scope.ContainerID,
			FieldName:     path[len(path)-1],
			SourceText:    sourceText,
			SourceFormat:  SourceFormatPlainText,
			SourceLocale:  scope.SourceLocale,
		})
	})
	return units, nil
}

// ApplyRichTextResults applies validated XLIFF results to one cloned target
// Block. The unit prefix must be the same stable prefix used during extraction.
func ApplyRichTextResults(
	block *contentv1.RichTextBlockLocale,
	unitPrefix string,
	results map[string]UnitResult,
) error {
	if block == nil || strings.TrimSpace(block.GetBlockId()) == "" {
		return fmt.Errorf("typed Rich Text translation Block ID is required")
	}
	if strings.TrimSpace(unitPrefix) == "" {
		return fmt.Errorf("typed Rich Text translation unit prefix is required")
	}

	for _, segment := range richTextSemanticSegments(block) {
		result, exists := results[richTextUnitID(unitPrefix, segment.path)]
		if !exists {
			continue
		}
		rebuilt, err := applyRichTextSemanticResult(segment.content, result)
		if err != nil {
			return err
		}
		segment.replace(rebuilt)
	}
	walkMutableRichTextTranslationStrings(block.ProtoReflect(), nil, func(
		path []string,
		message protoreflect.Message,
		field protoreflect.FieldDescriptor,
	) {
		result, exists := results[richTextUnitID(unitPrefix, path)]
		if !exists {
			return
		}
		current := message.Get(field).String()
		message.Set(field, protoreflect.ValueOfString(
			PreserveSourceEdgeWhitespace(current, result.TranslatedText),
		))
	})
	return nil
}

// IsRichTextTranslationStringField reports whether a non-inline protobuf
// string field belongs to the typed Rich Text translation surface.
func IsRichTextTranslationStringField(
	message protoreflect.MessageDescriptor,
	field protoreflect.FieldDescriptor,
) bool {
	messageName := string(message.Name())
	fieldName := string(field.Name())
	if messageName == "RichTextStyledText" {
		return false
	}
	if messageName == "CodeBlockBlockLocale" {
		return fieldName == "content"
	}
	switch fieldName {
	case "title", "caption", "alt":
		return strings.HasSuffix(messageName, "LocaleProps")
	default:
		return false
	}
}

type richTextSemanticSegment struct {
	path    []string
	content []*contentv1.RichTextInline
	replace func([]*contentv1.RichTextInline)
}

func richTextSemanticSegments(block *contentv1.RichTextBlockLocale) []richTextSemanticSegment {
	if block == nil {
		return nil
	}
	if value := block.GetParagraph(); value != nil {
		return []richTextSemanticSegment{{path: []string{"paragraph", "content"}, content: value.GetContent(), replace: func(content []*contentv1.RichTextInline) { value.Content = content }}}
	}
	if value := block.GetHeading(); value != nil {
		return []richTextSemanticSegment{{path: []string{"heading", "content"}, content: value.GetContent(), replace: func(content []*contentv1.RichTextInline) { value.Content = content }}}
	}
	if value := block.GetBulletListItem(); value != nil {
		return []richTextSemanticSegment{{path: []string{"bullet_list_item", "content"}, content: value.GetContent(), replace: func(content []*contentv1.RichTextInline) { value.Content = content }}}
	}
	if value := block.GetNumberedListItem(); value != nil {
		return []richTextSemanticSegment{{path: []string{"numbered_list_item", "content"}, content: value.GetContent(), replace: func(content []*contentv1.RichTextInline) { value.Content = content }}}
	}
	if value := block.GetCheckListItem(); value != nil {
		return []richTextSemanticSegment{{path: []string{"check_list_item", "content"}, content: value.GetContent(), replace: func(content []*contentv1.RichTextInline) { value.Content = content }}}
	}
	if value := block.GetQuote(); value != nil {
		return []richTextSemanticSegment{{path: []string{"quote", "content"}, content: value.GetContent(), replace: func(content []*contentv1.RichTextInline) { value.Content = content }}}
	}
	table := block.GetTable().GetContent()
	if table == nil {
		return nil
	}
	segments := make([]richTextSemanticSegment, 0)
	for _, row := range table.GetRows() {
		for _, cell := range row.GetCells() {
			cell := cell
			segments = append(segments, richTextSemanticSegment{
				path:    []string{"table", "content", "rows", row.GetRowId(), "cells", cell.GetCellId(), "content"},
				content: cell.GetContent(),
				replace: func(content []*contentv1.RichTextInline) { cell.Content = content },
			})
		}
	}
	return segments
}

func richTextSemanticInline(content []*contentv1.RichTextInline) (string, []XLIFFOriginalData, []XLIFFInline) {
	data := make([]XLIFFOriginalData, 0)
	nodes := make([]XLIFFInline, 0, len(content))
	runIndex := 0
	codeIndex := 0
	appendData := func(value string) string {
		id := "d" + strconv.Itoa(len(data)+1)
		data = append(data, XLIFFOriginalData{ID: id, Value: value})
		return id
	}
	styledNode := func(text *contentv1.RichTextStyledText) XLIFFInline {
		runIndex++
		start, end := appendData(""), appendData("")
		nextData, children := XLIFFInlineFromText(text.GetText(), data)
		data = nextData
		return XLIFFInline{
			Kind: XLIFFInlinePairedCode, ID: "r" + strconv.Itoa(runIndex),
			DataRefStart: start, DataRefEnd: end, CanCopy: "no", CanDelete: "no", Children: children,
		}
	}
	for _, item := range content {
		if item == nil {
			continue
		}
		switch {
		case item.GetText() != nil:
			nodes = append(nodes, styledNode(item.GetText()))
		case item.GetLink() != nil:
			codeIndex++
			start, end := appendData(""), appendData("")
			children := make([]XLIFFInline, 0, len(item.GetLink().GetContent()))
			for _, text := range item.GetLink().GetContent() {
				children = append(children, styledNode(text))
			}
			nodes = append(nodes, XLIFFInline{
				Kind: XLIFFInlinePairedCode, ID: "l" + strconv.Itoa(codeIndex),
				DataRefStart: start, DataRefEnd: end, CanCopy: "no", CanDelete: "no", Children: children,
			})
		case item.GetHardBreak() != nil:
			codeIndex++
			ref := appendData("\n")
			nodes = append(nodes, XLIFFInline{
				Kind: XLIFFInlinePlaceholder, ID: "h" + strconv.Itoa(codeIndex),
				DataRef: ref, CanCopy: "no", CanDelete: "no",
			})
		case item.GetMathInline() != nil:
			codeIndex++
			ref := appendData(item.GetMathInline().GetSource())
			nodes = append(nodes, XLIFFInline{
				Kind: XLIFFInlinePlaceholder, ID: "m" + strconv.Itoa(codeIndex),
				DataRef: ref, CanCopy: "no", CanDelete: "no",
			})
		}
	}
	visible, _ := ProjectXLIFFInline(nodes, data)
	return visible, data, nodes
}

func walkRichTextTranslationStrings(message protoreflect.Message, path []string, visit func([]string, string)) {
	descriptor := message.Descriptor()
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		isTranslationString := field.Kind() == protoreflect.StringKind &&
			IsRichTextTranslationStringField(descriptor, field)
		if !message.Has(field) && !field.IsList() && !isTranslationString {
			continue
		}
		fieldPath := append(append([]string(nil), path...), string(field.Name()))
		if field.IsList() {
			list := message.Get(field).List()
			for itemIndex := 0; itemIndex < list.Len(); itemIndex++ {
				itemPath := append(append([]string(nil), fieldPath...), strconv.Itoa(itemIndex))
				if field.Kind() == protoreflect.MessageKind {
					walkRichTextTranslationStrings(list.Get(itemIndex).Message(), itemPath, visit)
				}
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind {
			walkRichTextTranslationStrings(message.Get(field).Message(), fieldPath, visit)
			continue
		}
		if !isTranslationString {
			continue
		}
		visit(fieldPath, message.Get(field).String())
	}
}

func richTextUnitID(prefix string, path []string) string {
	return prefix + ":typed:" + strings.Join(path, "/")
}

func applyRichTextSemanticResult(content []*contentv1.RichTextInline, result UnitResult) ([]*contentv1.RichTextInline, error) {
	if len(result.TargetInline) == 0 {
		runCount := 0
		for _, item := range content {
			if item.GetText() != nil {
				runCount++
			}
			if item.GetLink() != nil {
				runCount += len(item.GetLink().GetContent())
			}
		}
		if runCount != 1 {
			return nil, fmt.Errorf("typed Rich Text target inline structure is required")
		}
		cloned := cloneRichTextInlineSlice(content)
		for _, item := range cloned {
			if item.GetText() != nil {
				item.GetText().Text = PreserveSourceEdgeWhitespace(item.GetText().GetText(), result.TranslatedText)
				return cloned, nil
			}
			if item.GetLink() != nil && len(item.GetLink().GetContent()) == 1 {
				text := item.GetLink().GetContent()[0]
				text.Text = PreserveSourceEdgeWhitespace(text.GetText(), result.TranslatedText)
				return cloned, nil
			}
		}
	}
	return rebuildRichTextInline(content, result)
}

type richTextInlineAuthority struct {
	runs         map[string]*contentv1.RichTextStyledText
	links        map[string]*contentv1.RichTextLink
	placeholders map[string]*contentv1.RichTextInline
}

func rebuildRichTextInline(source []*contentv1.RichTextInline, result UnitResult) ([]*contentv1.RichTextInline, error) {
	authority := indexRichTextInlineAuthority(source)
	rebuilt := make([]*contentv1.RichTextInline, 0, len(result.TargetInline))
	for _, node := range result.TargetInline {
		item, err := rebuildRichTextTopLevelNode(node, result.OriginalData, authority)
		if err != nil {
			return nil, err
		}
		rebuilt = append(rebuilt, item)
	}
	visible, err := richTextVisibleText(rebuilt)
	if err != nil {
		return nil, err
	}
	if visible != result.TranslatedText {
		return nil, fmt.Errorf("typed Rich Text applied text differs from the validated target")
	}
	return rebuilt, nil
}

func indexRichTextInlineAuthority(source []*contentv1.RichTextInline) richTextInlineAuthority {
	authority := richTextInlineAuthority{
		runs: make(map[string]*contentv1.RichTextStyledText), links: make(map[string]*contentv1.RichTextLink),
		placeholders: make(map[string]*contentv1.RichTextInline),
	}
	runIndex := 0
	codeIndex := 0
	indexRun := func(text *contentv1.RichTextStyledText) {
		runIndex++
		authority.runs["r"+strconv.Itoa(runIndex)] = proto.Clone(text).(*contentv1.RichTextStyledText)
	}
	for _, item := range source {
		switch {
		case item == nil:
			continue
		case item.GetText() != nil:
			indexRun(item.GetText())
		case item.GetLink() != nil:
			codeIndex++
			authority.links["l"+strconv.Itoa(codeIndex)] = proto.Clone(item.GetLink()).(*contentv1.RichTextLink)
			for _, text := range item.GetLink().GetContent() {
				indexRun(text)
			}
		case item.GetHardBreak() != nil:
			codeIndex++
			authority.placeholders["h"+strconv.Itoa(codeIndex)] = proto.Clone(item).(*contentv1.RichTextInline)
		case item.GetMathInline() != nil:
			codeIndex++
			authority.placeholders["m"+strconv.Itoa(codeIndex)] = proto.Clone(item).(*contentv1.RichTextInline)
		}
	}
	return authority
}

func rebuildRichTextTopLevelNode(node XLIFFInline, originalData []XLIFFOriginalData, authority richTextInlineAuthority) (*contentv1.RichTextInline, error) {
	switch node.Kind {
	case XLIFFInlinePairedCode:
		if sourceRun, ok := authority.runs[node.ID]; ok {
			text, err := ProjectXLIFFInline(node.Children, originalData)
			if err != nil {
				return nil, err
			}
			run := proto.Clone(sourceRun).(*contentv1.RichTextStyledText)
			run.Text = PreserveSourceEdgeWhitespace(sourceRun.GetText(), text)
			return &contentv1.RichTextInline{Value: &contentv1.RichTextInline_Text{Text: run}}, nil
		}
		if sourceLink, ok := authority.links[node.ID]; ok {
			link := proto.Clone(sourceLink).(*contentv1.RichTextLink)
			link.Content = make([]*contentv1.RichTextStyledText, 0, len(node.Children))
			for _, child := range node.Children {
				if child.Kind != XLIFFInlinePairedCode {
					return nil, fmt.Errorf("typed Rich Text link target contains text outside an original styled run")
				}
				sourceRun, ok := authority.runs[child.ID]
				if !ok {
					return nil, fmt.Errorf("typed Rich Text link target references an unknown styled run")
				}
				text, err := ProjectXLIFFInline(child.Children, originalData)
				if err != nil {
					return nil, err
				}
				run := proto.Clone(sourceRun).(*contentv1.RichTextStyledText)
				run.Text = PreserveSourceEdgeWhitespace(sourceRun.GetText(), text)
				link.Content = append(link.Content, run)
			}
			return &contentv1.RichTextInline{Value: &contentv1.RichTextInline_Link{Link: link}}, nil
		}
		return nil, fmt.Errorf("typed Rich Text target references an unknown paired code")
	case XLIFFInlinePlaceholder:
		item, ok := authority.placeholders[node.ID]
		if !ok {
			return nil, fmt.Errorf("typed Rich Text target moved an inline placeholder outside its styled run")
		}
		return proto.Clone(item).(*contentv1.RichTextInline), nil
	default:
		return nil, fmt.Errorf("typed Rich Text target contains text outside an original styled run")
	}
}

func cloneRichTextInlineSlice(source []*contentv1.RichTextInline) []*contentv1.RichTextInline {
	cloned := make([]*contentv1.RichTextInline, 0, len(source))
	for _, item := range source {
		if item != nil {
			cloned = append(cloned, proto.Clone(item).(*contentv1.RichTextInline))
		}
	}
	return cloned
}

func richTextVisibleText(content []*contentv1.RichTextInline) (string, error) {
	text, data, inline := richTextSemanticInline(content)
	projected, err := ProjectXLIFFInline(inline, data)
	if err != nil {
		return "", err
	}
	if text != projected {
		return "", fmt.Errorf("typed Rich Text projection changed")
	}
	return projected, nil
}

func walkMutableRichTextTranslationStrings(
	message protoreflect.Message,
	path []string,
	visit func([]string, protoreflect.Message, protoreflect.FieldDescriptor),
) {
	descriptor := message.Descriptor()
	fields := descriptor.Fields()
	for index := 0; index < fields.Len(); index++ {
		field := fields.Get(index)
		isTranslationString := field.Kind() == protoreflect.StringKind &&
			IsRichTextTranslationStringField(descriptor, field)
		if !message.Has(field) && !field.IsList() && !isTranslationString {
			continue
		}
		fieldPath := append(append([]string(nil), path...), string(field.Name()))
		if field.IsList() {
			list := message.Mutable(field).List()
			for itemIndex := 0; itemIndex < list.Len(); itemIndex++ {
				itemPath := append(append([]string(nil), fieldPath...), strconv.Itoa(itemIndex))
				if field.Kind() == protoreflect.MessageKind {
					walkMutableRichTextTranslationStrings(list.Get(itemIndex).Message(), itemPath, visit)
				}
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind {
			walkMutableRichTextTranslationStrings(message.Mutable(field).Message(), fieldPath, visit)
			continue
		}
		if isTranslationString {
			visit(fieldPath, message, field)
		}
	}
}
