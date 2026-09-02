package contentblock

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"sort"
	"strings"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// MaterializedContent is a deterministic semantic projection. HTML contains
// no executable source or caller-provided raw markup; Text is the equivalent
// plain-text projection used by search and delivery fallbacks.
type MaterializedContent struct {
	HTML string
	Text string
}

type LocalizedMaterializedContent struct {
	Locale string
	MaterializedContent
}

type FileRenderSelector struct {
	BlockID       uuid.UUID
	ReferencePath string
	FileID        uuid.UUID
}

type FileRenderTarget struct {
	URL      string
	MIMEType string
}

// FileRenderResolver resolves only an already-authorized, public delivery
// target. Implementations must not return original object keys or signed URLs
// for durable HTML projections.
type FileRenderResolver interface {
	ResolveContentBlockFile(context.Context, FileRenderSelector) (FileRenderTarget, error)
}

func (s *Store) LoadAndMaterializeRichTextLocale(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
	sourceLocale string,
	locale string,
	resolver FileRenderResolver,
) (MaterializedContent, error) {
	snapshot, err := s.LoadSnapshot(ctx, db, documentID, sourceLocale)
	if err != nil {
		return MaterializedContent{}, err
	}
	return materializeSnapshotLocale(ctx, snapshot, locale, resolver)
}

func (s *Store) LoadAndMaterializeRichTextLocaleInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	sourceLocale string,
	locale string,
	resolver FileRenderResolver,
) (MaterializedContent, error) {
	snapshot, err := s.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
	if err != nil {
		return MaterializedContent{}, err
	}
	return materializeSnapshotLocale(ctx, snapshot, locale, resolver)
}

func materializeSnapshotLocale(
	ctx context.Context,
	snapshot Snapshot,
	locale string,
	resolver FileRenderResolver,
) (MaterializedContent, error) {
	document, err := MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return MaterializedContent{}, err
	}
	return MaterializeLocalizedRichTextDocument(ctx, document, resolver)
}

// MaterializeSnapshotRichTextLocale builds the public read projection for one
// requested locale. Missing target Blocks and optional fields fall back to the
// current source; explicitly present empty target values remain empty.
func MaterializeSnapshotRichTextLocale(
	snapshot Snapshot,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := SnapshotToRichTextDocument(snapshot)
	if err != nil {
		return nil, err
	}
	return localizedRichTextDocumentForMaterialization(document, locale)
}

