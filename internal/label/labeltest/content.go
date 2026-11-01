package labeltest

import (
	"context"
	"maps"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/label"
	"github.com/google/uuid"
)

// The fake content document keeps only what the Label use cases branch on:
// the revision compare-and-set, the fence that runs first, whether a batch
// changes anything, and that only a source-locale change advances the
// shared revision. Block validation stays the content store's.

func (s *Store) document(documentID uuid.UUID) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	if !ok {
		return Document{}, contentblock.ErrDocumentNotFound
	}
	return document, nil
}

func (s *Store) putDocument(document Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[document.ID] = document
}

func (s *Store) nextResult() (contentblock.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.contentResults) == 0 {
		return contentblock.Result{}, false
	}
	result := s.contentResults[0]
	s.contentResults = s.contentResults[1:]
	return result, true
}

func snapshotOf(document Document, sourceLocale string) contentblock.Snapshot {
	return contentblock.Snapshot{
		Document:     contentblock.Document{ID: document.ID, Profile: document.Profile, Revision: document.Revision},
		SourceLocale: sourceLocale, Blocks: document.Blocks,
	}
}

// CreateContentDocument implements label.Store.
func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	if err := s.fail("CreateContentDocument"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document := Document{ID: input.ID, Profile: input.Profile, Revision: uuid.New(), SourceLocale: input.SourceLocale}
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	s.putDocument(document)
	return snapshotOf(document, input.SourceLocale), nil
}

// LockedContentSnapshot implements label.Store.
func (s *Store) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.fail("LockedContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return contentblock.Snapshot{}, err
	}
	return snapshotOf(document, sourceLocale), nil
}

// ContentSnapshot implements label.Store.
func (s *Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// LockContentDocument implements label.Store.
func (s *Store) LockContentDocument(_ context.Context, documentID uuid.UUID) error {
	_, err := s.document(documentID)
	return err
}

// CurrentContentRevision implements label.Store.
func (s *Store) CurrentContentRevision(_ context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	document, err := s.document(documentID)
	return document.Revision, err
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence label.ContentFence) (Document, contentblock.DomainContext, error) {
	domain, err := fence(ctx, s, documentID)
	if err != nil {
		return Document{}, contentblock.DomainContext{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return Document{}, contentblock.DomainContext{}, err
	}
	if expected != document.Revision {
		return Document{}, contentblock.DomainContext{}, &contentblock.StaleRevisionError{CurrentRevision: document.Revision}
	}
	return document, domain, nil
}

func batchLocales(batch contentblock.Batch) []string {
	locales := make([]string, 0, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		locales = append(locales, group.Locale)
	}
	return locales
}

func batchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}

// ApplyContentBatch implements label.Store. A batch with any mutation
// changes the document and advances its revision; touching the source
// locale or the shared graph changes the translation source.
func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence label.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(document, result), nil
	}
	if !batchChanges(batch) {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	sourceChanged := len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	return s.commit(document, contentblock.Result{
		Changed: true, ContentChanged: true, TranslationSourceChanged: sourceChanged, ChangedLocales: batchLocales(batch),
	}), nil
}

// commit stores the document after a changed result, which advances its
// revision unless the result names one.
func (s *Store) commit(document Document, result contentblock.Result) contentblock.Result {
	if result.Changed && result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = uuid.New()
	}
	if result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = document.Revision
	}
	document.Revision = result.DocumentRevision
	document.Locales = maps.Clone(document.Locales)
	for _, locale := range result.ChangedLocales {
		if document.Locales == nil {
			document.Locales = map[string]bool{}
		}
		document.Locales[locale] = true
	}
	s.putDocument(document)
	return result
}

// ApplyTargetLocaleContentBatch implements label.Store. A target-locale
// batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence label.ContentFence, effect label.TargetMetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyTargetLocaleContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	contentChanged := len(batch.LocaleGroups) != 0
	metadata, err := effect(ctx, s, contentChanged)
	if err != nil {
		return contentblock.Result{}, err
	}
	result := contentblock.Result{
		DocumentRevision: document.Revision, Changed: contentChanged || metadata.Changed,
		ContentChanged: contentChanged, MetadataChanged: metadata.Changed,
	}
	if result.Changed {
		result.ChangedLocales = []string{locale}
	}
	return result, nil
}

// ReplaceContentSnapshot implements label.Store.
func (s *Store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence label.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ReplaceContentSnapshot"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, replace.DocumentID, replace.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	document.Blocks = replace.Blocks
	return s.commit(document, contentblock.Result{Changed: true, ContentChanged: true, TranslationSourceChanged: true}), nil
}

// AdvanceContentRevision implements label.Store.
func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence label.ContentFence, effect label.MetadataEffect) (contentblock.AdvanceResult, error) {
	if err := s.fail("AdvanceContentRevision"); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	document, _, err := s.admit(ctx, input.DocumentID, input.ExpectedRevision, fence)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	metadata, err := effect(ctx, s)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	if !metadata.Changed {
		return contentblock.AdvanceResult{DocumentRevision: document.Revision}, nil
	}
	result := s.commit(document, contentblock.Result{Changed: true})
	return contentblock.AdvanceResult{
		DocumentRevision: result.DocumentRevision, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource,
	}, nil
}

// DeleteContentDocument implements label.Store.
func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence label.ContentFence) error {
	if err := s.fail("DeleteContentDocument"); err != nil {
		return err
	}
	if _, err := s.document(documentID); err != nil {
		return err
	}
	if _, err := fence(ctx, s, documentID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.documents, documentID)
	return nil
}
