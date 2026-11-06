// Package documenttext changes text across documents and locales without a read
// or a revision from the caller. Each document's edits, in every locale and in
// its translation units, commit or roll back together; each document stands alone.
package documenttext

import (
	"context"
	"errors"
	"fmt"
	"slices"

	core "github.com/echovisionlab/geul-api/internal/aidocument"
	translationapp "github.com/echovisionlab/geul-api/internal/translation/application"
)

// MaxDocuments bounds the documents one Edit changes.
const MaxDocuments = 50

// Documents is what an edit needs of the document application.
type Documents interface {
	Open(context.Context, core.OpenRequest) (core.OpenMetadata, error)
	Read(context.Context, core.ReadRequest) (core.Projection, error)
	Apply(context.Context, core.ApplyRequest) (core.ApplyResult, error)
}

// TranslationUnits writes a document's translation units.
type TranslationUnits interface {
	EditDocumentUnits(context.Context, core.DocumentIdentity, []translationapp.TranslationUnitEdit) (map[string]translationapp.XLIFFImport, error)
}

// Transaction runs work so that every mutation under its context commits or rolls
// back together.
type Transaction func(ctx context.Context, work func(context.Context) error) error

// Service applies text edits.
type Service struct {
	documents    Documents
	translations TranslationUnits
	transaction  Transaction
}

func NewService(documents Documents, translations TranslationUnits, transaction Transaction) (*Service, error) {
	switch {
	case documents == nil:
		return nil, errors.New("document text service needs documents")
	case translations == nil:
		return nil, errors.New("document text service needs translation units")
	case transaction == nil:
		return nil, errors.New("document text service needs a transaction")
	}
	return &Service{documents: documents, translations: translations, transaction: transaction}, nil
}

// Edit is one text edit of one document's locale. Translations, on a source-locale
// edit, sets the whole translated text per locale of the unit holding the
// replacement.
type Edit struct {
	Document     core.DocumentIdentity
	Locale       core.Locale
	TextEdit     core.TextEdit
	Translations map[string]string
}

// LocaleResult is what became of one locale. A dry run fills Previews and leaves the
// revisions empty, because the writes that made them rolled back. When the same edit
// also wrote the source, DocumentRevision is the one that write left and
// TargetRevision is empty, because that write moved it.
type LocaleResult struct {
	Locale           core.Locale
	DocumentRevision core.Revision
	TargetRevision   *core.Revision
	Blocks           []core.BlockID
	Previews         []core.TextEditPreview
}

// DocumentResult is what became of one document: Err when none of its edits
// applied, and otherwise what they did.
type DocumentResult struct {
	Document     core.DocumentIdentity
	Locales      []LocaleResult
	Translations map[string]translationapp.XLIFFImport
	Err          error
}

// RefusalError is an edit that cannot apply, with its position among the edits
// given when one edit is to blame.
type RefusalError struct {
	Message   string
	EditIndex *int
}

func (refusal *RefusalError) Error() string { return refusal.Message }

// errDryRun rolls a dry run's transaction back after it has done all of the
// edit's work and checks.
var errDryRun = errors.New("text edit dry run")

// plannedEdit is an edit with its position among the edits given.
type plannedEdit struct {
	Edit
	position int
}

type documentEdits struct {
	document core.DocumentIdentity
	edits    []plannedEdit
}

// EditDocuments applies edits one after another per document, each document in
// its own transaction, and reports each document. With dryRun every edit runs and
// rolls back.
func (service *Service) EditDocuments(ctx context.Context, edits []Edit, dryRun bool) ([]DocumentResult, error) {
	documents, err := groupByDocument(edits)
	if err != nil {
		return nil, err
	}
	results := make([]DocumentResult, 0, len(documents))
	for _, document := range documents {
		results = append(results, service.editDocument(ctx, document, dryRun))
	}
	return results, nil
}