// MaterializeLocalizedRichTextDocument renders one generated typed document.
// A nil resolver is valid only when the document has no active File Block.
func MaterializeLocalizedRichTextDocument(
	ctx context.Context,
	document *contentv1.LocalizedRichTextDocument,
	resolver FileRenderResolver,
) (MaterializedContent, error) {
	if document == nil {
		return MaterializedContent{}, fmt.Errorf("%w: localized Rich Text document is required", ErrInvalidMutation)
	}
	aggregate := &contentv1.RichTextDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		SourceLocale:            document.GetLocale(),
		Base:                    document.GetBase(),
		LocaleOverlays:          []*contentv1.RichTextLocaleOverlay{document.GetLocaleOverlay()},
	}
	if err := contentv1.ValidateRichTextDocument(
		aggregate,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	); err != nil {
		return MaterializedContent{}, fmt.Errorf("%w: validate localized Rich Text document: %v", ErrInvalidMutation, err)
	}
	localized := make(map[string]*contentv1.RichTextBlockLocale, len(document.GetLocaleOverlay().GetBlocks()))
	for _, block := range document.GetLocaleOverlay().GetBlocks() {
		localized[block.GetBlockId()] = block
	}
	nodes := document.GetBase().GetNodes()
	byParent := make(map[string][]*contentv1.RichTextBlockNode)
	for _, node := range nodes {
		parent := node.GetPlacement().GetParentBlockId()
		byParent[parent] = append(byParent[parent], node)
	}
	for parent := range byParent {
		sort.Slice(byParent[parent], func(i, j int) bool {
			left := byParent[parent][i]
			right := byParent[parent][j]
			if left.GetPlacement().GetIndex() != right.GetPlacement().GetIndex() {
				return left.GetPlacement().GetIndex() < right.GetPlacement().GetIndex()
			}
			return left.GetBlock().GetId() < right.GetBlock().GetId()
		})
	}
	var htmlBuilder strings.Builder
	var textParts []string
	visited := make(map[string]bool, len(nodes))
	var renderChildren func(string) error
	renderChildren = func(parent string) error {
		for _, node := range byParent[parent] {
			blockID := node.GetBlock().GetId()
			if visited[blockID] {
				return fmt.Errorf("%w: render Block cycle at %s", ErrInvalidMutation, blockID)
			}
			visited[blockID] = true
			fragment, err := renderRichTextBlock(ctx, node.GetBlock(), localized[blockID], resolver)
			if err != nil {
				return err
			}
			callout := node.GetBlock().GetCallout()
			if callout != nil {
				props := callout.GetProps()
				icon := props.GetIcon()
				if icon == "" {
					icon = "💡"
				}
				backgroundColor := props.GetBackgroundColor()
				if backgroundColor == "" {
					backgroundColor = "gray"
				}
				textColor := props.GetTextColor()
				if textColor == "" {
					textColor = "default"
				}
				htmlBuilder.WriteString(`<aside data-callout="" data-bg-color="` + html.EscapeString(backgroundColor) + `" data-text-color="` + html.EscapeString(textColor) + `"><span data-callout-icon="" aria-hidden="true">` + html.EscapeString(icon) + `</span><div data-callout-content=""><div data-callout-copy="">` + fragment.HTML + `</div>`)
			} else {
				htmlBuilder.WriteString(fragment.HTML)
			}
			if strings.TrimSpace(fragment.Text) != "" {
				textParts = append(textParts, fragment.Text)
			}
			if err := renderChildren(blockID); err != nil {
				return err
			}
			if callout != nil {
				htmlBuilder.WriteString(`</div></aside>`)
			}
		}
		return nil
	}
	if err := renderChildren(""); err != nil {
		return MaterializedContent{}, err
	}
	if len(visited) != len(nodes) {
		return MaterializedContent{}, fmt.Errorf("%w: render graph contains unreachable Blocks", ErrInvalidMutation)
	}
	return MaterializedContent{HTML: htmlBuilder.String(), Text: strings.Join(textParts, "\n")}, nil
}

// CompleteRichTextDocumentLocales returns the deterministic stored locale set.
// The source locale is authoritative and must be complete. Every sparse target
// overlay is materializable because missing Blocks fall back to source.
func CompleteRichTextDocumentLocales(document *contentv1.RichTextDocument) ([]string, error) {
	if err := contentv1.ValidateRichTextDocument(
		document,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	); err != nil {
		return nil, fmt.Errorf("%w: validate Rich Text document: %v", ErrInvalidMutation, err)
	}
	locales := make([]string, 0, len(document.GetLocaleOverlays()))
	seen := make(map[string]struct{}, len(document.GetLocaleOverlays()))
	for _, overlay := range document.GetLocaleOverlays() {
		if _, exists := seen[overlay.GetLocale()]; exists {
			continue
		}
		seen[overlay.GetLocale()] = struct{}{}
		locales = append(locales, overlay.GetLocale())
	}
	sort.Strings(locales)
	return locales, nil
}

