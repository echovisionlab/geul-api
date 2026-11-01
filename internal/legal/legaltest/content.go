package legaltest

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
)

// The answers of the fake Campaign, Share Link and table constraints.
var (
	errNoticeStarted        = domainerr.Precondition("legal notice delivery has already started")
	errPreviewTokenBound    = errors.New("automatic legal preview token is already bound to a different share link")
	errDuplicateTranslation = errors.New("legal translation locale already exists")
	// errSingleStatus is the unique index that keeps one active and one
	// scheduled version of a kind.
	errSingleStatus = errors.New("a kind holds one active and one scheduled version")
)

// The fake content document keeps what the Terms and Privacy use cases
// branch on: the revision compare-and-set behind the fence that runs first,
// whether a batch changes anything, that only a source-locale or graph
// change advances the shared revision, and the Block values of each locale
// so projections render them. Block validation stays the content store's.

func (d Document) clone() Document {
	d.Blocks = slices.Clone(d.Blocks)
	overlays := make(map[string][]contentblock.LocaleBlockUpdate, len(d.Overlays))
	for locale, blocks := range d.Overlays {
		overlays[locale] = slices.Clone(blocks)
	}
	d.Overlays = overlays
	return d
}

func (d Document) snapshot(sourceLocale string) contentblock.Snapshot {
	snapshot := contentblock.Snapshot{
		Document:     contentblock.Document{ID: d.ID, Profile: d.Profile, Revision: d.Revision},
		SourceLocale: sourceLocale, SnapshotDigest: "digest-" + d.Revision.String(),
		Blocks: slices.Clone(d.Blocks),
	}
	for _, locale := range slices.Sorted(maps.Keys(d.Overlays)) {
		snapshot.LocaleOverlays = append(snapshot.LocaleOverlays, contentblock.LocaleOverlay{
			Locale: locale, Blocks: slices.Clone(d.Overlays[locale]),
		})
	}
	return snapshot
}

func (s *Store) document(documentID uuid.UUID) (Document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	document, ok := s.documents[documentID]
	if !ok {
		return Document{}, contentblock.ErrDocumentNotFound
	}
	return document.clone(), nil
}

func (s *Store) putDocument(document Document) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.documents[document.ID] = document
}

// admit runs the fence and the revision compare-and-set of a write.
func (s *Store) admit(ctx context.Context, documentID, expected uuid.UUID, fence legal.ContentFence) (Document, contentblock.DomainContext, error) {
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

func graphChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0
}

func localeChanges(groups []contentblock.LocaleMutationGroup) bool {
	for _, group := range groups {
		if len(group.Upserts) != 0 || len(group.Deletes) != 0 {
			return true
		}
	}
	return false
}

// apply writes a batch into document.
func (d *Document) apply(batch contentblock.Batch) {
	for _, upsert := range batch.Upserts {
		index := slices.IndexFunc(d.Blocks, func(block contentblock.BaseBlock) bool { return block.ID == upsert.ID })
		if index < 0 {
			d.Blocks = append(d.Blocks, upsert)
		} else {
			d.Blocks[index] = upsert
		}
	}
	for _, deleted := range batch.Deletes {
		d.Blocks = slices.DeleteFunc(d.Blocks, func(block contentblock.BaseBlock) bool { return block.ID == deleted })
		for locale := range d.Overlays {
			d.Overlays[locale] = slices.DeleteFunc(d.Overlays[locale], func(block contentblock.LocaleBlockUpdate) bool { return block.BlockID == deleted })
		}
	}
	for _, group := range batch.LocaleGroups {
		blocks := d.Overlays[group.Locale]
		for _, upsert := range group.Upserts {
			index := slices.IndexFunc(blocks, func(block contentblock.LocaleBlockUpdate) bool { return block.BlockID == upsert.BlockID })
			if index < 0 {
				blocks = append(blocks, upsert)
			} else {
				blocks[index] = upsert
			}
		}
		for _, deleted := range group.Deletes {
			blocks = slices.DeleteFunc(blocks, func(block contentblock.LocaleBlockUpdate) bool { return block.BlockID == deleted })
		}
		if len(blocks) == 0 {
			delete(d.Overlays, group.Locale)
		} else {
			d.Overlays[group.Locale] = blocks
		}
	}
}

func batchLocales(batch contentblock.Batch) []string {
	locales := make([]string, 0, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		locales = append(locales, group.Locale)
	}
	return locales
}

