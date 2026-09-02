package aidocumentadapter

import (
	"errors"
	"fmt"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type generatedInlineStyle struct {
	bold, italic, underline, strike, code bool
	textColor, backgroundColor            string
}

func (style generatedInlineStyle) with(item core.InlineItem) generatedInlineStyle {
	switch item.Kind {
	case core.InlineKindBold:
		style.bold = true
	case core.InlineKindItalic:
		style.italic = true
	case core.InlineKindUnderline:
		style.underline = true
	case core.InlineKindStrike:
		style.strike = true
	case core.InlineKindCode:
		style.code = true
	case core.InlineKindTextColor:
		style.textColor = item.Target
	case core.InlineKindBackground:
		style.backgroundColor = item.Target
	}
	return style
}

func projectInlineList(list protoreflect.List) ([]core.InlineItem, error) {
	result := make([]core.InlineItem, 0, list.Len())
	for index := 0; index < list.Len(); index++ {
		inline, ok := list.Get(index).Message().Interface().(*contentv1.RichTextInline)
		if !ok {
			return nil, errors.New("generated inline type mismatch")
		}
		switch {
		case inline.GetText() != nil:
			result = append(result, projectStyledText(inline.GetText()))
		case inline.GetHardBreak() != nil:
			result = append(result, core.HardBreak())
		case inline.GetMathInline() != nil:
			result = append(result, core.InlineMath(inline.GetMathInline().GetSource()))
		case inline.GetLink() != nil:
			children := make([]core.InlineItem, 0, len(inline.GetLink().GetContent()))
			for _, text := range inline.GetLink().GetContent() {
				children = append(children, projectStyledText(text))
			}
			result = append(result, core.Link(inline.GetLink().GetHref(), children...))
		default:
			return nil, errors.New("generated inline value is required")
		}
	}
	return result, nil
}

func projectStyledText(value *contentv1.RichTextStyledText) core.InlineItem {
	item := core.InlineText(value.GetText())
	styles := value.GetStyles()
	if styles == nil {
		return item
	}
	if styles.GetCode() {
		item = core.InlineCode(item)
	}
	if styles.GetStrike() {
		item = core.Strike(item)
	}
	if styles.GetUnderline() {
		item = core.Underline(item)
	}
	if styles.GetItalic() {
		item = core.Italic(item)
	}
	if styles.GetBold() {
		item = core.Bold(item)
	}
	if styles.TextColor != nil {
		item = core.TextColor(styles.GetTextColor(), item)
	}
	if styles.BackgroundColor != nil {
		item = core.BackgroundColor(styles.GetBackgroundColor(), item)
	}
	return item
}

func projectLocaleTable(message protoreflect.Message, descriptor contentv1.ContentTableDescriptor) (core.Value, error) {
	table, ok := message.Interface().(*contentv1.RichTextTableLocale)
	if !ok {
		return core.Value{}, errors.New("generated locale table type mismatch")
	}
	rowIdentity, cellIdentity := core.FieldID(descriptor.RowIdentity.Name), core.FieldID(descriptor.CellIdentity.Name)
	rowValues := make([]core.ListItem, 0, len(table.GetRows()))
	for _, row := range table.GetRows() {
		cellValues := make([]core.ListItem, 0, len(row.GetCells()))
		for _, cell := range row.GetCells() {
			content, err := projectInlineList(cell.ProtoReflect().Get(findMessageField(cell.ProtoReflect(), "content")).List())
			if err != nil {
				return core.Value{}, err
			}
			cellValues = append(cellValues, core.StableItem(core.RelationItemID(cell.GetCellId()), core.Object(
				core.ObjectValue(cellIdentity, core.Text(cell.GetCellId())), core.ObjectValue(richTextTableCellContent, core.RichText(content...)),
			)))
		}
		rowValues = append(rowValues, core.StableItem(core.RelationItemID(row.GetRowId()), core.Object(
			core.ObjectValue(rowIdentity, core.Text(row.GetRowId())), core.ObjectValue(richTextTableCellsField, core.List(cellValues...)),
		)))
	}
	return core.Object(core.ObjectValue(richTextTableRowsField, core.List(rowValues...))), nil
}

func inlineToGenerated(values []core.InlineItem) ([]*contentv1.RichTextInline, error) {
	result := make([]*contentv1.RichTextInline, 0, len(values))
	for _, value := range values {
		converted, err := inlineItemToGenerated(value, generatedInlineStyle{}, false)
		if err != nil {
			return nil, err
		}
		result = append(result, converted...)
	}
	return result, nil
}

func inlineItemToGenerated(
	item core.InlineItem,
	style generatedInlineStyle,
	insideLink bool,
) ([]*contentv1.RichTextInline, error) {
	switch item.Kind {
	case core.InlineKindText:
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: generatedStyledText(item.Text, style)}}}, nil
	case core.InlineKindBold, core.InlineKindItalic, core.InlineKindUnderline, core.InlineKindStrike,
		core.InlineKindCode, core.InlineKindTextColor, core.InlineKindBackground:
		style = style.with(item)
		var result []*contentv1.RichTextInline
		for _, child := range item.Children {
			converted, err := inlineItemToGenerated(child, style, insideLink)
			if err != nil {
				return nil, err
			}
			result = append(result, converted...)
		}
		return result, nil
	case core.InlineKindLink:
		if insideLink {
			return nil, errors.New("nested inline links are not supported by the generated catalog")
		}
		link := &contentv1.RichTextLink{Href: item.Target}
		for _, child := range item.Children {
			converted, err := inlineItemToGenerated(child, style, true)
			if err != nil {
				return nil, err
			}
			for _, value := range converted {
				if value.GetText() == nil {
					return nil, errors.New("generated links may contain styled text only")
				}
				link.Content = append(link.Content, value.GetText())
			}
		}
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Link{Link: link}}}, nil
	case core.InlineKindHardBreak:
		if insideLink || style != (generatedInlineStyle{}) {
			return nil, errors.New("hard breaks cannot carry marks or appear inside links")
		}
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_HardBreak{HardBreak: &contentv1.RichTextHardBreak{}}}}, nil
	case core.InlineKindMath:
		if insideLink || style != (generatedInlineStyle{}) {
			return nil, errors.New("inline math cannot carry marks or appear inside links")
		}
		return []*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_MathInline{MathInline: &contentv1.RichTextInlineMath{Source: item.Text}}}}, nil
	case core.InlineKindPlaceholder:
		return nil, errors.New("placeholder inline nodes are not part of the generated Rich Text catalog")
	default:
		return nil, fmt.Errorf("unsupported inline kind %q", item.Kind)
	}
}

