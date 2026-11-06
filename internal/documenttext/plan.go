package documenttext

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	"github.com/echovisionlab/geul-api/internal/translation"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

// documentTimeout bounds the shared transaction of one document, so a lock it can
// never get ends the edit instead of the request.
const documentTimeout = 30 * time.Second

// localeWrite is what one locale's reads decided to write.
type localeWrite struct {
	request        core.ApplyRequest
	blocks         []core.BlockID
	previews       []core.TextEditPreview
	isSourceLocale bool
}

// planDocumentWrites puts a translation's write before the source's: writing the
// source moves every translation's revision, and the reads hold the current one.
func (service *Service) planDocumentWrites(ctx context.Context, document *documentEdits) ([]localeWrite, []translationapp.TranslationUnitEdit, error) {
	var writes []localeWrite
	var unitEdits []translationapp.TranslationUnitEdit
	for _, edits := range editsByLocale(document.edits) {
		write, translations, err := service.planLocaleWrite(ctx, document.document, edits)
		if err != nil {
			return nil, nil, err
		}
		if write.isSourceLocale {
			writes = append(writes, write)
		} else {
			writes = slices.Insert(writes, 0, write)
		}
		unitEdits = append(unitEdits, translations...)
	}
	return writes, unitEdits, nil
}

// editsByLocale groups a document's edits per locale in the order first named.
func editsByLocale(edits []plannedEdit) [][]plannedEdit {
	var locales [][]plannedEdit
	positions := map[core.Locale]int{}
	for _, edit := range edits {
		position, seen := positions[edit.Locale]
		if !seen {
			position = len(locales)
			positions[edit.Locale] = position
			locales = append(locales, nil)
		}
		locales[position] = append(locales[position], edit)
	}
	return locales
}

// planLocaleWrite reads the blocks a locale's edits touch and computes its write.
func (service *Service) planLocaleWrite(ctx context.Context, document core.DocumentIdentity, edits []plannedEdit) (localeWrite, []translationapp.TranslationUnitEdit, error) {
	locale := edits[0].Locale
	read, err := service.readEditedBlocks(ctx, document, edits)
	if err != nil {
		return localeWrite{}, nil, err
	}
	textEdits := make([]core.TextEdit, len(edits))
	for index, edit := range edits {
		textEdits[index] = edit.TextEdit
	}
	plan, err := core.PlanTextEdits(read.nodes, textEdits)
	if err != nil {
		return localeWrite{}, nil, refusalOfLocaleEdit(edits, err)
	}
	unitEdits, err := translationUnitEdits(edits, plan.Matches, locale, read.sourceLocale)
	if err != nil {
		return localeWrite{}, nil, err
	}
	changedBlocks := []core.BlockID{}
	for _, match := range plan.Matches {
		for _, block := range match.Blocks {
			changedBlocks = appendMissing(changedBlocks, block)
		}
	}
	return localeWrite{
		isSourceLocale: locale == read.sourceLocale, blocks: changedBlocks, previews: plan.Previews,
		request: core.ApplyRequest{
			Protocol: core.ProtocolVersion, Profile: document.Domain, Document: document.Reference, Locale: locale,
			ExpectedDocumentRevision: read.documentRevision, ExpectedTargetRevision: read.targetRevision, Operations: plan.Operations,
		},
	}, unitEdits, nil
}

// refusalOfLocaleEdit names the failed edit by its position among all the edits.
func refusalOfLocaleEdit(edits []plannedEdit, err error) error {
	var editError *core.TextEditError
	if !errors.As(err, &editError) {
		return err
	}
	return &RefusalError{Message: editError.Message, EditIndex: &edits[editError.Index].position}
}

func translationUnitEdits(edits []plannedEdit, matches []core.TextEditMatch, locale, sourceLocale core.Locale) ([]translationapp.TranslationUnitEdit, error) {
	var unitEdits []translationapp.TranslationUnitEdit
	for index, match := range matches {
		edit := edits[index]
		if len(edit.Translations) == 0 {
			continue
		}
		if locale != sourceLocale {
			return nil, &RefusalError{
				Message:   fmt.Sprintf("translations belong on an edit of the source locale %s; edit a translation's words with its own locale", sourceLocale),
				EditIndex: &edit.position,
			}
		}
		for _, field := range match.Fields {
			unitEdits = append(unitEdits, translationUnitEdit(*edit.TextEdit.Replace, field, edit.Translations))
		}
	}
	return unitEdits, nil
}

