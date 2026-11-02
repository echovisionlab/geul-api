package worktest

import (
	"context"
	"maps"
	"reflect"
	"slices"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/optional"
	"github.com/echovisionlab/geul-api/internal/versionrestore"
	"github.com/echovisionlab/geul-api/internal/work"
	"github.com/google/uuid"

	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// The fake content document keeps only what the Work use cases branch on:
// the revision compare-and-set, the fence that runs first, whether a write
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

// CreateContentDocument implements work.Store.
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

// LockedContentSnapshot implements work.Store.
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

// ContentSnapshot implements work.Store.
func (s *Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.fail("ContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// MediaReferences implements work.Store.
func (s *Store) MediaReferences(_ context.Context, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error) {
	if err := s.fail("MediaReferences"); err != nil {
		return nil, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return nil, err
	}
	return slices.Clone(document.Media), nil
}

// LocaleOwnershipConflict implements work.Store with the conflict
// contentblock.LocaleOwnershipConflict answers.
func (s *Store) LocaleOwnershipConflict(_ context.Context, documentID uuid.UUID, message string) error {
	document, err := s.document(documentID)
	if err != nil {
		return err
	}
	return domainerr.CollaborationConflict(
		intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_LOCALE_OWNERSHIP_CHANGED, message,
		domainerr.CollaborationRevisions{Document: document.Revision.String()},
	)
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence work.ContentFence) (Document, contentblock.DomainContext, error) {
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

// batchResult is what a batch changes: any mutation changes the document,
// and touching the source locale or the shared graph changes the
// translation source. A queued result overrides it.
func (s *Store) batchResult(batch contentblock.Batch, domain contentblock.DomainContext) contentblock.Result {
	if result, ok := s.nextResult(); ok {
		return result
	}
	if !batchChanges(batch) {
		return contentblock.Result{}
	}
	sourceChanged := len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	return contentblock.Result{
		Changed: true, ContentChanged: true, TranslationSourceChanged: sourceChanged, ChangedLocales: batchLocales(batch),
	}
}

// ApplyContentBatch implements work.Store.
func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence work.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	return s.commit(document, s.batchResult(batch, domain)), nil
}

// ApplyContentBatchWithMetadata implements work.Store: the metadata effect
// runs after the Blocks change and joins the result.
func (s *Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence work.ContentFence, effect work.MetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatchWithMetadata"); err != nil {
		return contentblock.Result{}, err
	}
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	result := s.batchResult(batch, domain)
	metadata, err := effect(ctx, s)
	if err != nil {
		return contentblock.Result{}, err
	}
	if metadata.Changed {
		result.Changed, result.MetadataChanged = true, true
		result.TranslationSourceChanged = result.TranslationSourceChanged || metadata.AffectsTranslationSource
		for _, locale := range metadata.ChangedLocales {
			if !slices.Contains(result.ChangedLocales, locale) {
				result.ChangedLocales = append(result.ChangedLocales, locale)
			}
		}
	}
	return s.commit(document, result), nil
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

// ApplyTargetLocaleContentBatch implements work.Store. A target-locale
// batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence work.ContentFence, effect work.TargetMetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyTargetLocaleContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	contentChanged := false
	for _, group := range batch.LocaleGroups {
		contentChanged = contentChanged || len(group.Upserts) != 0 || len(group.Deletes) != 0
	}
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

// ReplaceContentSnapshot implements work.Store; the snapshot changes the
// document when its Blocks differ.
func (s *Store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence work.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ReplaceContentSnapshot"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, replace.DocumentID, replace.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if len(document.Blocks) == 0 && len(replace.Blocks) == 0 || reflect.DeepEqual(document.Blocks, replace.Blocks) {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	document.Blocks = replace.Blocks
	return s.commit(document, contentblock.Result{Changed: true, ContentChanged: true, TranslationSourceChanged: true}), nil
}

// AdvanceContentRevision implements work.Store.
func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence work.ContentFence, effect work.MetadataEffect) (contentblock.AdvanceResult, error) {
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
	if metadata.SourceLocale != "" {
		document.SourceLocale = metadata.SourceLocale
	}
	result := s.commit(document, contentblock.Result{Changed: true})
	return contentblock.AdvanceResult{
		DocumentRevision: result.DocumentRevision, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource,
	}, nil
}

// DeleteContentDocument implements work.Store.
func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence work.ContentFence) error {
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

// RestoreVersion implements work.Store with versionrestore's flow over the
// fake document: put the version's source locale back, replace the Blocks,
// and restore the title and summary, advancing the revision once.
func (s *Store) RestoreVersion(ctx context.Context, restore work.VersionRestore) (versionrestore.Outcome, error) {
	if err := s.fail("RestoreVersion"); err != nil {
		return versionrestore.Outcome{}, err
	}
	load := func() (versionrestore.Source, Document, error) {
		source, err := restore.LoadSource(ctx, s)
		if err != nil {
			return versionrestore.Source{}, Document{}, err
		}
		document, err := s.document(restore.DocumentID)
		return source, document, err
	}
	source, current, err := load()
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	var outcome versionrestore.Outcome
	if source.Locale != restore.Version.Locale {
		if err := restore.SwitchSourceLocale(ctx, s, source.Locale, current.Revision); err != nil {
			return versionrestore.Outcome{}, err
		}
		outcome.SourceLocaleSwitched = true
		if source, current, err = load(); err != nil {
			return versionrestore.Outcome{}, err
		}
	}
	outcome.TitleChanged = !optional.Equal(source.Title, restore.Version.Title)
	outcome.SummaryChanged = !optional.Equal(source.Summary, restore.Version.Summary)
	input, err := restore.Replacement(ctx, s, current.Revision)
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	replaced, err := s.ReplaceContentSnapshot(ctx, input, restore.Fence)
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	outcome.ContentChanged, outcome.DocumentRevision = replaced.Changed, replaced.DocumentRevision
	if !outcome.TitleChanged && !outcome.SummaryChanged {
		return outcome, nil
	}
	if replaced.Changed {
		return outcome, restore.UpdateMetadata(ctx, s)
	}
	advanced, err := s.AdvanceContentRevision(ctx,
		contentblock.AdvanceInput{DocumentID: restore.DocumentID, ExpectedRevision: replaced.DocumentRevision},
		restore.Fence,
		func(ctx context.Context, store work.Store) (contentblock.MetadataEffect, error) {
			if err := restore.UpdateMetadata(ctx, store); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
		},
	)
	if err != nil {
		return versionrestore.Outcome{}, err
	}
	outcome.DocumentRevision = advanced.DocumentRevision
	return outcome, nil
}
