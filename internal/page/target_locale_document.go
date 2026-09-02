package page

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pageLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type pageTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata pageLocaleMetadataRow
	TargetMetadata *pageLocaleMetadataRow
	TargetRevision string
}

type pageTargetMetadataPatch struct {
	EnsureLocale  bool
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
	DeleteLocale  bool
}

func normalizePageDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func pageSourceRoomFence(pageID, locale string, base contentblock.DomainFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_LOCALE_OWNERSHIP_CHANGED,
				"Page source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func loadOptionalPageLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	locale string,
	forUpdate bool,
) (pageLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("page_translation").
		Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", pageID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row pageLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return pageLocaleMetadataRow{}, false, nil
		}
		return pageLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func derivePageTargetRevision(documentRevision string, metadata pageLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func pageSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale && len(overlay.Blocks) != 0 {
			return true
		}
	}
	return false
}

func loadPageTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (pageTargetLocaleState, error) {
	locale, err := normalizePageDocumentLocale(locale)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	lock := "SHARE"
	if forUpdate {
		lock = "UPDATE"
	}
	_, lockedDocumentID, err := loadPageAIDocumentRoot(ctx, tx, pageID, lock)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return pageTargetLocaleState{}, errs.FailedPrecondition("Page content document changed; reload before saving")
	}
	domain, err := loadPageContentDomainContext(ctx, tx, pageID)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return pageTargetLocaleState{}, normalizePageContentBlockError(err)
	}
	if snapshot.SourceLocale != domain.SourceLocale || snapshot.Document.Profile != "page" {
		return pageTargetLocaleState{}, errs.FailedPrecondition("Page content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := loadOptionalPageLocaleMetadataRow(ctx, tx, pageID, domain.SourceLocale, forUpdate)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if !sourceExists {
		return pageTargetLocaleState{}, errs.FailedPrecondition("Page source locale metadata is missing")
	}
	state := pageTargetLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: sourceMetadata}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := loadOptionalPageLocaleMetadataRow(ctx, tx, pageID, locale, forUpdate)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if !exists {
		if pageSnapshotContainsLocale(snapshot, locale) {
			return pageTargetLocaleState{}, errs.FailedPrecondition("Page target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = derivePageTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	return state, nil
}

func validatePageTargetBatchAuthority(batch contentblock.Batch, documentID, expectedRevision uuid.UUID, locale string, allowLocaleDeletes bool) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Page target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Page target locale cannot mutate the shared section graph or File ownership",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Page target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Page target mutation must match the authorized locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Page target patch cannot unset locale Blocks; write explicit empty values instead",
			)
		}
	}
	return nil
}

func applyPageTargetLocaleBatch(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	expectedTargetRevision *string,
	patch pageTargetMetadataPatch,
	allowCreate bool,
	allowLocaleDeletes bool,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, *string, error) {
	return applyPageTargetLocaleBatchWithRevisionPolicy(
		ctx, tx, store, pageID, documentID, locale, batch, expectedTargetRevision,
		patch, allowCreate, allowLocaleDeletes, false, now, fence,
	)
}

func applyPageTargetLocaleBatchUsingCurrentRevision(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	patch pageTargetMetadataPatch,
	allowCreate bool,
	allowLocaleDeletes bool,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, *string, error) {
	return applyPageTargetLocaleBatchWithRevisionPolicy(
		ctx, tx, store, pageID, documentID, locale, batch, nil,
		patch, allowCreate, allowLocaleDeletes, true, now, fence,
	)
}

