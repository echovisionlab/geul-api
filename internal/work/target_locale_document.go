package work

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type workLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type workTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata workLocaleMetadataRow
	TargetMetadata *workLocaleMetadataRow
	TargetRevision string
}

type workTargetMetadataPatch struct {
	EnsureLocale  bool
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
	DeleteLocale  bool
}

// workTargetMetadataProjection is the read-time locale projection. A target
// row may intentionally omit one nullable value; that value falls back to the
// current source value for resident/public reads while exact mutation state
// continues to preserve the target row's sparse presence.
func workTargetMetadataProjection(
	source workLocaleMetadataRow,
	target *workLocaleMetadataRow,
	locale string,
) workLocaleMetadataRow {
	projected := workLocaleMetadataRow{
		Locale:  locale,
		Title:   cloneOptionalString(source.Title),
		Summary: cloneOptionalString(source.Summary),
	}
	if target == nil {
		return projected
	}
	if target.Title != nil {
		projected.Title = cloneOptionalString(target.Title)
	}
	if target.Summary != nil {
		projected.Summary = cloneOptionalString(target.Summary)
	}
	projected.UpdatedAt = target.UpdatedAt
	return projected
}

func normalizeWorkDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func workSourceRoomFence(locale string, base contentblock.DomainFence) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, tx, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, errs.CollaborationConflict(
				intrav1.CollaborationConflictReason_COLLABORATION_CONFLICT_REASON_LOCALE_OWNERSHIP_CHANGED,
				"Work source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func loadOptionalWorkLocaleMetadataRow(ctx context.Context, tx *gorm.DB, workID, locale string, forUpdate bool) (workLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("work_translation").Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", workID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row workLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return workLocaleMetadataRow{}, false, nil
		}
		return workLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func deriveWorkTargetRevision(documentRevision string, metadata workLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func workSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale {
			return true
		}
	}
	return false
}

func validateWorkSourceStorage(
	storage contentv1.ContentStorageMutationBatch,
	sourceLocale string,
) error {
	for _, group := range storage.LocaleGroups {
		if group.Locale != sourceLocale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Work source room mutation must match the current source locale",
			)
		}
	}
	return nil
}

func loadWorkTargetLocaleState(ctx context.Context, tx *gorm.DB, store *contentblock.Store, workID string, documentID uuid.UUID, locale string, forUpdate bool) (workTargetLocaleState, error) {
	locale, err := normalizeWorkDocumentLocale(locale)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	lock := "SHARE"
	if forUpdate {
		lock = "UPDATE"
	}
	_, lockedDocumentID, err := loadWorkAIDocumentRoot(ctx, tx, workID, lock)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return workTargetLocaleState{}, errs.FailedPrecondition("Work content document changed; reload before saving")
	}
	domain, err := loadWorkContentDomainContext(ctx, tx, workID)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return workTargetLocaleState{}, normalizeWorkContentBlockError(err)
	}
	if snapshot.SourceLocale != domain.SourceLocale || snapshot.Document.Profile != workContentDocumentProfile {
		return workTargetLocaleState{}, errs.FailedPrecondition("Work content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := loadOptionalWorkLocaleMetadataRow(ctx, tx, workID, domain.SourceLocale, forUpdate)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	if !sourceExists {
		return workTargetLocaleState{}, errs.FailedPrecondition("Work source locale metadata is missing")
	}
	state := workTargetLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: sourceMetadata}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := loadOptionalWorkLocaleMetadataRow(ctx, tx, workID, locale, forUpdate)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	if !exists {
		if workSnapshotContainsLocale(snapshot, locale) {
			return workTargetLocaleState{}, errs.FailedPrecondition("Work target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveWorkTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return workTargetLocaleState{}, err
	}
	return state, nil
}

func validateWorkTargetBatchAuthority(batch contentblock.Batch, documentID, expectedRevision uuid.UUID, locale string, allowLocaleDeletes bool) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Work target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Work target locale cannot mutate the shared Block graph or File ownership",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Work target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Work target mutation must match the authorized locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Work target patch cannot unset locale Blocks; write explicit empty values instead",
			)
		}
	}
	return nil
}