func localizedRichTextDocumentForMaterialization(
	document *contentv1.RichTextDocument,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	if err := contentv1.ValidateRichTextDocument(
		document,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_RESTORE_SNAPSHOT,
	); err != nil {
		return nil, fmt.Errorf("%w: validate Rich Text document: %v", ErrInvalidMutation, err)
	}
	var source *contentv1.RichTextLocaleOverlay
	var target *contentv1.RichTextLocaleOverlay
	for _, overlay := range document.GetLocaleOverlays() {
		switch overlay.GetLocale() {
		case document.GetSourceLocale():
			source = overlay
		case locale:
			target = overlay
		}
	}
	if source == nil {
		return nil, fmt.Errorf("%w: source locale overlay is missing", ErrInvalidMutation)
	}
	if locale == document.GetSourceLocale() {
		target = source
	}
	targetByBlock := make(map[string]*contentv1.RichTextBlockLocale)
	if target != nil {
		for _, block := range target.GetBlocks() {
			targetByBlock[block.GetBlockId()] = block
		}
	}
	sourceByBlock := make(map[string]*contentv1.RichTextBlockLocale, len(source.GetBlocks()))
	for _, block := range source.GetBlocks() {
		sourceByBlock[block.GetBlockId()] = block
	}
	merged := &contentv1.RichTextLocaleOverlay{Locale: locale}
	for _, node := range document.GetBase().GetNodes() {
		blockID := node.GetBlock().GetId()
		sourceBlock := sourceByBlock[blockID]
		if sourceBlock == nil {
			return nil, fmt.Errorf("%w: source locale Block %s is missing", ErrInvalidMutation, blockID)
		}
		merged.Blocks = append(merged.Blocks, mergeRichTextLocaleBlock(sourceBlock, targetByBlock[blockID]))
	}
	return &contentv1.LocalizedRichTextDocument{
		BlockCatalogFingerprint: document.GetBlockCatalogFingerprint(),
		Profile:                 document.GetProfile(),
		Locale:                  locale,
		Base:                    proto.Clone(document.GetBase()).(*contentv1.RichTextBlockGraph),
		LocaleOverlay:           merged,
	}, nil
}

func mergeRichTextLocaleBlock(
	source *contentv1.RichTextBlockLocale,
	target *contentv1.RichTextBlockLocale,
) *contentv1.RichTextBlockLocale {
	if target == nil {
		return proto.Clone(source).(*contentv1.RichTextBlockLocale)
	}
	// Inline content is one semantic translated value, so presence of the target
	// Block includes an explicitly empty value. Table content is merged by its
	// durable row/cell IDs below.
	switch {
	case target.GetParagraph() != nil,
		target.GetHeading() != nil,
		target.GetBulletListItem() != nil,
		target.GetNumberedListItem() != nil,
		target.GetCheckListItem() != nil,
		target.GetQuote() != nil:
		return proto.Clone(target).(*contentv1.RichTextBlockLocale)
	case target.GetTable() != nil:
		return mergeRichTextTableLocaleBlock(source, target)
	case target.GetCodeBlock() != nil:
		// The current wire has no scalar presence for code content. Until the
		// hard cut makes it optional, target Block presence is the only safe
		// authority for an explicitly empty content value.
		result := proto.Clone(source).(*contentv1.RichTextBlockLocale)
		result.Value = &contentv1.RichTextBlockLocale_CodeBlock{CodeBlock: &contentv1.CodeBlockBlockLocale{
			Content: target.GetCodeBlock().GetContent(),
		}}
		if source.GetCodeBlock().GetProps() != nil {
			result.GetCodeBlock().Props = proto.Clone(source.GetCodeBlock().GetProps()).(*contentv1.CodeBlockLocaleProps)
		}
		if target.GetCodeBlock().GetProps() != nil {
			if result.GetCodeBlock().Props == nil {
				result.GetCodeBlock().Props = &contentv1.CodeBlockLocaleProps{}
			}
			proto.Merge(result.GetCodeBlock().Props, target.GetCodeBlock().GetProps())
		}
		return result
	default:
		// Props-only locale payloads use proto optional scalars, so Merge keeps
		// missing target fields from source and honors explicitly present empty
		// strings.
		result := proto.Clone(source).(*contentv1.RichTextBlockLocale)
		proto.Merge(result, target)
		return result
	}
}

