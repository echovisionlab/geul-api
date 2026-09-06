package artist

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type artistLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type artistTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata artistLocaleMetadataRow
	TargetMetadata *artistLocaleMetadataRow
	TargetRevision string
}

func normalizeArtistDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func requireArtistSourceRoomLocale(
	ctx context.Context,
	db *gorm.DB,
	artistID string,
	locale string,
) (string, error) {
	normalized, err := normalizeArtistDocumentLocale(locale)
	if err != nil {
		return "", err
	}
	source, err := loadCreativeDocumentAuthority(ctx, db, artistContentEntity, artistID)
	if err != nil {
		return "", err
	}
	if normalized != source.SourceLocale {
		return "", errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
			"Artist document metadata and audit checkpoints are source-locale only",
		)
	}
	return normalized, nil
}

func artistSourceRoomFence(artistID string, locale string) contentblock.DomainFence {
	base := internalCreativeContentFence(artistContentEntity, artistID)
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_LOCALE_OWNERSHIP_CHANGED,
				"Artist source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func loadOptionalArtistLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	artistID string,
	locale string,
	forUpdate bool,
) (artistLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("artist_translation").
		Select("locale", "title", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", artistID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row artistLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return artistLocaleMetadataRow{}, false, nil
		}
		return artistLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func deriveArtistTargetRevision(documentRevision string, metadata artistLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func artistSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale && len(overlay.Blocks) != 0 {
			return true
		}
	}
	return false
}

func loadArtistTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (artistTargetLocaleState, error) {
	locale, err := normalizeArtistDocumentLocale(locale)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	lock := "SHARE"
	if forUpdate {
		lock = "UPDATE"
	}
	lockedDocumentID, err := loadCreativeContentDocumentIDWithStrength(
		ctx, tx, artistContentEntity, artistID, lock,
	)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return artistTargetLocaleState{}, errs.FailedPrecondition("Artist content document changed; reload before saving")
	}
	source, err := loadCreativeDocumentAuthorityWithStrength(
		ctx, tx, artistContentEntity, artistID, lock,
	)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return artistTargetLocaleState{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	if snapshot.SourceLocale != source.SourceLocale || snapshot.Document.Profile != creativeContentProfile {
		return artistTargetLocaleState{}, errs.FailedPrecondition("Artist content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := loadOptionalArtistLocaleMetadataRow(
		ctx, tx, artistID, source.SourceLocale, forUpdate,
	)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	if !sourceExists {
		return artistTargetLocaleState{}, errs.FailedPrecondition("Artist source locale metadata is missing")
	}
	state := artistTargetLocaleState{
		Snapshot: snapshot, SourceLocale: source.SourceLocale, SourceMetadata: sourceMetadata,
	}
	if locale == source.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := loadOptionalArtistLocaleMetadataRow(ctx, tx, artistID, locale, forUpdate)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	if !exists {
		if artistSnapshotContainsLocale(snapshot, locale) {
			return artistTargetLocaleState{}, errs.FailedPrecondition("Artist target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveArtistTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return artistTargetLocaleState{}, err
	}
	return state, nil
}

func validateArtistTargetBatchAuthority(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Artist target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Artist target locale cannot mutate the shared Block graph",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Artist target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Artist target mutation must match the authorized locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Artist target patch cannot unset locale Blocks; write explicit empty values instead",
			)
		}
	}
	return nil
}

func validateArtistSourceStorage(
	storage contentv1.ContentStorageMutationBatch,
	sourceLocale string,
) error {
	for _, group := range storage.LocaleGroups {
		if group.Locale != sourceLocale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Artist source room mutation must match the current source locale",
			)
		}
	}
	return nil
}

func applyArtistTargetLocaleBatch(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	expectedTargetRevision *string,
	allowCreate bool,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, string, error) {
	return applyArtistTargetLocaleBatchAuthority(
		ctx, tx, store, artistID, documentID, locale, batch, expectedTargetRevision,
		allowCreate, false, false, now, fence,
	)
}

