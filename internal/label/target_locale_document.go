package label

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// labelEntityName names the owner in shared content document messages.
const labelEntityName = "Label"

type labelTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata labelLocaleMetadataRow
	TargetMetadata *labelLocaleMetadataRow
	TargetRevision string
}

func (state labelTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: labelEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
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
	Policy                   targetlocale.Policy
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
		return labelTargetLocaleState{}, errors.New("Label target locale dependencies are required")
	}
	locale = strings.TrimSpace(locale)
	if locale == "" {
		return labelTargetLocaleState{}, domainerr.Required("locale")
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
		return labelTargetLocaleState{}, domainerr.InvalidField("locale", "Label target locale must differ from the source locale")
	}
	if forUpdate {
		var document contentblock.Document
		if err := tx.WithContext(ctx).Table("content_document").
			Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			Where("id = ?", documentID).Take(&document).Error; err != nil {
			return labelTargetLocaleState{}, normalizeLabelContentBlockError(err)
		}
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return labelTargetLocaleState{}, normalizeLabelContentBlockError(err)
	}
	if snapshot.Document.Profile != creativeContentProfile {
		return labelTargetLocaleState{}, domainerr.Precondition("Label target locale requires the compact content profile")
	}
	sourceMetadata, exists, err := loadOptionalLabelLocaleMetadata(ctx, tx, labelID, source.SourceLocale, forUpdate)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if !exists {
		return labelTargetLocaleState{}, errors.New("Label source locale metadata is missing")
	}
	target, exists, err := loadOptionalLabelLocaleMetadata(ctx, tx, labelID, locale, forUpdate)
	if err != nil {
		return labelTargetLocaleState{}, err
	}
	if !exists && targetlocale.SnapshotHasLocale(snapshot, locale) {
		return labelTargetLocaleState{}, domainerr.Precondition("Label target locale Blocks exist without owning metadata")
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
		return clause.LockingStrengthUpdate
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row labelLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return labelLocaleMetadataRow{}, false, nil
		}
		return labelLocaleMetadataRow{}, false, err
	}
	return row, true, nil
}

func deriveLabelTargetRevision(documentRevision string, updatedAt time.Time) (string, error) {
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
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
		return labelTargetLocaleMutationResult{}, domainerr.InvalidField("target", "Label target identity, revision, and fence are required")
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
		return labelTargetLocaleMutationResult{}, domainerr.Precondition("Label source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return labelTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	batch, err := input.Policy.Admit(state.locked(), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return labelTargetLocaleMutationResult{}, err
	}
	now := targetlocale.WriteTime(input.Now)
	var finalUpdatedAt time.Time
	result, err := store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, input.Locale, contentblock.LockedFence(labelEntityName, documentID, domain),
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				created := tx.WithContext(ctx).Exec(`
					INSERT INTO label_translation (entity_id, locale, title, created_at, updated_at)
					VALUES (?::uuid, ?, NULL, ?, ?)`, input.LabelID, input.Locale, now, now)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, created.Error
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errors.New("Label target locale could not be created")
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
				return contentblock.MetadataEffect{}, updated.Error
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errors.New("Label target locale disappeared while saving")
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
	if err := state.locked().CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil {
		return contentblock.Result{DocumentRevision: state.Snapshot.Document.Revision}, nil
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision, ContributorMemberIDs: contributors,
		LocaleGroups: targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	return store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, locale, contentblock.LockedFence(labelEntityName, documentID, domain),
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec("DELETE FROM label_translation WHERE entity_id = ?::uuid AND locale = ?", labelID, locale)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, deleted.Error
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errors.New("Label target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		})
}
