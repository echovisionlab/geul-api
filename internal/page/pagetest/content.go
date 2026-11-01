package pagetest

import (
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/page"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// The fake content document keeps only what the Page use cases branch on:
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
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: document.ID, Profile: document.Profile, Revision: document.Revision},
		SourceLocale: sourceLocale, Blocks: document.Blocks,
	}
	for _, locale := range slices.Sorted(maps.Keys(document.Locales)) {
		if locale != sourceLocale {
			snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, contentblock.LocaleOverlay{Locale: locale})
		}
	}
	return snapshot
}

// CreateContentDocument implements page.Store.
func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	if err := s.fail("CreateContentDocument"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document := Document{ID: input.ID, Profile: input.Profile, Revision: uuid.New()}
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	s.mu.Lock()
	s.documents[document.ID] = document
	s.mu.Unlock()
	return snapshotOf(document, input.SourceLocale), nil
}

// LockedContentSnapshot implements page.Store.
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

// ContentSnapshot implements page.Store.
func (s *Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence page.ContentFence) (Document, contentblock.DomainContext, error) {
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
	s.mu.Lock()
	s.documents[document.ID] = document
	s.mu.Unlock()
	return result
}

func (s *Store) applyBatch(ctx context.Context, batch contentblock.Batch, fence page.ContentFence, effect page.MetadataEffect) (contentblock.Result, error) {
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(document, result), nil
	}
	var metadata contentblock.MetadataEffect
	if effect != nil {
		if metadata, err = effect(ctx, s); err != nil {
			return contentblock.Result{}, err
		}
	}
	if !batchChanges(batch) && !metadata.Changed {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	sourceChanged := len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || metadata.AffectsTranslationSource
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	return s.commit(document, contentblock.Result{
		Changed: true, ContentChanged: batchChanges(batch), MetadataChanged: metadata.Changed,
		TranslationSourceChanged: sourceChanged, ChangedLocales: append(batchLocales(batch), metadata.ChangedLocales...),
	}), nil
}

// ApplyContentBatch implements page.Store. A batch with any mutation
// changes the document and advances its revision; touching the source
// locale or the shared graph changes the translation source.
func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence page.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, nil)
}

// ApplyContentBatchWithMetadata implements page.Store.
func (s *Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence page.ContentFence, effect page.MetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatchWithMetadata"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, effect)
}

// ApplyTargetLocaleContentBatch implements page.Store. A target-locale
// batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence page.ContentFence, effect page.TargetMetadataEffect) (contentblock.Result, error) {
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
		s.PutDocumentLocale(document.ID, locale)
	}
	return result, nil
}

// ReplaceContentSnapshot implements page.Store. The fake reports a change
// only when the replacement carries Blocks.
func (s *Store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence page.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ReplaceContentSnapshot"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, replace.DocumentID, replace.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if result, ok := s.nextResult(); ok {
		return s.commit(document, result), nil
	}
	if len(replace.Blocks) == 0 && len(document.Blocks) == 0 {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	document.Blocks = replace.Blocks
	return s.commit(document, contentblock.Result{Changed: true, ContentChanged: true, TranslationSourceChanged: true}), nil
}

// AdvanceContentRevision implements page.Store.
func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence page.ContentFence, effect page.MetadataEffect) (contentblock.AdvanceResult, error) {
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

// DeleteContentDocument implements page.Store.
func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence page.ContentFence) error {
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

// LocaleOwnershipConflict implements page.Store.
func (s *Store) LocaleOwnershipConflict(_ context.Context, documentID uuid.UUID, _ string) error {
	document, err := s.document(documentID)
	if err != nil {
		return err
	}
	return &contentblock.StaleRevisionError{CurrentRevision: document.Revision}
}

// PublicationAttachments implements page.Store.
func (s *Store) PublicationAttachments(_ context.Context, documentID uuid.UUID) ([]contentblock.PublicationAttachment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.attachments[documentID]), nil
}

// MediaReferences implements page.Store.
func (s *Store) MediaReferences(_ context.Context, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.blockMedia[documentID]), nil
}

// LockAttachableFiles implements page.Store.
func (s *Store) LockAttachableFiles(_ context.Context, fileIDs []string) error {
	if err := s.fail("LockAttachableFiles"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lockedFiles = append(s.lockedFiles, fileIDs...)
	return nil
}

// FileExists implements page.Store.
func (s *Store) FileExists(_ context.Context, fileID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.files[fileID]; !ok {
		return page.ErrFileNotFound
	}
	return nil
}

// PublicationFiles implements page.Store.
func (s *Store) PublicationFiles(_ context.Context, fileIDs []string) ([]page.PublicationFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	files := make([]page.PublicationFile, 0, len(fileIDs))
	for _, fileID := range slices.Sorted(slices.Values(fileIDs)) {
		if file, ok := s.files[fileID]; ok {
			files = append(files, file)
		}
	}
	return files, nil
}

// PublicationPresentations implements page.Store.
func (s *Store) PublicationPresentations(context.Context, []string) (page.Presentations, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return page.Presentations{
		SourceAssets: maps.Clone(s.presentations.SourceAssets), Derivatives: maps.Clone(s.presentations.Derivatives),
	}, nil
}

// ReleaseFeaturedImageBindings implements page.Store.
func (s *Store) ReleaseFeaturedImageBindings(_ context.Context, pageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, pageID+":featured_image*")
	return nil
}

// ReleaseExactFeaturedImageBinding implements page.Store.
func (s *Store) ReleaseExactFeaturedImageBinding(_ context.Context, pageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = append(s.released, pageID+":featured_image")
	return nil
}

// ReadyAssets implements page.Store.
func (s *Store) ReadyAssets(_ context.Context, assetIDs []string) (map[string]*commonv1.AssetRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	refs := map[string]*commonv1.AssetRef{}
	for _, id := range assetIDs {
		if ref, ok := s.readyAssets[id]; ok {
			refs[id] = ref
		}
	}
	return refs, nil
}

// UnavailableAttachments implements page.Store.
func (s *Store) UnavailableAttachments(context.Context, *contentv1.LocalizedPageDocument) (map[uuid.UUID]contentv1.MissingAttachmentMediaKind, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.unavailable), nil
}

// HydrateBlockMedia implements page.Store; the fake delivers the media as
// referenced.
func (s *Store) HydrateBlockMedia(_ context.Context, _ string, _ uuid.UUID, _ *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return items, nil
}

// RequestOG implements page.Store.
func (s *Store) RequestOG(_ context.Context, pageID, locale string, allLocales bool, reason string) (string, error) {
	if err := s.fail("RequestOG"); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogRequests = append(s.ogRequests, OGRequest{PageID: pageID, Locale: locale, AllLocales: allLocales, Reason: reason})
	return s.OGRunID, nil
}

// CancelOG implements page.Store.
func (s *Store) CancelOG(_ context.Context, pageID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ogCancels = append(s.ogCancels, pageID)
	return nil
}