func applyWorkTargetLocaleBatch(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	workID string,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	expectedTargetRevision *string,
	patch workTargetMetadataPatch,
	allowCreate bool,
	seedSource bool,
	allowLocaleDeletes bool,
	useCurrentTargetRevision bool,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, *string, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	state, err := loadWorkTargetLocaleState(ctx, tx, store, workID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, nil, errs.InvalidArgument("locale", "Work target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, nil, errs.FailedPrecondition("Work source locale changed; reload before saving")
	}
	if err := validateWorkTargetBatchAuthority(batch, documentID, state.Snapshot.Document.Revision, locale, allowLocaleDeletes); err != nil {
		return contentblock.Result{}, nil, err
	}
	if err := translation.ValidateTargetRevisionWrite(
		expectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, useCurrentTargetRevision,
	); err != nil {
		return contentblock.Result{}, nil, err
	}
	if patch.DeleteLocale && (patch.EnsureLocale || patch.UpdateTitle || patch.UpdateSummary || len(batch.LocaleGroups) != 0) {
		return contentblock.Result{}, nil, errs.InvalidArgument("operations", "Work translation deletion must be exclusive")
	}
	if state.TargetMetadata == nil && !allowCreate {
		return contentblock.Result{}, nil, errs.FailedPrecondition("Work target locale must be explicitly created before collaboration")
	}
	if patch.DeleteLocale && state.TargetMetadata == nil {
		return contentblock.Result{}, nil, errs.FailedPrecondition("Work target locale must exist before deletion")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC().Truncate(time.Microsecond)
	if state.TargetMetadata == nil && seedSource {
		batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, locale)
		if err != nil {
			return contentblock.Result{}, nil, err
		}
	}
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
	var final workLocaleMetadataRow
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, workLockedTargetFence(documentID, domain),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if patch.DeleteLocale {
				deleted := tx.WithContext(ctx).Exec("DELETE FROM work_translation WHERE entity_id = ?::uuid AND locale = ?", workID, locale)
				if deleted.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
				}
				if deleted.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Work target locale disappeared while deleting")
				}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			if state.TargetMetadata == nil {
				title, summary := patch.Title, patch.Summary
				if seedSource {
					title = cloneOptionalString(state.SourceMetadata.Title)
					summary = cloneOptionalString(state.SourceMetadata.Summary)
					if patch.UpdateTitle {
						title = cloneOptionalString(patch.Title)
					}
					if patch.UpdateSummary {
						summary = cloneOptionalString(patch.Summary)
					}
				}
				created := tx.WithContext(ctx).Exec(
					"INSERT INTO work_translation (entity_id, locale, title, summary, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?, ?)",
					workID, locale, title, summary, now, now,
				)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Work target locale could not be created")
				}
				final = workLocaleMetadataRow{Locale: locale, Title: cloneOptionalString(title), Summary: cloneOptionalString(summary), UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			final = *state.TargetMetadata
			updates := map[string]any{}
			metadataChanged := false
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
			updated := tx.WithContext(ctx).Table("work_translation").Where("entity_id = ?::uuid AND locale = ?", workID, locale).Updates(updates)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Work target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, nil, normalizeWorkContentBlockError(err)
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Table("work").Where("id = ?::uuid", workID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return contentblock.Result{}, nil, errs.Internal(err)
		}
	}
	if patch.DeleteLocale {
		return result, nil, nil
	}
	revision, err := deriveWorkTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	return result, &revision, nil
}

func workLockedTargetFence(documentID uuid.UUID, domain contentblock.DomainContext) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Work content document changed; reload before saving")
		}
		return domain, nil
	}
}

func workContributorUUIDs(values []string) ([]uuid.UUID, error) {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		parsed, err := uuid.Parse(value)
		if err != nil || parsed == uuid.Nil || parsed.String() != value {
			return nil, errs.InvalidArgument("contributor_member_ids", "must contain canonical Member UUIDs")
		}
		result = append(result, parsed)
	}
	return result, nil
}