func mergeRichTextTableLocaleBlock(
	source *contentv1.RichTextBlockLocale,
	target *contentv1.RichTextBlockLocale,
) *contentv1.RichTextBlockLocale {
	sourceTable := source.GetTable()
	targetTable := target.GetTable()
	resultTable := &contentv1.TableBlockLocale{}
	if sourceTable.GetProps() != nil {
		resultTable.Props = proto.Clone(sourceTable.GetProps()).(*contentv1.TableLocaleProps)
	}
	if targetTable.GetProps() != nil {
		if resultTable.Props == nil {
			resultTable.Props = &contentv1.TableLocaleProps{}
		}
		proto.Merge(resultTable.Props, targetTable.GetProps())
	}
	resultTable.Content = &contentv1.RichTextTableLocale{}
	targetRows := make(map[string]*contentv1.RichTextTableRowLocale)
	if targetTable.GetContent() != nil {
		for _, row := range targetTable.GetContent().GetRows() {
			targetRows[row.GetRowId()] = row
		}
	}
	for _, sourceRow := range sourceTable.GetContent().GetRows() {
		resultRow := &contentv1.RichTextTableRowLocale{RowId: sourceRow.GetRowId()}
		targetCells := make(map[string]*contentv1.RichTextTableCellLocale)
		if targetRow := targetRows[sourceRow.GetRowId()]; targetRow != nil {
			for _, cell := range targetRow.GetCells() {
				targetCells[cell.GetCellId()] = cell
			}
		}
		for _, sourceCell := range sourceRow.GetCells() {
			if targetCell := targetCells[sourceCell.GetCellId()]; targetCell != nil {
				resultRow.Cells = append(resultRow.Cells, proto.Clone(targetCell).(*contentv1.RichTextTableCellLocale))
				continue
			}
			resultRow.Cells = append(resultRow.Cells, proto.Clone(sourceCell).(*contentv1.RichTextTableCellLocale))
		}
		resultTable.Content.Rows = append(resultTable.Content.Rows, resultRow)
	}
	return &contentv1.RichTextBlockLocale{
		BlockId: source.GetBlockId(),
		Value:   &contentv1.RichTextBlockLocale_Table{Table: resultTable},
	}
}

func renderRichTextBlock(
	ctx context.Context,
	block *contentv1.RichTextBlock,
	localized *contentv1.RichTextBlockLocale,
	resolver FileRenderResolver,
) (MaterializedContent, error) {
	if block == nil || localized == nil {
		return MaterializedContent{}, fmt.Errorf("%w: Block and locale payload are required", ErrInvalidMutation)
	}
	switch base := block.GetValue().(type) {
	case *contentv1.RichTextBlock_Paragraph:
		value := localized.GetParagraph()
		return inlineContainerWithAttributes(
			"p",
			paragraphPresentationAttributes(base.Paragraph.GetProps()),
			value.GetContent(),
		)
	case *contentv1.RichTextBlock_Heading:
		value := localized.GetHeading()
		level := int(base.Heading.GetProps().GetLevel())
		if level < 1 || level > 3 {
			level = 1
		}
		return inlineContainer(fmt.Sprintf("h%d", level), value.GetContent())
	case *contentv1.RichTextBlock_BulletListItem:
		value, err := renderInline(localized.GetBulletListItem().GetContent())
		return wrapList("ul", "", value, err)
	case *contentv1.RichTextBlock_NumberedListItem:
		value, err := renderInline(localized.GetNumberedListItem().GetContent())
		start := base.NumberedListItem.GetProps().GetStart()
		attribute := ""
		if start > 1 {
			attribute = fmt.Sprintf(` start="%d"`, start)
		}
		return wrapList("ol", attribute, value, err)
	case *contentv1.RichTextBlock_CheckListItem:
		value, err := renderInline(localized.GetCheckListItem().GetContent())
		checked := base.CheckListItem.GetProps().GetChecked()
		return wrapList("ul", fmt.Sprintf(` data-check-list="true" data-checked="%t"`, checked), value, err)
	case *contentv1.RichTextBlock_Quote:
		return inlineContainer("blockquote", localized.GetQuote().GetContent())
	case *contentv1.RichTextBlock_Callout:
		value := localized.GetCallout()
		if value == nil {
			return MaterializedContent{}, fmt.Errorf("%w: Callout locale payload is required", ErrInvalidMutation)
		}
		return renderInline(value.GetContent())
	case *contentv1.RichTextBlock_CodeBlock:
		title := localized.GetCodeBlock().GetProps().GetTitle()
		code := localized.GetCodeBlock().GetContent()
		caption := ""
		text := code
		if title != "" {
			caption = "<figcaption>" + html.EscapeString(title) + "</figcaption>"
			text = title + "\n" + code
		}
		return MaterializedContent{
			HTML: "<figure>" + caption + "<pre><code>" + html.EscapeString(code) + "</code></pre></figure>",
			Text: text,
		}, nil
	case *contentv1.RichTextBlock_Divider:
		return MaterializedContent{HTML: "<hr>"}, nil
	case *contentv1.RichTextBlock_Table:
		return renderTable(base.Table.GetContent(), localized.GetTable().GetContent())
	case *contentv1.RichTextBlock_P5Sketch:
		return renderInteractive("p5-sketch", localized.GetP5Sketch().GetProps().GetTitle()), nil
	case *contentv1.RichTextBlock_ThreeScene:
		return renderInteractive("three-scene", localized.GetThreeScene().GetProps().GetTitle()), nil
	case *contentv1.RichTextBlock_Shader:
		return renderInteractive("shader", localized.GetShader().GetProps().GetTitle()), nil
	case *contentv1.RichTextBlock_Math:
		latex := base.Math.GetProps().GetLatex()
		return MaterializedContent{HTML: `<span data-math="block">` + html.EscapeString(latex) + `</span>`, Text: latex}, nil
	case *contentv1.RichTextBlock_Map:
		caption := localized.GetMap().GetProps().GetCaption()
		return MaterializedContent{HTML: `<div data-block-kind="map">` + html.EscapeString(caption) + `</div>`, Text: caption}, nil
	case *contentv1.RichTextBlock_File:
		return renderFile(ctx, block.GetId(), base.File.GetProps(), localized.GetFile().GetProps(), resolver)
	default:
		return MaterializedContent{}, fmt.Errorf("%w: unsupported Rich Text Block kind", ErrInvalidMutation)
	}
}