func generatedStyledText(text string, style generatedInlineStyle) *contentv1.RichTextStyledText {
	result := &contentv1.RichTextStyledText{Text: text}
	if style == (generatedInlineStyle{}) {
		return result
	}
	result.Styles = &contentv1.RichTextStyle{}
	truth := func(value bool) *bool {
		if !value {
			return nil
		}
		copy := true
		return &copy
	}
	result.Styles.Bold = truth(style.bold)
	result.Styles.Italic = truth(style.italic)
	result.Styles.Underline = truth(style.underline)
	result.Styles.Strike = truth(style.strike)
	result.Styles.Code = truth(style.code)
	if style.textColor != "" {
		result.Styles.TextColor = &style.textColor
	}
	if style.backgroundColor != "" {
		result.Styles.BackgroundColor = &style.backgroundColor
	}
	return result
}

func projectBaseTable(message protoreflect.Message, descriptor contentv1.ContentTableDescriptor) (core.Value, error) {
	table, ok := message.Interface().(*contentv1.RichTextTableBase)
	if !ok {
		return core.Value{}, errors.New("generated base table type mismatch")
	}
	fields := []core.ObjectField{}
	tableMessage := table.ProtoReflect()
	tableFields, _, err := projectObject(tableMessage, descriptor.Fields, richTextTableField, nil)
	if err != nil {
		return core.Value{}, err
	}
	fields = append(fields, tableFields.Object...)
	rowIdentity, cellIdentity := core.FieldID(descriptor.RowIdentity.Name), core.FieldID(descriptor.CellIdentity.Name)
	rows := make([]core.ListItem, 0, len(table.GetRows()))
	for _, row := range table.GetRows() {
		cells := make([]core.ListItem, 0, len(row.GetCells()))
		for _, cell := range row.GetCells() {
			cellFields := []core.ObjectField{core.ObjectValue(cellIdentity, core.Text(cell.GetId())), core.ObjectValue(richTextTableHeaderField, core.Boolean(cell.GetHeader()))}
			if props := cell.GetProps(); props != nil {
				projected, _, err := projectObject(props.ProtoReflect(), descriptor.CellFields, richTextTableField, nil)
				if err != nil {
					return core.Value{}, err
				}
				cellFields = append(cellFields, projected.Object...)
			}
			cells = append(cells, core.StableItem(core.RelationItemID(cell.GetId()), core.Object(cellFields...)))
		}
		rows = append(rows, core.StableItem(core.RelationItemID(row.GetId()), core.Object(
			core.ObjectValue(rowIdentity, core.Text(row.GetId())), core.ObjectValue(richTextTableCellsField, core.List(cells...)),
		)))
	}
	fields = append(fields, core.ObjectValue(richTextTableRowsField, core.List(rows...)))
	return core.Object(fields...), nil
}