// translationUnitEdit narrows to the block when it is a content block, whose
// handle is a UUID. A document's root holds fields like the title under a handle of
// its own, whose units carry no block but are named after the field.
func translationUnitEdit(replacement string, field core.FieldTarget, targets map[string]string) translationapp.TranslationUnitEdit {
	edit := translationapp.TranslationUnitEdit{SourceContains: replacement, Targets: targets}
	switch {
	case uuid.Validate(string(field.Block)) == nil:
		edit.Block = string(field.Block)
	case field.Relation == "":
		edit.FieldUnit = translation.EntityUnitID(string(field.Field))
	}
	return edit
}

// localeRead is what a locale's reads saw. The revisions are those of the first
// read: Apply refuses them if the document moved since.
type localeRead struct {
	nodes            []core.Node
	documentRevision core.Revision
	targetRevision   *core.Revision
	sourceLocale     core.Locale
}

func (read *localeRead) adoptFirstRead(first localeRead) {
	if read.documentRevision == "" {
		read.documentRevision, read.targetRevision, read.sourceLocale = first.documentRevision, first.targetRevision, first.sourceLocale
	}
}

// readEditedBlocks finds the blocks a locale's edits can touch and reads them.
func (service *Service) readEditedBlocks(ctx context.Context, document core.DocumentIdentity, edits []plannedEdit) (localeRead, error) {
	locale := edits[0].Locale
	var read localeRead
	var blockIDs []core.BlockID
	for _, edit := range edits {
		if edit.TextEdit.Block != "" {
			blockIDs = appendMissing(blockIDs, edit.TextEdit.Block)
			continue
		}
		matches, err := service.readAllPages(ctx, core.ReadRequest{
			Document: document, Locale: locale, Mode: core.ReadOutline, Query: outlineQueryForSelection(edit.TextEdit), Limit: core.DefaultOutlineReadLimit,
		})
		if err != nil {
			return localeRead{}, err
		}
		read.adoptFirstRead(matches)
		for _, node := range matches.nodes {
			blockIDs = appendMissing(blockIDs, node.ID)
		}
	}
	for chunk := range slices.Chunk(blockIDs, core.DefaultValueReadLimit) {
		values, err := service.readAllPages(ctx, core.ReadRequest{Document: document, Locale: locale, Mode: core.ReadBlocks, Blocks: chunk, Limit: core.DefaultValueReadLimit})
		if err != nil {
			return localeRead{}, err
		}
		read.adoptFirstRead(values)
		read.nodes = append(read.nodes, values.nodes...)
	}
	if read.documentRevision != "" {
		return read, nil
	}
	metadata, err := service.documents.Open(ctx, core.OpenRequest{Document: document, Locale: locale})
	if err != nil {
		return localeRead{}, err
	}
	read.documentRevision, read.targetRevision, read.sourceLocale = metadata.DocumentRevision, metadata.TargetRevision, metadata.SourceLocale
	return read, nil
}

// outlineQueryForSelection is the start of the selection, which matches every
// block that contains all of it.
func outlineQueryForSelection(edit core.TextEdit) string {
	selection := []rune(cmp.Or(edit.Find, edit.FindLink))
	return string(selection[:min(len(selection), core.MaxReadQueryRunes)])
}

// readAllPages follows next cursors and keeps the first page's revisions.
func (service *Service) readAllPages(ctx context.Context, request core.ReadRequest) (localeRead, error) {
	var read localeRead
	for {
		projection, err := service.documents.Read(ctx, request)
		if err != nil {
			return localeRead{}, err
		}
		read.adoptFirstRead(localeRead{documentRevision: projection.DocumentRevision, targetRevision: projection.TargetRevision, sourceLocale: projection.SourceLocale})
		read.nodes = append(read.nodes, projection.Nodes...)
		if projection.Next == nil {
			return read, nil
		}
		request.Cursor = *projection.Next
	}
}