func inlineContainer(tag string, content []*contentv1.RichTextInline) (MaterializedContent, error) {
	return inlineContainerWithAttributes(tag, "", content)
}

func inlineContainerWithAttributes(tag, attributes string, content []*contentv1.RichTextInline) (MaterializedContent, error) {
	value, err := renderInline(content)
	if err != nil {
		return MaterializedContent{}, err
	}
	return MaterializedContent{HTML: "<" + tag + attributes + ">" + value.HTML + "</" + tag + ">", Text: value.Text}, nil
}

func paragraphPresentationAttributes(props *contentv1.ParagraphProps) string {
	if props == nil {
		return ""
	}
	styles := make([]string, 0, 3)
	switch props.GetTextAlignment() {
	case contentv1.ParagraphProps_TEXT_ALIGNMENT_CENTER:
		styles = append(styles, "text-align:center")
	case contentv1.ParagraphProps_TEXT_ALIGNMENT_RIGHT:
		styles = append(styles, "text-align:right")
	}
	if color := props.GetTextColor(); color != "" && color != "default" {
		styles = append(styles, "color:"+color)
	}
	if color := props.GetBackgroundColor(); color != "" && color != "default" {
		styles = append(styles, "background-color:"+color)
	}
	if len(styles) == 0 {
		return ""
	}
	return ` style="` + html.EscapeString(strings.Join(styles, ";")) + `"`
}

func wrapList(tag, attribute string, value MaterializedContent, err error) (MaterializedContent, error) {
	if err != nil {
		return MaterializedContent{}, err
	}
	return MaterializedContent{HTML: "<" + tag + attribute + "><li>" + value.HTML + "</li></" + tag + ">", Text: value.Text}, nil
}

func renderInline(content []*contentv1.RichTextInline) (MaterializedContent, error) {
	var htmlBuilder strings.Builder
	var textBuilder strings.Builder
	for _, inline := range content {
		switch value := inline.GetValue().(type) {
		case *contentv1.RichTextInline_Text:
			escaped := html.EscapeString(value.Text.GetText())
			style := value.Text.GetStyles()
			if style.GetCode() {
				escaped = "<code>" + escaped + "</code>"
			}
			if style.GetStrike() {
				escaped = "<s>" + escaped + "</s>"
			}
			if style.GetUnderline() {
				escaped = "<u>" + escaped + "</u>"
			}
			if style.GetItalic() {
				escaped = "<em>" + escaped + "</em>"
			}
			if style.GetBold() {
				escaped = "<strong>" + escaped + "</strong>"
			}
			htmlBuilder.WriteString(escaped)
			textBuilder.WriteString(value.Text.GetText())
		case *contentv1.RichTextInline_HardBreak:
			htmlBuilder.WriteString("<br>")
			textBuilder.WriteByte('\n')
		case *contentv1.RichTextInline_Link:
			href, err := safeLink(value.Link.GetHref())
			if err != nil {
				return MaterializedContent{}, err
			}
			var bodyHTML strings.Builder
			var bodyText strings.Builder
			for _, text := range value.Link.GetContent() {
				fragment, err := renderInline([]*contentv1.RichTextInline{{Value: &contentv1.RichTextInline_Text{Text: text}}})
				if err != nil {
					return MaterializedContent{}, err
				}
				bodyHTML.WriteString(fragment.HTML)
				bodyText.WriteString(fragment.Text)
			}
			htmlBuilder.WriteString(`<a href="` + html.EscapeString(href) + `">` + bodyHTML.String() + `</a>`)
			textBuilder.WriteString(bodyText.String())
		case *contentv1.RichTextInline_MathInline:
			source := value.MathInline.GetSource()
			htmlBuilder.WriteString(`<span data-math="inline">` + html.EscapeString(source) + `</span>`)
			textBuilder.WriteString(source)
		default:
			return MaterializedContent{}, fmt.Errorf("%w: unsupported Rich Text inline kind", ErrInvalidMutation)
		}
	}
	return MaterializedContent{HTML: htmlBuilder.String(), Text: textBuilder.String()}, nil
}