func applyPageTargetLocaleBatchWithRevisionPolicy(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	pageID string,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	expectedTargetRevision *string,
	patch pageTargetMetadataPatch,
	allowCreate bool,
	allowLocaleDeletes bool,
	useCurrentTargetRevision bool,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, *string, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	state, err := loadPageTargetLocaleState(ctx, tx, store, pageID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, nil, errs.InvalidArgument("locale", "Page target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, nil, errs.FailedPrecondition("Page source locale changed; reload before saving")
	}
	if err := validatePageTargetBatchAuthority(batch, documentID, state.Snapshot.Document.Revision, locale, allowLocaleDeletes); err != nil {
		return contentblock.Result{}, nil, err
	}
	if err := translation.ValidateTargetRevisionWrite(
		expectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, useCurrentTargetRevision,
	); err != nil {
		return contentblock.Result{}, nil, err
	}
	if patch.DeleteLocale && (patch.EnsureLocale || patch.UpdateTitle || patch.UpdateSummary || len(batch.LocaleGroups) != 0) {
		return contentblock.Result{}, nil, errs.InvalidArgument("operations", "Page translation deletion must be exclusive")
	}
	if state.TargetMetadata == nil && !allowCreate {
		return contentblock.Result{}, nil, errs.FailedPrecondition("Page target locale must be explicitly created before collaboration")
	}
	if patch.DeleteLocale && state.TargetMetadata == nil {
		return contentblock.Result{}, nil, errs.FailedPrecondition("Page target locale must exist before deletion")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC().Truncate(time.Microsecond)
	if patch.DeleteLocale {
		group := contentblock.LocaleMutationGroup{Locale: locale}
		for _, overlay := range state.Snapshot.LocaleOverlays {
			if overlay.Locale == locale {
				for _, block := range overlay.Blocks {
					group.Deletes = append(group.Deletes, block.BlockID)
				}
				break
			}
		}
		if len(group.Deletes) != 0 {
			batch.LocaleGroups = []contentblock.LocaleMutationGroup{group}
		}
	}
	var final pageLocaleMetadataRow
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, pageLockedTargetFence(documentID, domain),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if patch.DeleteLocale {
				deleted := tx.WithContext(ctx).Exec("DELETE FROM page_translation WHERE entity_id = ?::uuid AND locale = ?", pageID, locale)
				if deleted.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
				}
				if deleted.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Page target locale disappeared while deleting")
				}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			if state.TargetMetadata == nil {
				created := tx.WithContext(ctx).Exec(
					"INSERT INTO page_translation (entity_id, locale, title, summary, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?, ?)",
					pageID, locale, patch.Title, patch.Summary, now, now,
				)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Page target locale could not be created")
				}
				final = pageLocaleMetadataRow{Locale: locale, Title: cloneOptionalString(patch.Title), Summary: cloneOptionalString(patch.Summary), UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			final = *state.TargetMetadata
			metadataChanged := false
			updates := map[string]any{}
			if patch.UpdateTitle && !nullableStringEqual(final.Title, patch.Title) {
				final.Title, updates["title"], metadataChanged = cloneOptionalString(patch.Title), patch.Title, true
			}
			if patch.UpdateSummary && !nullableStringEqual(final.Summary, patch.Summary) {
				final.Summary, updates["summary"], metadataChanged = cloneOptionalString(patch.Summary), patch.Summary, true
			}
			if !contentChanged && !metadataChanged {
				return contentblock.MetadataEffect{}, nil
			}
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updates["updated_at"] = final.UpdatedAt
			updated := tx.WithContext(ctx).Table("page_translation").
				Where("entity_id = ?::uuid AND locale = ?", pageID, locale).Updates(updates)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Page target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, nil, normalizePageContentBlockError(err)
	}
	if patch.DeleteLocale {
		return result, nil, nil
	}
	targetRevision, err := derivePageTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	return result, &targetRevision, nil
}

func pageLockedTargetFence(documentID uuid.UUID, domain contentblock.DomainContext) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Page content document changed; reload before saving")
		}
		return domain, nil
	}
}

func pageContributorUUIDs(values []string) ([]uuid.UUID, error) {
	contributors := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		contributor, err := parsePageContentUUID("contributor_member_ids", value)
		if err != nil {
			return nil, err
		}
		contributors = append(contributors, contributor)
	}
	return contributors, nil
}
