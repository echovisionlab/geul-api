package programeventtest

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/programevent"
	"github.com/google/uuid"
)

// The fake content document keeps only what the Program Event use cases
// branch on: the revision compare-and-set, the fence that runs first,
// whether a batch changes anything, and that only a source change advances
// the shared revision. Block validation stays the content store's; the
// seeded snapshot keeps its Blocks.

func (s *Store) document(documentID uuid.UUID) (contentblock.Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot, ok := s.documents[documentID]
	if !ok {
		return contentblock.Snapshot{}, contentblock.ErrDocumentNotFound
	}
	return snapshot, nil
}

func (s *Store) putDocument(snapshot contentblock.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[snapshot.Document.ID] = snapshot
}

func (s *Store) nextResult() (contentblock.Result, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ContentResults) == 0 {
		return contentblock.Result{}, false
	}
	result := s.ContentResults[0]
	s.ContentResults = s.ContentResults[1:]
	return result, true
}

// CreateContentDocument implements programevent.Store.
func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	if err := s.enter("CreateContentDocument"); err != nil {
		return contentblock.Snapshot{}, err
	}
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: input.ID, Profile: input.Profile, Revision: uuid.New()},
		SourceLocale: input.SourceLocale,
	}
	if snapshot.Document.ID == uuid.Nil {
		snapshot.Document.ID = uuid.New()
	}
	s.putDocument(snapshot)
	return snapshot, nil
}

// ContentSnapshot implements programevent.Store.
func (s *Store) ContentSnapshot(_ context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.enter("ContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	snapshot, err := s.document(documentID)
	if err != nil {
		return contentblock.Snapshot{}, err
	}
	snapshot.SourceLocale = sourceLocale
	return snapshot, nil
}

// MediaReferences implements programevent.Store.
func (s *Store) MediaReferences(_ context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error) {
	if err := s.enter("MediaReferences"); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.BlockMedia[documentID]
	if items == nil {
		return []*fileref.BlockMediaItem{}, nil
	}
	return items, nil
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence programevent.ContentFence) (contentblock.Snapshot, contentblock.DomainContext, error) {
	domain, err := fence(ctx, s, documentID)
	if err != nil {
		return contentblock.Snapshot{}, contentblock.DomainContext{}, err
	}
	snapshot, err := s.document(documentID)
	if err != nil {
		return contentblock.Snapshot{}, contentblock.DomainContext{}, err
	}
	if expected != snapshot.Document.Revision {
		return contentblock.Snapshot{}, contentblock.DomainContext{}, &contentblock.StaleRevisionError{CurrentRevision: snapshot.Document.Revision}
	}
	return snapshot, domain, nil
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

// commit stores the document after a changed result, which advances its
// revision unless the result names one.
func (s *Store) commit(snapshot contentblock.Snapshot, result contentblock.Result) contentblock.Result {
	if result.Changed && result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = uuid.New()
	}
	if result.DocumentRevision == uuid.Nil {
		result.DocumentRevision = snapshot.Document.Revision
	}
	snapshot.Document.Revision = result.DocumentRevision
	s.putDocument(snapshot)
	return result
}

func (s *Store) applyBatch(ctx context.Context, batch contentblock.Batch, fence programevent.ContentFence, effect programevent.MetadataEffect) (contentblock.Result, error) {
	snapshot, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(snapshot, result), nil
	}
	var metadata contentblock.MetadataEffect
	if effect != nil {
		if metadata, err = effect(ctx, s); err != nil {
			return contentblock.Result{}, err
		}
	}
	if !batchChanges(batch) && !metadata.Changed {
		return contentblock.Result{DocumentRevision: snapshot.Document.Revision}, nil
	}
	sourceChanged := len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || metadata.AffectsTranslationSource
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	return s.commit(snapshot, contentblock.Result{
		Changed: true, ContentChanged: batchChanges(batch), MetadataChanged: metadata.Changed,
		TranslationSourceChanged: sourceChanged, ChangedLocales: batchLocales(batch),
	}), nil
}

// ApplyContentBatch implements programevent.Store. A batch with any
// mutation changes the document and advances its revision; touching the
// source locale or the shared graph changes the translation source.
func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence programevent.ContentFence) (contentblock.Result, error) {
	if err := s.enter("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, nil)
}

// ApplyContentBatchWithMetadata implements programevent.Store.
func (s *Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence programevent.ContentFence, effect programevent.MetadataEffect) (contentblock.Result, error) {
	if err := s.enter("ApplyContentBatchWithMetadata"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, effect)
}

// ApplyTargetLocaleContentBatch implements programevent.Store. A
// target-locale batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence programevent.ContentFence, effect programevent.TargetMetadataEffect) (contentblock.Result, error) {
	if err := s.enter("ApplyTargetLocaleContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	snapshot, _, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	contentChanged := len(batch.LocaleGroups) != 0
	metadata, err := effect(ctx, s, contentChanged)
	if err != nil {
		return contentblock.Result{}, err
	}
	result := contentblock.Result{
		DocumentRevision: snapshot.Document.Revision, Changed: contentChanged || metadata.Changed,
		ContentChanged: contentChanged, MetadataChanged: metadata.Changed,
	}
	if result.Changed {
		result.ChangedLocales = []string{locale}
	}
	return result, nil
}

// AdvanceContentRevision implements programevent.Store.
func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence programevent.ContentFence, effect programevent.MetadataEffect) (contentblock.AdvanceResult, error) {
	if err := s.enter("AdvanceContentRevision"); err != nil {
		return contentblock.AdvanceResult{}, err
	}
	snapshot, _, err := s.admit(ctx, input.DocumentID, input.ExpectedRevision, fence)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	metadata, err := effect(ctx, s)
	if err != nil {
		return contentblock.AdvanceResult{}, err
	}
	if !metadata.Changed {
		return contentblock.AdvanceResult{DocumentRevision: snapshot.Document.Revision}, nil
	}
	result := s.commit(snapshot, contentblock.Result{Changed: true})
	return contentblock.AdvanceResult{
		DocumentRevision: result.DocumentRevision, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource,
	}, nil
}

// DeleteContentDocument implements programevent.Store.
func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence programevent.ContentFence) error {
	if err := s.enter("DeleteContentDocument"); err != nil {
		return err
	}
	if _, err := fence(ctx, s, documentID); err != nil {
		return err
	}
	if _, err := s.document(documentID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.documents, documentID)
	return nil
}