func safeLink(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("%w: link requires an absolute URI", ErrInvalidMutation)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "mailto", "tel":
		return value, nil
	default:
		return "", fmt.Errorf("%w: unsafe link scheme", ErrInvalidMutation)
	}
}

func renderTable(base *contentv1.RichTextTableBase, localized *contentv1.RichTextTableLocale) (MaterializedContent, error) {
	if base == nil || localized == nil {
		return MaterializedContent{}, fmt.Errorf("%w: table base and locale content are required", ErrInvalidMutation)
	}
	localizedRows := make(map[string]*contentv1.RichTextTableRowLocale, len(localized.GetRows()))
	for _, row := range localized.GetRows() {
		if row.GetRowId() == "" {
			return MaterializedContent{}, fmt.Errorf("%w: table locale row ID is required", ErrInvalidMutation)
		}
		if _, exists := localizedRows[row.GetRowId()]; exists {
			return MaterializedContent{}, fmt.Errorf("%w: duplicate table locale row ID", ErrInvalidMutation)
		}
		localizedRows[row.GetRowId()] = row
	}
	var htmlBuilder strings.Builder
	var textRows []string
	htmlBuilder.WriteString("<table><tbody>")
	for _, row := range base.GetRows() {
		if row.GetId() == "" {
			return MaterializedContent{}, fmt.Errorf("%w: table base row ID is required", ErrInvalidMutation)
		}
		localeRow := localizedRows[row.GetId()]
		if localeRow == nil {
			return MaterializedContent{}, fmt.Errorf("%w: table locale row %s is missing", ErrInvalidMutation, row.GetId())
		}
		localizedCells := make(map[string]*contentv1.RichTextTableCellLocale, len(localeRow.GetCells()))
		for _, cell := range localeRow.GetCells() {
			if cell.GetCellId() == "" {
				return MaterializedContent{}, fmt.Errorf("%w: table locale cell ID is required", ErrInvalidMutation)
			}
			if _, exists := localizedCells[cell.GetCellId()]; exists {
				return MaterializedContent{}, fmt.Errorf("%w: duplicate table locale cell ID", ErrInvalidMutation)
			}
			localizedCells[cell.GetCellId()] = cell
		}
		htmlBuilder.WriteString("<tr>")
		var textCells []string
		for _, cell := range row.GetCells() {
			if cell.GetId() == "" {
				return MaterializedContent{}, fmt.Errorf("%w: table base cell ID is required", ErrInvalidMutation)
			}
			localeCell := localizedCells[cell.GetId()]
			if localeCell == nil {
				return MaterializedContent{}, fmt.Errorf("%w: table locale cell %s is missing", ErrInvalidMutation, cell.GetId())
			}
			value, err := renderInline(localeCell.GetContent())
			if err != nil {
				return MaterializedContent{}, err
			}
			tag := "td"
			if cell.GetHeader() {
				tag = "th"
			}
			attributes := ""
			if colspan := cell.GetProps().GetColspan(); colspan > 1 {
				attributes += fmt.Sprintf(` colspan="%d"`, colspan)
			}
			if rowspan := cell.GetProps().GetRowspan(); rowspan > 1 {
				attributes += fmt.Sprintf(` rowspan="%d"`, rowspan)
			}
			htmlBuilder.WriteString("<" + tag + attributes + ">" + value.HTML + "</" + tag + ">")
			textCells = append(textCells, value.Text)
		}
		htmlBuilder.WriteString("</tr>")
		textRows = append(textRows, strings.Join(textCells, "\t"))
	}
	htmlBuilder.WriteString("</tbody></table>")
	return MaterializedContent{HTML: htmlBuilder.String(), Text: strings.Join(textRows, "\n")}, nil
}

