package contentblock

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type uuidGenerator func() uuid.UUID

// Option configures deterministic Store test seams.
type Option func(*Store)

// Store owns common Content Block validation and persistence. Owning domain
// services retain authorization, lifecycle, metadata, and outer transaction
// authority.
type Store struct {
	contract Contract
	reuse    FileReuseAuthorizer
	repo     repository
	newUUID  uuidGenerator
	now      func() time.Time
}

// NewStore constructs the shared foundation. Both dependencies are mandatory;
// omitting File reuse authority would turn a File UUID into attach permission.
func NewStore(contract Contract, reuse FileReuseAuthorizer, options ...Option) (*Store, error) {
	if contract == nil {
		return nil, fmt.Errorf("content Block contract is required")
	}
	if reuse == nil {
		return nil, fmt.Errorf("content Block File reuse authorizer is required")
	}
	store := &Store{
		contract: contract,
		reuse:    reuse,
		newUUID:  uuid.New,
		now:      func() time.Time { return time.Now().UTC() },
	}
	for _, option := range options {
		option(store)
	}
	return store, nil
}

// CreateDocument creates one empty aggregate inside the caller-owned tx.
func (s *Store) CreateDocument(
	ctx context.Context,
	tx *gorm.DB,
	input CreateInput,
) (Snapshot, error) {
	if tx == nil {
		return Snapshot{}, fmt.Errorf("content Block transaction is required")
	}
	input.Profile = strings.TrimSpace(input.Profile)
	if input.Profile == "" {
		return Snapshot{}, fmt.Errorf("%w: document profile is required", ErrInvalidMutation)
	}
	if _, err := s.contract.Limits(input.Profile); err != nil {
		return Snapshot{}, fmt.Errorf("%w: validate document profile: %v", ErrInvalidMutation, err)
	}
	if err := validateLocale(input.SourceLocale); err != nil {
		return Snapshot{}, fmt.Errorf("%w: source locale: %v", ErrInvalidMutation, err)
	}
	if input.ID == uuid.Nil {
		input.ID = s.newUUID()
	}
	document := Document{
		ID:        input.ID,
		Profile:   input.Profile,
		Revision:  s.newUUID(),
		CreatedAt: s.now(),
		UpdatedAt: s.now(),
	}
	state := newAggregate(document)
	if err := s.repo.createDocument(ctx, tx, documentRow(document)); err != nil {
		return Snapshot{}, err
	}
	return s.snapshot(state, input.SourceLocale)
}

// LoadSnapshot loads one coherent aggregate. Authorization belongs to the
// owning API and must be completed before exposing this result. Owning APIs
// that also return root metadata must use LoadSnapshotInTransaction after
// locking the root row in the same transaction.
func (s *Store) LoadSnapshot(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
	sourceLocale string,
) (Snapshot, error) {
	if db == nil || documentID == uuid.Nil {
		return Snapshot{}, fmt.Errorf("%w: document database and UUID are required", ErrInvalidMutation)
	}
	if err := validateLocale(sourceLocale); err != nil {
		return Snapshot{}, fmt.Errorf("%w: source locale: %v", ErrInvalidMutation, err)
	}
	var snapshot Snapshot
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var loadErr error
		snapshot, loadErr = s.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
		return loadErr
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	return snapshot, err
}

// LoadSnapshotInTransaction loads an aggregate after taking a shared document
// lock. The owning API must lock its root row first to preserve the global lock
// order used by write fences.
func (s *Store) LoadSnapshotInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	sourceLocale string,
) (Snapshot, error) {
	if tx == nil || documentID == uuid.Nil {
		return Snapshot{}, fmt.Errorf("%w: document transaction and UUID are required", ErrInvalidMutation)
	}
	if err := validateLocale(sourceLocale); err != nil {
		return Snapshot{}, fmt.Errorf("%w: source locale: %v", ErrInvalidMutation, err)
	}
	state, err := s.repo.loadAggregate(ctx, tx, documentID, "SHARE")
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateAggregate(s.contract, &state, sourceLocale); err != nil {
		return Snapshot{}, err
	}
	return s.snapshot(state, sourceLocale)
}

// LoadPublicationAttachments returns exact active and missing selectors
// without loading or validating the document aggregate. The owning domain
// applies its File/derivative readiness policy to active File IDs and rejects
// any returned missing selector before publishing.
func (s *Store) LoadPublicationAttachments(
	ctx context.Context,
	db *gorm.DB,
	documentID uuid.UUID,
) ([]PublicationAttachment, error) {
	if db == nil || documentID == uuid.Nil {
		return nil, fmt.Errorf("%w: publication database and document UUID are required", ErrInvalidMutation)
	}
	return s.repo.loadPublicationAttachments(ctx, db, documentID)
}

