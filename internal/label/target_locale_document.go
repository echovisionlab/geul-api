package label

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/translation"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type labelTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata labelLocaleMetadataRow
	TargetMetadata *labelLocaleMetadataRow
	TargetRevision string
}

type labelLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type labelTargetLocaleMutationInput struct {
	LabelID                  string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	Batch                    contentblock.Batch
	AllowCreate              bool
	SeedSource               bool
	// AllowLocaleDeletes is reserved for authoritative whole-replacement
	// translation-provider delivery. Interactive Collab and DCDP mutations
	// preserve every locale unit; they represent an intentional empty value as
	// an upsert instead of deleting the locale row.
	AllowLocaleDeletes bool
	// Translation provider delivery intentionally accepts the target current
	// at delivery time and overwrites those exact locale values under this row
	// lock. Interactive DCDP and Collab callers provide their observed CAS.
	UseCurrentTargetRevision bool
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type labelTargetLocaleMutationResult struct {
	Content        contentblock.Result
	TargetRevision string
	Created        bool
}

func loadLabelTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	labelID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (labelTargetLocaleState, error) {
	if tx == nil || store == nil || documentID == uuid.Nil {
		return labelTargetLocaleState{}, errs.InternalMsg("Label target locale dependencies are required")
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return labelTargetLocaleState{}, errs.Required("locale")
	}
	canonicalLocale, err := normalizeLabelDocumentLocale(locale)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	locale = canonicalLocale
	source, err := loadLabelSourceAuthority(ctx, tx, labelID, lockMode(forUpdate))
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if locale == source.SourceLocale {
		return labelTargetLocaleState{}, errs.InvalidArgument("locale", "Label target locale must differ from the source locale")
	}
	if forUpdate {
		var document contentblock.Document
		if err := tx.WithContext(ctx).Table("content_document").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", documentID).Take(&document).Error; err != nil {
			return labelTargetLocaleState{}, normalizeLabelContentBlockError(err)
		}
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return labelTargetLocaleState{}, normalizeLabelContentBlockError(err)
	}
	if snapshot.Document.Profile != creativeContentProfile {
		return labelTargetLocaleState{}, errs.FailedPrecondition("Label target locale requires the compact content profile")
	}
	sourceMetadata, exists, err := loadOptionalLabelLocaleMetadata(ctx, tx, labelID, source.SourceLocale, forUpdate)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if !exists {
		return labelTargetLocaleState{}, errs.InternalMsg("Label source locale metadata is missing")
	}
	target, exists, err := loadOptionalLabelLocaleMetadata(ctx, tx, labelID, locale, forUpdate)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if !exists && labelSnapshotContainsLocale(snapshot, locale) {
		return labelTargetLocaleState{}, errs.FailedPrecondition("Label target locale Blocks exist without owning metadata")
	}
	state := labelTargetLocaleState{Snapshot: snapshot, SourceLocale: source.SourceLocale, SourceMetadata: sourceMetadata}
	if exists {
		state.TargetMetadata = &target
		state.TargetRevision, err = deriveLabelTargetRevision(snapshot.Document.Revision.String(), target.UpdatedAt)
		if err != nil {
			return labelTargetLocaleState{}, err
		}
	}
	return state, nil
}

func lockMode(forUpdate bool) string {
	if forUpdate {
		return "UPDATE"
	}
	return ""
}

func loadOptionalLabelLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	labelID string,
	locale string,
	forUpdate bool,
) (labelLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("label_translation").
		Select("locale", "title", "created_at", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", labelID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row labelLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return labelLocaleMetadataRow{}, false, nil
		}
		return labelLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func deriveLabelTargetRevision(documentRevision string, updatedAt time.Time) (string, error) {
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func applyLabelTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input labelTargetLocaleMutationInput,
) (labelTargetLocaleMutationResult, error) {
	if input.ExpectedDocumentRevision == uuid.Nil || strings.TrimSpace(input.LabelID) == "" ||
		strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return labelTargetLocaleMutationResult{}, errs.InvalidArgument("target", "Label target identity, revision, and fence are required")
	}
	documentID, err := loadLabelContentDocumentID(ctx, tx, input.LabelID)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	domain, err := input.Fence(ctx, tx, documentID)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	state, err := loadLabelTargetLocaleState(ctx, tx, store, input.LabelID, documentID, input.Locale, true)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	if state.SourceLocale != domain.SourceLocale {
		return labelTargetLocaleMutationResult{}, errs.FailedPrecondition("Label source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return labelTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.UseCurrentTargetRevision,
	); err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return labelTargetLocaleMutationResult{}, errs.FailedPrecondition("Label target locale must be explicitly created before collaboration")
	}
	batch := contentblock.CloneBatch(input.Batch)
	if err := validateLabelTargetBatch(batch, documentID, input.ExpectedDocumentRevision, input.Locale, input.AllowLocaleDeletes); err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	if state.TargetMetadata == nil && input.SeedSource {
		batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
		if err != nil {
			return labelTargetLocaleMutationResult{}, err
		}
	}
	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	var finalUpdatedAt time.Time
	authorizedFence := func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Label content document changed; reload before saving")
		}
		return domain, nil
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, input.Locale, authorizedFence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				created := tx.WithContext(ctx).Exec(`
					INSERT INTO label_translation (entity_id, locale, title, created_at, updated_at)
					VALUES (?::uuid, ?, NULL, ?, ?)`, input.LabelID, input.Locale, now, now)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Label target locale could not be created")
				}
				finalUpdatedAt = now
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			finalUpdatedAt = state.TargetMetadata.UpdatedAt
			if !contentChanged {
				return contentblock.MetadataEffect{}, nil
			}
			finalUpdatedAt = translation.NextTargetUpdatedAt(now, state.TargetMetadata.UpdatedAt)
			updated := tx.WithContext(ctx).Table("label_translation").
				Where("entity_id = ?::uuid AND locale = ?", input.LabelID, input.Locale).
				Update("updated_at", finalUpdatedAt)
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Label target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		})
	if err != nil {
		return labelTargetLocaleMutationResult{}, normalizeLabelContentBlockError(err)
	}
	if finalUpdatedAt.IsZero() && state.TargetMetadata != nil {
		finalUpdatedAt = state.TargetMetadata.UpdatedAt
	}
	targetRevision, err := deriveLabelTargetRevision(result.DocumentRevision.String(), finalUpdatedAt)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	return labelTargetLocaleMutationResult{
		Content: result, TargetRevision: targetRevision, Created: state.TargetMetadata == nil,
	}, nil
}

func deleteLabelTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	labelID string,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	fence contentblock.DomainFence,
) (contentblock.Result, error) {
	documentID, err := loadLabelContentDocumentID(ctx, tx, labelID)
	if err != nil {
		return contentblock.Result{}, err
	}
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadLabelTargetLocaleState(ctx, tx, store, labelID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateExpectedTargetRevision(expectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil); err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil {
		return contentblock.Result{DocumentRevision: state.Snapshot.Document.Revision}, nil
	}
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedDocumentRevision, ContributorMemberIDs: contributors}
	group := contentblock.LocaleMutationGroup{Locale: locale}
	for _, overlay := range state.Snapshot.LocaleOverlays {
		if overlay.Locale == locale {
			for _, block := range overlay.Blocks {
				group.Deletes = append(group.Deletes, block.BlockID)
			}
		}
	}
	sort.Slice(group.Deletes, func(i, j int) bool { return group.Deletes[i].String() < group.Deletes[j].String() })
	if len(group.Deletes) != 0 {
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{group}
	}
	authorizedFence := func(_ context.Context, _ *gorm.DB, requestedDocumentID uuid.UUID) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Label content document changed; reload before saving")
		}
		return domain, nil
	}
	return store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, locale, authorizedFence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec("DELETE FROM label_translation WHERE entity_id = ?::uuid AND locale = ?", labelID, locale)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Label target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		})
}

func validateLabelTargetBatch(batch contentblock.Batch, documentID, expected uuid.UUID, locale string, allowLocaleDeletes bool) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expected {
		return errs.InvalidArgument("batch", "Label target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Label target locale cannot mutate the shared Block graph",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Label target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Label target locale mutation must match the authenticated room locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Label target locale units cannot be deleted by interactive editing; set an explicit empty value instead",
			)
		}
	}
	return nil
}

func labelSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale && len(overlay.Blocks) != 0 {
			return true
		}
	}
	return false
}