func renderInteractive(kind, title string) MaterializedContent {
	label := title
	if label == "" {
		label = "Interactive content"
	}
	return MaterializedContent{
		HTML: `<div data-block-kind="` + kind + `">` + html.EscapeString(label) + `</div>`,
		Text: label,
	}
}

func renderFile(
	ctx context.Context,
	rawBlockID string,
	base *contentv1.FileProps,
	localized *contentv1.FileLocaleProps,
	resolver FileRenderResolver,
) (MaterializedContent, error) {
	if base == nil || base.GetAttachment() == nil || localized == nil {
		return MaterializedContent{}, fmt.Errorf("%w: File Block payload is incomplete", ErrInvalidMutation)
	}
	name := base.GetName()
	caption := localized.GetCaption()
	alt := localized.GetAlt()
	text := strings.TrimSpace(strings.Join(nonemptyStrings(name, caption), "\n"))
	if missing := base.GetAttachment().GetMissingAttachment(); missing != nil {
		label := text
		if label == "" {
			label = "Attachment unavailable"
		}
		return MaterializedContent{
			HTML: `<span data-missing-attachment="true">` + html.EscapeString(label) + `</span>`,
			Text: label,
		}, nil
	}
	blockID, err := uuid.Parse(rawBlockID)
	if err != nil {
		return MaterializedContent{}, fmt.Errorf("%w: invalid File Block UUID", ErrInvalidMutation)
	}
	fileID, err := uuid.Parse(base.GetAttachment().GetActiveFileId())
	if err != nil {
		return MaterializedContent{}, fmt.Errorf("%w: invalid active File UUID", ErrFileReference)
	}
	if resolver == nil {
		return MaterializedContent{}, fmt.Errorf("%w: active File requires a render resolver", ErrFileReference)
	}
	target, err := resolver.ResolveContentBlockFile(ctx, FileRenderSelector{
		BlockID: blockID, ReferencePath: "file", FileID: fileID,
	})
	if err != nil {
		return MaterializedContent{}, fmt.Errorf("%w: resolve File render target: %v", ErrFileReference, err)
	}
	escapedCaption := html.EscapeString(caption)
	if target.URL == "" {
		label := text
		if label == "" {
			label = "Media"
		}
		return MaterializedContent{
			HTML: `<figure data-content-block-file="` + fileID.String() + `">` + html.EscapeString(label) + `</figure>`,
			Text: label,
		}, nil
	}
	if _, err := safePublicAssetURL(target.URL); err != nil {
		return MaterializedContent{}, err
	}
	escapedURL := html.EscapeString(target.URL)
	var body string
	switch {
	case strings.HasPrefix(target.MIMEType, "image/"):
		body = `<img src="` + escapedURL + `" alt="` + html.EscapeString(alt) + `">`
	case strings.HasPrefix(target.MIMEType, "audio/"):
		body = `<audio controls src="` + escapedURL + `"></audio>`
	case strings.HasPrefix(target.MIMEType, "video/"):
		body = `<video controls src="` + escapedURL + `"></video>`
	default:
		label := name
		if label == "" {
			label = "Download file"
		}
		body = `<a href="` + escapedURL + `">` + html.EscapeString(label) + `</a>`
	}
	if escapedCaption != "" {
		body += "<figcaption>" + escapedCaption + "</figcaption>"
	}
	return MaterializedContent{HTML: "<figure>" + body + "</figure>", Text: text}, nil
}

func safePublicAssetURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return nil, fmt.Errorf("%w: File render target must be an absolute HTTPS URL", ErrFileReference)
	}
	return parsed, nil
}

func nonemptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