// ApplyBatch validates and applies a typed full-Block mutation. Store does not
// start a transaction: caller must return any error from its gorm.Transaction
// callback so partial writes roll back.
func (s *Store) ApplyBatch(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	fence DomainFence,
) (Result, error) {
	if isPostgres(tx) && (isLocaleOnlyBatch(batch) || isReorderOnlyBatch(batch) || isSharedPresentationBatch(batch)) {
		if err := validateBatchEnvelope(batch, tx, fence, false); err != nil {
			return Result{}, err
		}
		domain, err := fence(ctx, tx, batch.DocumentID)
		if err != nil {
			return Result{}, err
		}
		if err := validateLocale(domain.SourceLocale); err != nil {
			return Result{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
		}
		if isReorderOnlyBatch(batch) {
			return s.applyReorderBatchPostgres(ctx, tx, batch, domain)
		}
		if isSharedPresentationBatch(batch) {
			result, applicable, applyErr := s.applySharedPresentationPostgres(ctx, tx, batch, domain)
			if applyErr != nil || applicable {
				return result, applyErr
			}
			return s.applyBatchAfterFence(ctx, tx, batch, domain, false, false)
		}
		return s.applyLocaleBatchPostgres(ctx, tx, batch, domain)
	}
	return s.applyBatch(ctx, tx, batch, fence, false, false)
}

// ApplyBatchWithMetadata applies one generated content mutation and one
// owning-domain metadata mutation under the same aggregate lock and revision.
// The caller owns the transaction; any callback or persistence error rolls
// both boundaries back. A semantic no-op preserves the current revision.
func (s *Store) ApplyBatchWithMetadata(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	fence DomainFence,
	metadata MetadataMutation,
) (Result, error) {
	if metadata == nil {
		return Result{}, fmt.Errorf("%w: metadata mutation is required", ErrInvalidMutation)
	}
	if err := validateBatchEnvelope(batch, tx, fence, true); err != nil {
		return Result{}, err
	}
	domain, err := fence(ctx, tx, batch.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err := validateLocale(domain.SourceLocale); err != nil {
		return Result{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
	}
	return s.applyBatchAfterFenceWithMetadata(ctx, tx, batch, domain, false, false, metadata)
}

// SwitchSourceLocale applies one source-locale pointer change under the same
// Content Document CAS as any explicit-empty overlays required by the new
// source. Existing locale values are never copied, swapped, or rewritten.
func (s *Store) SwitchSourceLocale(
	ctx context.Context,
	tx *gorm.DB,
	input SourceLocaleSwitchInput,
	fence DomainFence,
	metadata MetadataMutation,
) (Result, error) {
	if tx == nil || fence == nil || metadata == nil || input.DocumentID == uuid.Nil ||
		input.ExpectedRevision == uuid.Nil {
		return Result{}, fmt.Errorf("%w: invalid source locale switch", ErrInvalidMutation)
	}
	if err := validateLocale(input.RequestedLocale); err != nil {
		return Result{}, fmt.Errorf("%w: requested source locale: %v", ErrInvalidMutation, err)
	}
	if err := validateContributorMemberIDs(input.ContributorMemberIDs); err != nil {
		return Result{}, err
	}
	domain, err := fence(ctx, tx, input.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err := validateLocale(domain.SourceLocale); err != nil {
		return Result{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
	}

	snapshot, err := s.LoadSnapshotInTransaction(ctx, tx, input.DocumentID, domain.SourceLocale)
	if err != nil {
		return Result{}, err
	}
	sourceByBlock := localeOverlayByBlock(snapshot.LocaleOverlays, domain.SourceLocale)
	requestedByBlock := localeOverlayByBlock(snapshot.LocaleOverlays, input.RequestedLocale)
	updates := make([]LocaleBlockUpdate, 0, len(snapshot.Blocks)-len(requestedByBlock))
	if input.RequestedLocale != domain.SourceLocale {
		for _, block := range snapshot.Blocks {
			if _, exists := requestedByBlock[block.ID]; exists {
				continue
			}
			source, exists := sourceByBlock[block.ID]
			if !exists {
				return Result{}, fmt.Errorf("%w: Block %s has no source locale overlay", ErrInvalidMutation, block.ID)
			}
			empty, err := s.contract.BuildExplicitEmptyLocale(
				snapshot.Document.Profile,
				block.Kind,
				source.LocalizedData,
			)
			if err != nil {
				return Result{}, fmt.Errorf("%w: build explicit-empty locale for Block %s: %v", ErrInvalidMutation, block.ID, err)
			}
			updates = append(updates, LocaleBlockUpdate{
				BlockID: block.ID, ExpectedKind: block.Kind, LocalizedData: empty,
			})
		}
	}
	batch := Batch{
		DocumentID: input.DocumentID, ExpectedRevision: input.ExpectedRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), input.ContributorMemberIDs...),
	}
	if len(updates) != 0 {
		batch.LocaleGroups = []LocaleMutationGroup{{
			Locale: input.RequestedLocale, Upserts: updates,
		}}
	}
	return s.ApplyBatchWithMetadata(ctx, tx, batch, fence, func(ctx context.Context, tx *gorm.DB) (MetadataEffect, error) {
		effect, err := metadata(ctx, tx)
		if err != nil {
			return MetadataEffect{}, err
		}
		if input.RequestedLocale != domain.SourceLocale &&
			(!effect.Changed || !effect.AffectsTranslationSource || effect.SourceLocale != input.RequestedLocale) {
			return MetadataEffect{}, fmt.Errorf("%w: source locale metadata did not accept the requested locale", ErrInvalidMutation)
		}
		return effect, nil
	})
}

func localeOverlayByBlock(overlays []LocaleOverlay, locale string) map[uuid.UUID]LocaleBlockUpdate {
	for _, overlay := range overlays {
		if overlay.Locale != locale {
			continue
		}
		result := make(map[uuid.UUID]LocaleBlockUpdate, len(overlay.Blocks))
		for _, block := range overlay.Blocks {
			result[block.BlockID] = block
		}
		return result
	}
	return map[uuid.UUID]LocaleBlockUpdate{}
}

// ApplyTargetLocaleBatchWithMetadata applies one exact non-source locale and
// its owning metadata row without advancing the shared Content Document
// revision. The caller owns authorization, root locking, target revision CAS,
// and the transaction. Store owns aggregate validation and locale persistence.
func (s *Store) ApplyTargetLocaleBatchWithMetadata(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	targetLocale string,
	fence DomainFence,
	metadata TargetLocaleMetadataMutation,
) (Result, error) {
	if metadata == nil {
		return Result{}, fmt.Errorf("%w: target locale metadata mutation is required", ErrInvalidMutation)
	}
	if err := validateBatchEnvelope(batch, tx, fence, true); err != nil {
		return Result{}, err
	}
	if err := validateLocale(targetLocale); err != nil {
		return Result{}, fmt.Errorf("%w: target locale: %v", ErrInvalidMutation, err)
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return Result{}, fmt.Errorf("%w: target locale cannot mutate the shared Block graph", ErrInvalidMutation)
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != targetLocale {
			return Result{}, fmt.Errorf("%w: target locale batch contains another locale", ErrInvalidMutation)
		}
	}

	domain, err := fence(ctx, tx, batch.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err := validateLocale(domain.SourceLocale); err != nil {
		return Result{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
	}
	if targetLocale == domain.SourceLocale {
		return Result{}, fmt.Errorf("%w: target locale must differ from source locale", ErrInvalidMutation)
	}
	before, err := s.repo.loadAggregate(ctx, tx, batch.DocumentID, "UPDATE")
	if err != nil {
		return Result{}, err
	}
	if err := validateAggregate(s.contract, &before, domain.SourceLocale); err != nil {
		return Result{}, err
	}
	if before.document.Revision != batch.ExpectedRevision {
		return Result{}, staleRevision(before.document.Revision)
	}
	after, mutation, err := s.apply(ctx, tx, before, batch, domain.SourceLocale, false, false)
	if err != nil {
		return Result{}, err
	}
	beforeDigest, err := snapshotDigest(before)
	if err != nil {
		return Result{}, err
	}
	afterDigest, err := snapshotDigest(after)
	if err != nil {
		return Result{}, err
	}
	contentChanged := beforeDigest != afterDigest
	if contentChanged {
		sourceChanged, deriveErr := deriveTranslationSourceChanged(
			s.contract, before, after, mutation, domain.SourceLocale,
		)
		if deriveErr != nil {
			return Result{}, deriveErr
		}
		if sourceChanged {
			return Result{}, fmt.Errorf("%w: target locale changed the source-owned Block view", ErrInvalidMutation)
		}
		if err := s.validateFileChanges(ctx, tx, before, after, mutation); err != nil {
			return Result{}, err
		}
	}
	metadataEffect, err := metadata(ctx, tx, contentChanged)
	if err != nil {
		return Result{}, err
	}
	if metadataEffect.AffectsTranslationSource || metadataEffect.SourceLocale != "" {
		return Result{}, fmt.Errorf("%w: target locale metadata cannot affect source authority", ErrInvalidMutation)
	}
	for _, locale := range metadataEffect.ChangedLocales {
		if locale != targetLocale {
			return Result{}, fmt.Errorf("%w: target metadata reported another locale", ErrInvalidMutation)
		}
	}
	if !contentChanged && !metadataEffect.Changed {
		return Result{DocumentRevision: before.document.Revision}, nil
	}
	if contentChanged {
		if err := s.repo.persistTargetLocale(ctx, tx, after, mutation); err != nil {
			return Result{}, err
		}
	}
	return Result{
		DocumentRevision: before.document.Revision,
		Changed:          true,
		ContentChanged:   contentChanged,
		MetadataChanged:  metadataEffect.Changed,
		ChangedLocales:   []string{targetLocale},
	}, nil
}

func (s *Store) applyBatch(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	fence DomainFence,
	allowMissingFiles bool,
	replaceAll bool,
) (Result, error) {
	if err := validateBatchEnvelope(batch, tx, fence, replaceAll); err != nil {
		return Result{}, err
	}
	domain, err := fence(ctx, tx, batch.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err := validateLocale(domain.SourceLocale); err != nil {
		return Result{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
	}
	return s.applyBatchAfterFence(ctx, tx, batch, domain, allowMissingFiles, replaceAll)
}

func (s *Store) applyBatchAfterFence(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
	allowMissingFiles bool,
	replaceAll bool,
) (Result, error) {
	return s.applyBatchAfterFenceWithMetadata(ctx, tx, batch, domain, allowMissingFiles, replaceAll, nil)
}

func (s *Store) applyBatchAfterFenceWithMetadata(
	ctx context.Context,
	tx *gorm.DB,
	batch Batch,
	domain DomainContext,
	allowMissingFiles bool,
	replaceAll bool,
	metadata MetadataMutation,
) (Result, error) {
	before, err := s.repo.loadAggregate(ctx, tx, batch.DocumentID, "UPDATE")
	if err != nil {
		return Result{}, err
	}
	if err := validateAggregate(s.contract, &before, domain.SourceLocale); err != nil {
		return Result{}, err
	}
	if before.document.Revision != batch.ExpectedRevision {
		return Result{}, staleRevision(before.document.Revision)
	}
	if replaceAll {
		desiredIDs := make(map[uuid.UUID]struct{}, len(batch.Upserts))
		for _, block := range batch.Upserts {
			desiredIDs[block.ID] = struct{}{}
		}
		for blockID := range before.blocks {
			if _, retained := desiredIDs[blockID]; !retained {
				batch.Deletes = append(batch.Deletes, blockID)
			}
		}
	}

	after, mutation, err := s.apply(
		ctx,
		tx,
		before,
		batch,
		domain.SourceLocale,
		allowMissingFiles,
		replaceAll,
	)
	if err != nil {
		return Result{}, err
	}
	beforeDigest, err := snapshotDigest(before)
	if err != nil {
		return Result{}, err
	}
	afterDigest, err := snapshotDigest(after)
	if err != nil {
		return Result{}, err
	}
	contentChanged := beforeDigest != afterDigest
	sourceChanged := false
	localesChanged := []string(nil)
	if contentChanged {
		sourceChanged, err = deriveTranslationSourceChanged(s.contract, before, after, mutation, domain.SourceLocale)
		if err != nil {
			return Result{}, err
		}
		localesChanged = changedMutationLocales(before, after, mutation)
		if err := s.validateFileChanges(ctx, tx, before, after, mutation); err != nil {
			return Result{}, err
		}
	}
	metadataEffect := MetadataEffect{}
	if metadata != nil {
		metadataEffect, err = metadata(ctx, tx)
		if err != nil {
			return Result{}, err
		}
		if metadataEffect.AffectsTranslationSource && !metadataEffect.Changed {
			return Result{}, fmt.Errorf("%w: unchanged metadata cannot affect translation source", ErrInvalidMutation)
		}
		if metadataEffect.SourceLocale != "" {
			if err := validateLocale(metadataEffect.SourceLocale); err != nil {
				return Result{}, fmt.Errorf("%w: metadata source locale: %v", ErrInvalidMutation, err)
			}
			if metadataEffect.SourceLocale != domain.SourceLocale && !metadataEffect.AffectsTranslationSource {
				return Result{}, fmt.Errorf("%w: source locale change must affect translation source", ErrInvalidMutation)
			}
		}
		for _, locale := range metadataEffect.ChangedLocales {
			if err := validateLocale(locale); err != nil {
				return Result{}, fmt.Errorf("%w: metadata changed locale: %v", ErrInvalidMutation, err)
			}
		}
		localesChanged = mergeChangedLocales(localesChanged, metadataEffect.ChangedLocales)
		sourceChanged = sourceChanged || metadataEffect.AffectsTranslationSource
	}
	effectiveSourceLocale := domain.SourceLocale
	if metadataEffect.SourceLocale != "" {
		effectiveSourceLocale = metadataEffect.SourceLocale
	}
	if effectiveSourceLocale != domain.SourceLocale {
		if err := validateAggregate(s.contract, &after, effectiveSourceLocale); err != nil {
			return Result{}, err
		}
	}
	if !contentChanged && !metadataEffect.Changed {
		return Result{DocumentRevision: before.document.Revision}, nil
	}

	after.document.Revision = s.newUUID()
	after.document.UpdatedAt = s.now()
	if err := s.repo.persist(ctx, tx, before, after, mutation); err != nil {
		return Result{}, err
	}
	return Result{
		DocumentRevision:         after.document.Revision,
		Changed:                  true,
		ContentChanged:           contentChanged,
		MetadataChanged:          metadataEffect.Changed,
		TranslationSourceChanged: sourceChanged,
		ChangedLocales:           localesChanged,
	}, nil
}

func mergeChangedLocales(left, right []string) []string {
	set := make(map[string]struct{}, len(left)+len(right))
	for _, locale := range left {
		set[locale] = struct{}{}
	}
	for _, locale := range right {
		set[locale] = struct{}{}
	}
	result := make([]string, 0, len(set))
	for locale := range set {
		result = append(result, locale)
	}
	sort.Strings(result)
	return result
}

func deriveTranslationSourceChanged(
	contract Contract,
	before, after aggregate,
	mutation persistedMutation,
	sourceLocale string,
) (bool, error) {
	if !mutationCanAffectTranslationSource(mutation, sourceLocale) {
		return false, nil
	}
	changed, err := contract.TranslationSourceChanged(
		before.document.Profile,
		before.localizedBlocks(sourceLocale),
		after.localizedBlocks(sourceLocale),
	)
	if err != nil {
		return false, fmt.Errorf("%w: derive translation source change: %v", ErrInvalidMutation, err)
	}
	return changed, nil
}

func mutationCanAffectTranslationSource(mutation persistedMutation, sourceLocale string) bool {
	if len(mutation.upsertOrder) > 0 || len(mutation.deleteOrder) > 0 || len(mutation.reorders) > 0 {
		return true
	}
	for _, changed := range mutation.localeMutations {
		if changed.locale == sourceLocale {
			return true
		}
	}
	return false
}

// ReplaceSnapshot restores one typed base/source snapshot. Target overlays on
// retained, kind-compatible Blocks remain stored for the translation owner to
// stale; removed Blocks cascade their overlays. Missing attachments are
// accepted only through this method and have no active File relation.
func (s *Store) ReplaceSnapshot(
	ctx context.Context,
	tx *gorm.DB,
	input ReplaceInput,
	fence DomainFence,
) (Result, error) {
	groups := make([]LocaleMutationGroup, 0, len(input.LocaleOverlays))
	for _, overlay := range input.LocaleOverlays {
		groups = append(groups, LocaleMutationGroup{
			Locale:  overlay.Locale,
			Upserts: append([]LocaleBlockUpdate(nil), overlay.Blocks...),
		})
	}
	return s.applyBatch(ctx, tx, Batch{
		DocumentID:       input.DocumentID,
		ExpectedRevision: input.ExpectedRevision,
		Upserts:          input.Blocks,
		LocaleGroups:     groups,
	}, fence, true, true)
}

// DeleteLocale removes every Block overlay row for one non-source locale and
// the owning domain's locale metadata under the same document CAS. A missing
// locale is a semantic no-op when the metadata callback also reports no
// change. Store does not own the transaction; callers must return any error so
// both persistence boundaries roll back together.
func (s *Store) DeleteLocale(
	ctx context.Context,
	tx *gorm.DB,
	input DeleteLocaleInput,
	fence DomainFence,
	deleteMetadata LocaleMetadataDeletion,
) (Result, error) {
	if tx == nil || fence == nil || deleteMetadata == nil {
		return Result{}, fmt.Errorf("%w: transaction, domain fence, and locale metadata deletion are required", ErrInvalidMutation)
	}
	if input.DocumentID == uuid.Nil || input.ExpectedRevision == uuid.Nil {
		return Result{}, fmt.Errorf("%w: document and expected revision must be UUIDs", ErrInvalidMutation)
	}
	if err := validateLocale(input.Locale); err != nil {
		return Result{}, fmt.Errorf("%w: locale: %v", ErrInvalidMutation, err)
	}
	if err := validateContributorMemberIDs(input.ContributorMemberIDs); err != nil {
		return Result{}, err
	}

	domain, err := fence(ctx, tx, input.DocumentID)
	if err != nil {
		return Result{}, err
	}
	if err := validateLocale(domain.SourceLocale); err != nil {
		return Result{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
	}
	document, err := s.repo.loadDocument(ctx, tx, input.DocumentID, "UPDATE")
	if err != nil {
		return Result{}, err
	}
	if document.Revision != input.ExpectedRevision {
		return Result{}, staleRevision(document.Revision)
	}
	if input.Locale == domain.SourceLocale {
		return Result{}, fmt.Errorf("%w: source locale cannot be deleted", ErrInvalidMutation)
	}

	metadataChanged, err := deleteMetadata(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	deletedOverlayRows, err := s.repo.deleteDocumentLocale(ctx, tx, input.DocumentID, input.Locale)
	if err != nil {
		return Result{}, err
	}
	if deletedOverlayRows == 0 && !metadataChanged {
		return Result{
			DocumentRevision: document.Revision,
		}, nil
	}

	after := document
	after.Revision = s.newUUID()
	after.UpdatedAt = s.now()
	if err := s.repo.updateDocument(ctx, tx, document, after); err != nil {
		return Result{}, err
	}
	return Result{
		DocumentRevision: after.Revision,
		Changed:          true,
		ContentChanged:   deletedOverlayRows > 0,
		MetadataChanged:  metadataChanged,
		ChangedLocales:   []string{input.Locale},
	}, nil
}

// AdvanceRevision attaches an owning-domain metadata mutation to the same
// document/source revision stream. Store executes the callback only after the
// root fence and CAS succeed; the callback derives the semantic impact.
func (s *Store) AdvanceRevision(
	ctx context.Context,
	tx *gorm.DB,
	input AdvanceInput,
	fence DomainFence,
	mutation MetadataMutation,
) (AdvanceResult, error) {
	if tx == nil || fence == nil || mutation == nil || input.DocumentID == uuid.Nil || input.ExpectedRevision == uuid.Nil {
		return AdvanceResult{}, fmt.Errorf("%w: invalid revision advance", ErrInvalidMutation)
	}
	domain, err := fence(ctx, tx, input.DocumentID)
	if err != nil {
		return AdvanceResult{}, err
	}
	if err := validateLocale(domain.SourceLocale); err != nil {
		return AdvanceResult{}, fmt.Errorf("%w: domain source locale: %v", ErrInvalidMutation, err)
	}
	document, err := s.repo.loadDocument(ctx, tx, input.DocumentID, "UPDATE")
	if err != nil {
		return AdvanceResult{}, err
	}
	if document.Revision != input.ExpectedRevision {
		return AdvanceResult{}, staleRevision(document.Revision)
	}
	effect, err := mutation(ctx, tx)
	if err != nil {
		return AdvanceResult{}, err
	}
	if effect.AffectsTranslationSource && !effect.Changed {
		return AdvanceResult{}, fmt.Errorf("%w: unchanged metadata cannot affect translation source", ErrInvalidMutation)
	}
	if effect.SourceLocale != "" {
		if err := validateLocale(effect.SourceLocale); err != nil {
			return AdvanceResult{}, fmt.Errorf("%w: result source locale: %v", ErrInvalidMutation, err)
		}
		if effect.SourceLocale != domain.SourceLocale && !effect.AffectsTranslationSource {
			return AdvanceResult{}, fmt.Errorf("%w: source locale change must affect translation source", ErrInvalidMutation)
		}
	}
	if !effect.Changed {
		return AdvanceResult{
			DocumentRevision: document.Revision,
		}, nil
	}
	after := document
	after.Revision = s.newUUID()
	after.UpdatedAt = s.now()
	if err := s.repo.updateDocument(ctx, tx, document, after); err != nil {
		return AdvanceResult{}, err
	}
	return AdvanceResult{
		DocumentRevision:         after.Revision,
		Changed:                  true,
		TranslationSourceChanged: effect.AffectsTranslationSource,
	}, nil
}

// DeleteDocument removes one aggregate after the owning root fence succeeds.
func (s *Store) DeleteDocument(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	fence DomainFence,
) error {
	if tx == nil || fence == nil || documentID == uuid.Nil {
		return fmt.Errorf("%w: invalid document delete", ErrInvalidMutation)
	}
	if _, err := fence(ctx, tx, documentID); err != nil {
		return err
	}
	if _, err := s.repo.loadDocument(ctx, tx, documentID, "UPDATE"); err != nil {
		return err
	}
	return s.repo.deleteDocument(ctx, tx, documentID)
}

// AdvanceInput supplies the CAS tokens for an owning-domain metadata callback.
type AdvanceInput struct {
	DocumentID       uuid.UUID
	ExpectedRevision uuid.UUID
}

type localeMutation struct {
	locale  string
	blockID uuid.UUID
	delete  bool
}

type persistedMutation struct {
	sourceLocale      string
	upsertOrder       []uuid.UUID
	localeMutations   []localeMutation
	deleteOrder       []uuid.UUID
	reorders          []Reorder
	kindChanged       map[uuid.UUID]bool
	allowMissingFiles bool
}

func validateBatchEnvelope(batch Batch, tx *gorm.DB, fence DomainFence, allowEmpty bool) error {
	if tx == nil || fence == nil {
		return fmt.Errorf("%w: transaction and domain fence are required", ErrInvalidMutation)
	}
	if batch.DocumentID == uuid.Nil || batch.ExpectedRevision == uuid.Nil {
		return fmt.Errorf("%w: document and expected revision must be UUIDs", ErrInvalidMutation)
	}
	if !allowEmpty && len(batch.Upserts) == 0 && len(batch.LocaleGroups) == 0 && len(batch.Deletes) == 0 && len(batch.Reorders) == 0 {
		return fmt.Errorf("%w: empty mutation batch", ErrInvalidMutation)
	}
	return validateContributorMemberIDs(batch.ContributorMemberIDs)
}

func validateContributorMemberIDs(memberIDs []uuid.UUID) error {
	for index, memberID := range memberIDs {
		if memberID == uuid.Nil {
			return fmt.Errorf("%w: contributor Member ID must be a UUID", ErrInvalidMutation)
		}
		if index > 0 && memberIDs[index-1].String() >= memberID.String() {
			return fmt.Errorf("%w: contributor Member IDs must be sorted and unique", ErrInvalidMutation)
		}
	}
	return nil
}

func (s *Store) snapshot(state aggregate, sourceLocale string) (Snapshot, error) {
	digest, err := snapshotDigest(state)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := state.snapshot(sourceLocale)
	snapshot.SnapshotDigest = digest
	return snapshot, nil
}

func (s *Store) apply(
	ctx context.Context,
	tx *gorm.DB,
	before aggregate,
	batch Batch,
	sourceLocale string,
	allowMissingFiles bool,
	replaceAll bool,
) (aggregate, persistedMutation, error) {
	if err := validateOperationIDs(batch); err != nil {
		return aggregate{}, persistedMutation{}, err
	}
	if err := s.rejectCrossDocumentIDs(ctx, tx, before.document.ID, batch); err != nil {
		return aggregate{}, persistedMutation{}, err
	}

	after := before.clone()
	mutation := persistedMutation{
		sourceLocale:      sourceLocale,
		reorders:          append([]Reorder(nil), batch.Reorders...),
		kindChanged:       make(map[uuid.UUID]bool),
		allowMissingFiles: allowMissingFiles,
	}
	localeInputs := make(map[string]map[uuid.UUID]json.RawMessage, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		updates := make(map[uuid.UUID]json.RawMessage, len(group.Upserts))
		for _, update := range group.Upserts {
			updates[update.BlockID] = update.LocalizedData
		}
		localeInputs[group.Locale] = updates
	}
	normalizedUpserts := make(map[uuid.UUID]FullBlock, len(batch.Upserts))
	for _, input := range batch.Upserts {
		localized, submitted := localeInputs[sourceLocale][input.ID]
		if !submitted && !replaceAll {
			localized, submitted = before.locales[input.ID][sourceLocale]
		}
		if !submitted {
			return aggregate{}, persistedMutation{}, fmt.Errorf(
				"%w: base Block %s requires a source locale overlay",
				ErrInvalidMutation,
				input.ID,
			)
		}
		normalized, err := normalizeBlock(s.contract, before.document.Profile, FullBlock{
			BaseBlock:     input,
			LocalizedData: localized,
		})
		if err != nil {
			return aggregate{}, persistedMutation{}, err
		}
		if !allowMissingFiles {
			for _, reference := range normalized.FileReferences {
				if reference.Missing {
					return aggregate{}, persistedMutation{}, fmt.Errorf("%w: missing attachment is restore-only", ErrFileReference)
				}
			}
		}
		if current, exists := before.blocks[normalized.ID]; exists && current.Kind != normalized.Kind {
			mutation.kindChanged[normalized.ID] = true
		}
		normalizedUpserts[normalized.ID] = normalized
	}

	retainedByOperation := make(map[uuid.UUID]struct{}, len(batch.Upserts)+len(batch.Reorders))
	for id := range normalizedUpserts {
		retainedByOperation[id] = struct{}{}
	}
	for _, reorder := range batch.Reorders {
		retainedByOperation[reorder.BlockID] = struct{}{}
	}
	deleteSet := expandedDeletes(before.blocks, batch.Deletes, retainedByOperation)
	for id := range deleteSet {
		delete(after.blocks, id)
		delete(after.locales, id)
	}

	for id, block := range normalizedUpserts {
		after.blocks[id] = cloneBlock(block)
		if mutation.kindChanged[id] {
			after.locales[id] = make(map[string]json.RawMessage)
		}
	}
	for _, reorder := range batch.Reorders {
		block, exists := after.blocks[reorder.BlockID]
		if !exists {
			return aggregate{}, persistedMutation{}, fmt.Errorf("%w: reorder target does not exist", ErrInvalidMutation)
		}
		block.ParentID = reorder.ParentID
		block.ContainerSlot = strings.TrimSpace(reorder.ContainerSlot)
		block.Position = reorder.Position
		after.blocks[reorder.BlockID] = block
	}
	for _, group := range batch.LocaleGroups {
		for _, blockID := range group.Deletes {
			if group.Locale == sourceLocale {
				return aggregate{}, persistedMutation{}, fmt.Errorf("%w: source locale overlays cannot be deleted", ErrInvalidMutation)
			}
			if after.locales[blockID] != nil {
				delete(after.locales[blockID], group.Locale)
			}
			mutation.localeMutations = append(mutation.localeMutations, localeMutation{
				locale: group.Locale, blockID: blockID, delete: true,
			})
		}
		for _, update := range group.Upserts {
			base, exists := after.blocks[update.BlockID]
			if !exists {
				return aggregate{}, persistedMutation{}, fmt.Errorf("%w: locale update Block %s does not exist", ErrInvalidMutation, update.BlockID)
			}
			candidate := cloneBlock(base)
			candidate.LocalizedData = update.LocalizedData
			normalized, err := normalizeBlock(s.contract, before.document.Profile, candidate)
			if err != nil {
				return aggregate{}, persistedMutation{}, err
			}
			if !sameBlockShared(base, normalized) {
				return aggregate{}, persistedMutation{}, fmt.Errorf("%w: locale validation changed shared Block %s", ErrInvalidMutation, update.BlockID)
			}
			if after.locales[update.BlockID] == nil {
				after.locales[update.BlockID] = make(map[string]json.RawMessage)
			}
			after.locales[update.BlockID][group.Locale] = append(json.RawMessage(nil), normalized.LocalizedData...)
			mutation.localeMutations = append(mutation.localeMutations, localeMutation{
				locale: group.Locale, blockID: update.BlockID,
			})
		}
	}
	limits, err := validateAggregateEnvelope(s.contract, &after, sourceLocale)
	if err != nil {
		return aggregate{}, persistedMutation{}, err
	}
	for id := range normalizedUpserts {
		if err := validateAggregateBlockPayload(s.contract, &after, sourceLocale, id); err != nil {
			return aggregate{}, persistedMutation{}, err
		}
	}
	if len(normalizedUpserts) > 0 || len(deleteSet) > 0 || len(batch.Reorders) > 0 {
		if err := validateAggregateStructure(s.contract, &after, limits); err != nil {
			return aggregate{}, persistedMutation{}, err
		}
	}

	mutation.upsertOrder = sortUpsertsByDepth(after.blocks, normalizedUpserts)
	sort.Slice(mutation.localeMutations, func(i, j int) bool {
		if mutation.localeMutations[i].locale != mutation.localeMutations[j].locale {
			return mutation.localeMutations[i].locale < mutation.localeMutations[j].locale
		}
		return mutation.localeMutations[i].blockID.String() < mutation.localeMutations[j].blockID.String()
	})
	mutation.deleteOrder = sortDeletesByDepth(before.blocks, deleteSet)
	return after, mutation, nil
}

func validateOperationIDs(batch Batch) error {
	baseOperations := make(map[uuid.UUID]string, len(batch.Upserts)+len(batch.Deletes)+len(batch.Reorders))
	for _, block := range batch.Upserts {
		if block.ID == uuid.Nil {
			return fmt.Errorf("%w: base upsert Block ID must be a UUID", ErrInvalidMutation)
		}
		if previous := baseOperations[block.ID]; previous != "" {
			return fmt.Errorf("%w: Block %s appears in %s and upsert", ErrInvalidMutation, block.ID, previous)
		}
		baseOperations[block.ID] = "upsert"
	}
	for _, blockID := range batch.Deletes {
		if blockID == uuid.Nil {
			return fmt.Errorf("%w: delete Block ID must be a UUID", ErrInvalidMutation)
		}
		if previous := baseOperations[blockID]; previous != "" {
			return fmt.Errorf("%w: Block %s appears in %s and delete", ErrInvalidMutation, blockID, previous)
		}
		baseOperations[blockID] = "delete"
	}
	for _, reorder := range batch.Reorders {
		if reorder.BlockID == uuid.Nil || reorder.Position < 0 || strings.TrimSpace(reorder.ContainerSlot) == "" {
			return fmt.Errorf("%w: invalid Block reorder", ErrInvalidMutation)
		}
		if previous := baseOperations[reorder.BlockID]; previous != "" {
			return fmt.Errorf("%w: Block %s appears in %s and reorder", ErrInvalidMutation, reorder.BlockID, previous)
		}
		baseOperations[reorder.BlockID] = "reorder"
	}
	seenLocales := make(map[string]struct{}, len(batch.LocaleGroups))
	for _, group := range batch.LocaleGroups {
		if err := validateLocale(group.Locale); err != nil {
			return err
		}
		if _, exists := seenLocales[group.Locale]; exists {
			return fmt.Errorf("%w: duplicate locale mutation group %q", ErrInvalidMutation, group.Locale)
		}
		seenLocales[group.Locale] = struct{}{}
		if len(group.Upserts) == 0 && len(group.Deletes) == 0 {
			return fmt.Errorf("%w: empty locale mutation group %q", ErrInvalidMutation, group.Locale)
		}
		seenBlocks := make(map[uuid.UUID]string, len(group.Upserts)+len(group.Deletes))
		for _, update := range group.Upserts {
			if update.BlockID == uuid.Nil {
				return fmt.Errorf("%w: locale update Block ID must be a UUID", ErrInvalidMutation)
			}
			if baseOperations[update.BlockID] == "delete" {
				return fmt.Errorf("%w: deleted Block %s also has a locale update", ErrInvalidMutation, update.BlockID)
			}
			if previous := seenBlocks[update.BlockID]; previous != "" {
				return fmt.Errorf("%w: Block %s appears twice in locale %s", ErrInvalidMutation, update.BlockID, group.Locale)
			}
			seenBlocks[update.BlockID] = "upsert"
		}
		for _, blockID := range group.Deletes {
			if blockID == uuid.Nil {
				return fmt.Errorf("%w: locale delete Block ID must be a UUID", ErrInvalidMutation)
			}
			if baseOperations[blockID] != "" {
				return fmt.Errorf("%w: Block %s has conflicting base and locale delete operations", ErrInvalidMutation, blockID)
			}
			if previous := seenBlocks[blockID]; previous != "" {
				return fmt.Errorf("%w: Block %s appears twice in locale %s", ErrInvalidMutation, blockID, group.Locale)
			}
			seenBlocks[blockID] = "delete"
		}
	}
	return nil
}

func (s *Store) rejectCrossDocumentIDs(
	ctx context.Context,
	tx *gorm.DB,
	documentID uuid.UUID,
	batch Batch,
) error {
	ids := make([]uuid.UUID, 0, len(batch.Upserts)*2+len(batch.Deletes)+len(batch.Reorders)*2)
	for _, block := range batch.Upserts {
		ids = append(ids, block.ID)
		if block.ParentID != nil {
			ids = append(ids, *block.ParentID)
		}
	}
	for _, group := range batch.LocaleGroups {
		for _, update := range group.Upserts {
			ids = append(ids, update.BlockID)
		}
		ids = append(ids, group.Deletes...)
	}
	ids = append(ids, batch.Deletes...)
	for _, reorder := range batch.Reorders {
		ids = append(ids, reorder.BlockID)
		if reorder.ParentID != nil {
			ids = append(ids, *reorder.ParentID)
		}
	}
	documents, err := s.repo.blockDocuments(ctx, tx, ids)
	if err != nil {
		return err
	}
	for id, owner := range documents {
		if owner != documentID {
			return fmt.Errorf("%w: Block %s", ErrCrossDocument, id)
		}
	}
	return nil
}

func expandedDeletes(
	blocks map[uuid.UUID]FullBlock,
	explicit []uuid.UUID,
	retained map[uuid.UUID]struct{},
) map[uuid.UUID]struct{} {
	deleted := make(map[uuid.UUID]struct{}, len(explicit))
	for _, id := range explicit {
		deleted[id] = struct{}{}
	}
	changed := true
	for changed {
		changed = false
		for id, block := range blocks {
			if _, keep := retained[id]; keep || block.ParentID == nil {
				continue
			}
			if _, parentDeleted := deleted[*block.ParentID]; parentDeleted {
				if _, already := deleted[id]; !already {
					deleted[id] = struct{}{}
					changed = true
				}
			}
		}
	}
	return deleted
}

func sortUpsertsByDepth(all map[uuid.UUID]FullBlock, upserts map[uuid.UUID]FullBlock) []uuid.UUID {
	depths := blockDepths(all)
	result := make([]uuid.UUID, 0, len(upserts))
	for id := range upserts {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool {
		if depths[result[i]] != depths[result[j]] {
			return depths[result[i]] < depths[result[j]]
		}
		return result[i].String() < result[j].String()
	})
	return result
}

func sortDeletesByDepth(all map[uuid.UUID]FullBlock, deleted map[uuid.UUID]struct{}) []uuid.UUID {
	depths := blockDepths(all)
	result := make([]uuid.UUID, 0, len(deleted))
	for id := range deleted {
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool {
		if depths[result[i]] != depths[result[j]] {
			return depths[result[i]] > depths[result[j]]
		}
		return result[i].String() < result[j].String()
	})
	return result
}

func blockDepths(blocks map[uuid.UUID]FullBlock) map[uuid.UUID]int {
	depths := make(map[uuid.UUID]int, len(blocks))
	var visit func(uuid.UUID) int
	visit = func(id uuid.UUID) int {
		if depth := depths[id]; depth > 0 {
			return depth
		}
		block := blocks[id]
		depth := 1
		if block.ParentID != nil {
			depth = visit(*block.ParentID) + 1
		}
		depths[id] = depth
		return depth
	}
	for id := range blocks {
		visit(id)
	}
	return depths
}

func (s *Store) validateFileChanges(
	ctx context.Context,
	tx *gorm.DB,
	before, after aggregate,
	mutation persistedMutation,
) error {
	changedBlocks := make(map[uuid.UUID]struct{}, len(mutation.upsertOrder)+len(mutation.deleteOrder))
	for _, id := range mutation.upsertOrder {
		changedBlocks[id] = struct{}{}
	}
	for _, id := range mutation.deleteOrder {
		changedBlocks[id] = struct{}{}
	}
	fileIDs := make([]uuid.UUID, 0)
	for blockID := range changedBlocks {
		for _, reference := range before.blocks[blockID].FileReferences {
			if !reference.Missing {
				fileIDs = append(fileIDs, reference.FileID)
			}
		}
		for _, reference := range after.blocks[blockID].FileReferences {
			if !reference.Missing {
				fileIDs = append(fileIDs, reference.FileID)
			}
		}
	}
	files, err := s.repo.lockFiles(ctx, tx, fileIDs)
	if err != nil {
		return err
	}
	for blockID := range changedBlocks {
		beforeReferences := referenceMap(before.blocks[blockID].FileReferences)
		block, exists := after.blocks[blockID]
		if !exists {
			continue
		}
		for _, reference := range block.FileReferences {
			if reference.Missing {
				if !mutation.allowMissingFiles {
					return fmt.Errorf("%w: missing attachment is restore-only", ErrFileReference)
				}
				continue
			}
			if previous, unchanged := beforeReferences[reference.ReferencePath]; unchanged &&
				!previous.Missing && previous.FileID == reference.FileID {
				continue
			}
			file, exists := files[reference.FileID]
			if !exists {
				return fmt.Errorf("%w: File %s does not exist", ErrFileReference, reference.FileID)
			}
			if file.DeleteRequestedAt != nil {
				return fmt.Errorf("%w: File %s is pending deletion", ErrFileReference, reference.FileID)
			}
			if !validateMIME(reference, file.MIMEType) {
				return fmt.Errorf("%w: File %s MIME %q is not allowed", ErrFileReference, reference.FileID, file.MIMEType)
			}
			if err := s.reuse.AuthorizeFileReuse(ctx, tx, after.document, block, reference, file); err != nil {
				return fmt.Errorf("%w: authorize File %s reuse: %v", ErrFileReference, reference.FileID, err)
			}
		}
	}
	return nil
}

func referenceMap(references []FileReference) map[string]FileReference {
	result := make(map[string]FileReference, len(references))
	for _, reference := range references {
		result[reference.ReferencePath] = reference
	}
	return result
}