func applyArtistTargetLocaleBatchAuthority(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	expectedTargetRevision *string,
	allowCreate bool,
	allowLocaleDeletes bool,
	useCurrentTargetRevision bool,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, string, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	state, err := loadArtistTargetLocaleState(ctx, tx, store, artistID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, "", errs.InvalidArgument("locale", "Artist target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, "", errs.FailedPrecondition("Artist source locale changed; reload before saving")
	}
	if err := validateArtistTargetBatchAuthority(
		batch, documentID, state.Snapshot.Document.Revision, locale, allowLocaleDeletes,
	); err != nil {
		return contentblock.Result{}, "", err
	}
	if err := translation.ValidateTargetRevisionWrite(
		expectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, useCurrentTargetRevision,
	); err != nil {
		return contentblock.Result{}, "", err
	}
	if state.TargetMetadata == nil && !allowCreate {
		return contentblock.Result{}, "", errs.FailedPrecondition("Artist target locale must be explicitly created before collaboration")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC().Truncate(time.Microsecond)
	var final artistLocaleMetadataRow
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, artistLockedTargetFence(documentID, domain),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				created := tx.WithContext(ctx).Exec(
					"INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, NULL, ?, ?)",
					artistID, locale, now, now,
				)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Artist target locale could not be created")
				}
				final = artistLocaleMetadataRow{Locale: locale, UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			final = *state.TargetMetadata
			if !contentChanged {
				return contentblock.MetadataEffect{}, nil
			}
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated := tx.WithContext(ctx).Table("artist_translation").
				Where("entity_id = ?::uuid AND locale = ?", artistID, locale).
				UpdateColumn("updated_at", final.UpdatedAt)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Artist target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, "", normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.Artist{}).
			Where("id = ?", artistID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return contentblock.Result{}, "", errs.Internal(err)
		}
	}
	targetRevision, err := deriveArtistTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	return result, targetRevision, nil
}

func updateArtistTargetLocaleTitle(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	expectedRevision uuid.UUID,
	expectedTargetRevision *string,
	title *string,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, string, error) {
	if title == nil {
		return contentblock.Result{}, "", errs.InvalidArgument("title", "is required")
	}
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	state, err := loadArtistTargetLocaleState(ctx, tx, store, artistID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	if state.TargetMetadata == nil || locale == state.SourceLocale {
		return contentblock.Result{}, "", errs.FailedPrecondition("Artist target locale must exist before metadata is edited")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, "", errs.FailedPrecondition("Artist source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != expectedRevision {
		return contentblock.Result{}, "", &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateExpectedTargetRevision(expectedTargetRevision, state.TargetRevision, true); err != nil {
		return contentblock.Result{}, "", err
	}
	normalizedTitle := strings.TrimSpace(*title)
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC().Truncate(time.Microsecond)
	final := *state.TargetMetadata
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, artistLockedTargetFence(documentID, domain),
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			if final.Title != nil && *final.Title == normalizedTitle {
				return contentblock.MetadataEffect{}, nil
			}
			final.Title = &normalizedTitle
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated := tx.WithContext(ctx).Table("artist_translation").
				Where("entity_id = ?::uuid AND locale = ?", artistID, locale).
				Updates(map[string]any{"title": normalizedTitle, "updated_at": final.UpdatedAt})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Artist target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, "", normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.Artist{}).
			Where("id = ?", artistID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return contentblock.Result{}, "", errs.Internal(err)
		}
	}
	targetRevision, err := deriveArtistTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return contentblock.Result{}, "", err
	}
	return result, targetRevision, nil
}

func deleteArtistTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	artistID string,
	documentID uuid.UUID,
	locale string,
	expectedRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	fence contentblock.DomainFence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadArtistTargetLocaleState(ctx, tx, store, artistID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil || locale == state.SourceLocale {
		return contentblock.Result{}, errs.FailedPrecondition("Artist target locale must exist before deletion")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, errs.FailedPrecondition("Artist source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != expectedRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateExpectedTargetRevision(expectedTargetRevision, state.TargetRevision, true); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
	}
	group := contentblock.LocaleMutationGroup{Locale: locale}
	for _, overlay := range state.Snapshot.LocaleOverlays {
		if overlay.Locale != locale {
			continue
		}
		for _, block := range overlay.Blocks {
			group.Deletes = append(group.Deletes, block.BlockID)
		}
		break
	}
	if len(group.Deletes) != 0 {
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{group}
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, artistLockedTargetFence(documentID, domain),
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec(
				"DELETE FROM artist_translation WHERE entity_id = ?::uuid AND locale = ?", artistID, locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Artist target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.Artist{}).
			Where("id = ?", artistID).
			UpdateColumn("updated_at", time.Now().UTC()).Error; err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}

func artistLockedTargetFence(
	documentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Artist content document changed; reload before saving")
		}
		return domain, nil
	}
}

func artistLocalizedDocument(
	state artistTargetLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	return document, nil
}

// artistSparseLocalizedDocument is the exact durable locale projection used by
// AI/XLIFF callers. Unlike the collaboration/public projection, it must retain
// absent target leaves so the caller can distinguish an absent unit from an
// explicitly authored empty value.
func artistSparseLocalizedDocument(
	state artistTargetLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCreativeContentBlockError(artistContentEntity, err)
	}
	return document, nil
}