// CreateContentDocument implements legal.Store.
func (s *Store) CreateContentDocument(_ context.Context, input contentblock.CreateInput) (contentblock.Snapshot, error) {
	if err := s.fail("CreateContentDocument"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document := Document{ID: input.ID, Profile: input.Profile, Revision: uuid.New(), Overlays: map[string][]contentblock.LocaleBlockUpdate{}}
	if document.ID == uuid.Nil {
		document.ID = uuid.New()
	}
	s.putDocument(document)
	return document.snapshot(input.SourceLocale), nil
}

// LockedContentSnapshot implements legal.Store.
func (s *Store) LockedContentSnapshot(_ context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	if err := s.fail("LockedContentSnapshot"); err != nil {
		return contentblock.Snapshot{}, err
	}
	document, err := s.document(documentID)
	if err != nil {
		return contentblock.Snapshot{}, err
	}
	return document.snapshot(sourceLocale), nil
}

// ContentSnapshot implements legal.Store.
func (s *Store) ContentSnapshot(ctx context.Context, documentID uuid.UUID, sourceLocale string) (contentblock.Snapshot, error) {
	return s.LockedContentSnapshot(ctx, documentID, sourceLocale)
}

// ContentRevision implements legal.Store.
func (s *Store) ContentRevision(_ context.Context, documentID uuid.UUID) (uuid.UUID, error) {
	document, err := s.document(documentID)
	return document.Revision, err
}

// ReplaceContentSnapshot implements legal.Store.
func (s *Store) ReplaceContentSnapshot(ctx context.Context, replace contentblock.ReplaceInput, fence legal.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ReplaceContentSnapshot"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, replace.DocumentID, replace.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	if len(replace.Blocks) == 0 && len(replace.LocaleOverlays) == 0 {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	document.Blocks = slices.Clone(replace.Blocks)
	for _, overlay := range replace.LocaleOverlays {
		document.Overlays[overlay.Locale] = slices.Clone(overlay.Blocks)
	}
	document.Revision = uuid.New()
	s.putDocument(document)
	return contentblock.Result{DocumentRevision: document.Revision, Changed: true, ContentChanged: true, TranslationSourceChanged: true}, nil
}

func (s *Store) applyBatch(ctx context.Context, batch contentblock.Batch, fence legal.ContentFence, effect legal.MetadataEffect) (contentblock.Result, error) {
	document, domain, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	var metadata contentblock.MetadataEffect
	if effect != nil {
		if metadata, err = effect(ctx, s); err != nil {
			return contentblock.Result{}, err
		}
	}
	contentChanged := graphChanges(batch) || localeChanges(batch.LocaleGroups)
	if !contentChanged && !metadata.Changed {
		return contentblock.Result{DocumentRevision: document.Revision}, nil
	}
	sourceChanged := graphChanges(batch) || metadata.AffectsTranslationSource
	for _, locale := range batchLocales(batch) {
		sourceChanged = sourceChanged || locale == domain.SourceLocale
	}
	document.apply(batch)
	document.Revision = uuid.New()
	s.putDocument(document)
	changed := batchLocales(batch)
	for _, locale := range metadata.ChangedLocales {
		if !slices.Contains(changed, locale) {
			changed = append(changed, locale)
		}
	}
	return contentblock.Result{
		DocumentRevision: document.Revision, Changed: true, ContentChanged: contentChanged,
		MetadataChanged: metadata.Changed, TranslationSourceChanged: sourceChanged, ChangedLocales: changed,
	}, nil
}

// ApplyContentBatch implements legal.Store.
func (s *Store) ApplyContentBatch(ctx context.Context, batch contentblock.Batch, fence legal.ContentFence) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, nil)
}

// ApplyContentBatchWithMetadata implements legal.Store.
func (s *Store) ApplyContentBatchWithMetadata(ctx context.Context, batch contentblock.Batch, fence legal.ContentFence, effect legal.MetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyContentBatchWithMetadata"); err != nil {
		return contentblock.Result{}, err
	}
	return s.applyBatch(ctx, batch, fence, effect)
}

// ApplyTargetLocaleContentBatch implements legal.Store. A target-locale
// batch never advances the shared revision.
func (s *Store) ApplyTargetLocaleContentBatch(ctx context.Context, batch contentblock.Batch, locale string, fence legal.ContentFence, effect legal.TargetMetadataEffect) (contentblock.Result, error) {
	if err := s.fail("ApplyTargetLocaleContentBatch"); err != nil {
		return contentblock.Result{}, err
	}
	document, _, err := s.admit(ctx, batch.DocumentID, batch.ExpectedRevision, fence)
	if err != nil {
		return contentblock.Result{}, err
	}
	contentChanged := localeChanges(batch.LocaleGroups)
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
		document.apply(batch)
		s.putDocument(document)
	}
	return result, nil
}

// AdvanceContentRevision implements legal.Store.
func (s *Store) AdvanceContentRevision(ctx context.Context, input contentblock.AdvanceInput, fence legal.ContentFence, effect legal.MetadataEffect) (contentblock.AdvanceResult, error) {
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
	document.Revision = uuid.New()
	s.putDocument(document)
	return contentblock.AdvanceResult{
		DocumentRevision: document.Revision, Changed: true, TranslationSourceChanged: metadata.AffectsTranslationSource,
	}, nil
}

// DeleteContentDocument implements legal.Store.
func (s *Store) DeleteContentDocument(ctx context.Context, documentID uuid.UUID, fence legal.ContentFence) error {
	if err := s.fail("DeleteContentDocument"); err != nil {
		return err
	}
	if _, err := fence(ctx, s, documentID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.documents[documentID]; !ok {
		return contentblock.ErrDocumentNotFound
	}
	delete(s.documents, documentID)
	return nil
}

// SiteVariables implements legal.Store.
func (s *Store) SiteVariables(context.Context) (sitevariable.Context, error) {
	if err := s.fail("SiteVariables"); err != nil {
		return sitevariable.Context{}, err
	}
	return s.SiteSettings, nil
}