// groupByDocument collects edits per document in the order first named.
func groupByDocument(edits []Edit) ([]*documentEdits, error) {
	var documents []*documentEdits
	byID := map[core.DocumentReference]*documentEdits{}
	for position, edit := range edits {
		document, seen := byID[edit.Document.Reference]
		if !seen {
			document = &documentEdits{document: edit.Document}
			byID[edit.Document.Reference] = document
			documents = append(documents, document)
		} else if document.document.Domain != edit.Document.Domain {
			return nil, &core.InputError{Message: fmt.Sprintf("edit %d names document %s with a second document_type", position, edit.Document.Reference)}
		}
		document.edits = append(document.edits, plannedEdit{Edit: edit, position: position})
	}
	if len(documents) > MaxDocuments {
		return nil, &core.InputError{Message: fmt.Sprintf("one call can edit at most %d documents; send the rest in another call", MaxDocuments)}
	}
	return documents, nil
}

// editDocument tries once more when the document moved between the read and the
// write: find still guards what the edit means, so a fresh read decides it again.
func (service *Service) editDocument(ctx context.Context, document *documentEdits, dryRun bool) DocumentResult {
	result, err := service.attemptDocumentEdit(ctx, document, dryRun)
	if IsConflict(err) {
		result, err = service.attemptDocumentEdit(ctx, document, dryRun)
	}
	if err != nil && !errors.Is(err, errDryRun) {
		return DocumentResult{Document: document.document, Err: err}
	}
	result.Document = document.document
	return result
}

// IsConflict reports a document that moved during an edit: a write refused its
// revision, or a read's next page no longer follows the first.
func IsConflict(err error) bool {
	var conflict *core.ConflictError
	var validation *core.ValidationError
	var cursor *core.CursorError
	return errors.As(err, &conflict) || errors.As(err, &cursor) || (errors.As(err, &validation) && validation.Result.Conflict != nil)
}

// attemptDocumentEdit reads outside the transaction, so the writes hold their
// locks only as long as they need. After a dry run it returns errDryRun with what
// was rolled back.
func (service *Service) attemptDocumentEdit(ctx context.Context, document *documentEdits, dryRun bool) (DocumentResult, error) {
	writes, unitEdits, err := service.planDocumentWrites(ctx, document)
	if err != nil {
		return DocumentResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, documentTimeout)
	defer cancel()
	var result DocumentResult
	err = service.transaction(ctx, func(ctx context.Context) error {
		locales, err := service.applyLocaleWrites(ctx, writes, dryRun)
		if err != nil {
			return err
		}
		var translations map[string]translationapp.XLIFFImport
		if len(unitEdits) != 0 {
			translations, err = service.translations.EditDocumentUnits(ctx, document.document, unitEdits)
			if err != nil {
				return err
			}
		}
		result.Locales, result.Translations = locales, translations
		if dryRun {
			return errDryRun
		}
		return nil
	})
	return result, err
}

func (service *Service) applyLocaleWrites(ctx context.Context, writes []localeWrite, dryRun bool) ([]LocaleResult, error) {
	results := make([]LocaleResult, 0, len(writes))
	for _, write := range writes {
		applied, err := service.documents.Apply(ctx, write.request)
		if err != nil {
			return nil, err
		}
		result := LocaleResult{Locale: write.request.Locale, Blocks: write.blocks}
		if dryRun {
			result.Previews = write.previews
		} else {
			result.DocumentRevision, result.TargetRevision = applied.DocumentRevision, applied.TargetRevision
		}
		results = append(results, result)
	}
	if !dryRun {
		dropRevisionsTheSourceMoved(results, writes)
	}
	return results, nil
}

// dropRevisionsTheSourceMoved keeps a caller from continuing with a revision the
// source write, which comes last, has already replaced.
func dropRevisionsTheSourceMoved(results []LocaleResult, writes []localeWrite) {
	if len(writes) < 2 || !writes[len(writes)-1].isSourceLocale {
		return
	}
	last := results[len(results)-1]
	for index := range results[:len(results)-1] {
		results[index].DocumentRevision, results[index].TargetRevision = last.DocumentRevision, nil
	}
}

func appendMissing[Value comparable](values []Value, value Value) []Value {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}