func setTableValue(message protoreflect.Message, value core.Value, localized bool, descriptor contentv1.RichTextCatalogDescriptor) error {
	if value.Kind != core.ValueKindObject {
		return errors.New("table content requires an object value")
	}
	if localized {
		table, ok := message.Interface().(*contentv1.RichTextTableLocale)
		if !ok {
			return errors.New("generated locale table type mismatch")
		}
		built := &contentv1.RichTextTableLocale{}
		rowIdentity, cellIdentity := core.FieldID(descriptor.Table.RowIdentity.Name), core.FieldID(descriptor.Table.CellIdentity.Name)
		rowsValue, _ := coreObjectValue(value, richTextTableRowsField)
		for _, rowItem := range rowsValue.List {
			rowIDValue, _ := coreObjectValue(rowItem.Value, rowIdentity)
			row := &contentv1.RichTextTableRowLocale{RowId: rowIDValue.Text}
			cellsValue, _ := coreObjectValue(rowItem.Value, richTextTableCellsField)
			for _, cellItem := range cellsValue.List {
				cellIDValue, _ := coreObjectValue(cellItem.Value, cellIdentity)
				contentValue, _ := coreObjectValue(cellItem.Value, richTextTableCellContent)
				inline, err := inlineToGenerated(contentValue.Inline)
				if err != nil {
					return err
				}
				row.Cells = append(row.Cells, &contentv1.RichTextTableCellLocale{CellId: cellIDValue.Text, Content: inline})
			}
			built.Rows = append(built.Rows, row)
		}
		proto.Reset(table)
		proto.Merge(table, built)
		return nil
	}
	table, ok := message.Interface().(*contentv1.RichTextTableBase)
	if !ok {
		return errors.New("generated base table type mismatch")
	}
	built := &contentv1.RichTextTableBase{}
	builtMessage := built.ProtoReflect()
	for _, fieldDescriptor := range descriptor.Table.Fields {
		fieldValue, present := coreObjectValue(value, core.FieldID(fieldDescriptor.Name))
		if !present {
			continue
		}
		field := findMessageField(builtMessage, fieldDescriptor.Name)
		if field == nil {
			return fmt.Errorf("generated table field %q is missing", fieldDescriptor.Name)
		}
		if err := setDescriptorValue(builtMessage, field, fieldDescriptor, fieldValue); err != nil {
			return err
		}
	}
	rowIdentity, cellIdentity := core.FieldID(descriptor.Table.RowIdentity.Name), core.FieldID(descriptor.Table.CellIdentity.Name)
	rowsValue, _ := coreObjectValue(value, richTextTableRowsField)
	for _, rowItem := range rowsValue.List {
		rowID, _ := coreObjectValue(rowItem.Value, rowIdentity)
		row := &contentv1.RichTextTableRowBase{Id: rowID.Text}
		cellsValue, _ := coreObjectValue(rowItem.Value, richTextTableCellsField)
		for _, cellItem := range cellsValue.List {
			cellID, _ := coreObjectValue(cellItem.Value, cellIdentity)
			cell := &contentv1.RichTextTableCellBase{Id: cellID.Text, Props: &contentv1.RichTextTableCellProps{}}
			if header, ok := coreObjectValue(cellItem.Value, richTextTableHeaderField); ok {
				cell.Header = header.Boolean
			}
			// Cell props are set through protobuf JSON names so generated enum
			// normalization remains authoritative in the final flattener.
			propsMessage := cell.Props.ProtoReflect()
			for _, object := range cellItem.Value.Object {
				if object.ID == cellIdentity || object.ID == richTextTableHeaderField {
					continue
				}
				fieldDescriptor, ok := findContentDescriptor(descriptor.Table.CellFields, string(object.ID))
				if !ok {
					continue
				}
				field := findMessageField(propsMessage, fieldDescriptor.Name)
				if field == nil {
					return fmt.Errorf("generated table cell field %q is missing", fieldDescriptor.Name)
				}
				if err := setDescriptorValue(propsMessage, field, fieldDescriptor, object.Value); err != nil {
					return err
				}
			}
			row.Cells = append(row.Cells, cell)
		}
		built.Rows = append(built.Rows, row)
	}
	proto.Reset(table)
	proto.Merge(table, built)
	return nil
}

func coreObjectValue(value core.Value, field core.FieldID) (core.Value, bool) {
	for _, candidate := range value.Object {
		if candidate.ID == field {
			return candidate.Value, true
		}
	}
	return core.Value{}, false
}
