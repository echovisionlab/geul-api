package programevent

import (
	"context"
	"errors"
	"sort"
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

type programEventLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type programEventExactLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata programEventLocaleMetadataRow
	TargetMetadata *programEventLocaleMetadataRow
	TargetRevision string
}

func normalizeProgramEventDocumentLocale(value string) (string, error) {
	locale := localization.NormalizeExactSupportedLocale(value)
	if locale == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *locale, nil
}

func loadOptionalProgramEventLocaleMetadataRow(
	ctx context.Context,
	db *gorm.DB,
	eventID string,
	locale string,
	forUpdate bool,
) (programEventLocaleMetadataRow, bool, error) {
	query := db.WithContext(ctx).Table("program_event_translation").
		Select("locale", "summary", "updated_at").
		Where("entity_id = ? AND locale = ?", eventID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row programEventLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programEventLocaleMetadataRow{}, false, nil
		}
		return programEventLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func deriveProgramEventTargetRevision(documentRevision string, metadata programEventLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func programEventSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale && len(overlay.Blocks) != 0 {
			return true
		}
	}
	return false
}

func loadProgramEventExactLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	eventID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (programEventExactLocaleState, error) {
	locale, err := normalizeProgramEventDocumentLocale(locale)
	if err != nil {
		return programEventExactLocaleState{}, err
	}
	source, err := loadProgramEventSourceLocale(ctx, tx, eventID, forUpdate)
	if err != nil {
		return programEventExactLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return programEventExactLocaleState{}, normalizeProgramEventContentBlockError(err)
	}
	if snapshot.Document.Profile != programEventContentProfile {
		return programEventExactLocaleState{}, errs.FailedPrecondition("Program Event content document profile changed")
	}
	sourceMetadata, exists, err := loadOptionalProgramEventLocaleMetadataRow(
		ctx, tx, eventID, source.SourceLocale, forUpdate,
	)
	if err != nil {
		return programEventExactLocaleState{}, err
	}
	if !exists {
		return programEventExactLocaleState{}, errs.FailedPrecondition("Program Event source locale metadata is not initialized")
	}
	state := programEventExactLocaleState{
		Snapshot: snapshot, SourceLocale: source.SourceLocale, SourceMetadata: sourceMetadata,
	}
	if locale == source.SourceLocale {
		state.TargetMetadata = &state.SourceMetadata
		return state, nil
	}
	target, exists, err := loadOptionalProgramEventLocaleMetadataRow(ctx, tx, eventID, locale, forUpdate)
	if err != nil {
		return programEventExactLocaleState{}, err
	}
	if !exists {
		if programEventSnapshotContainsLocale(snapshot, locale) {
			return programEventExactLocaleState{}, errs.FailedPrecondition("Program Event target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = deriveProgramEventTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return programEventExactLocaleState{}, err
	}
	return state, nil
}

func materializeProgramEventExactLocaleDocument(
	state programEventExactLocaleState,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	document, err := contentblock.MaterializeSnapshotRichTextLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeProgramEventContentBlockError(err)
	}
	return document, nil
}

func programEventLocaleMetadataProjection(
	source *intrav1.ProgramEventLocaleMetadata,
	target *programEventLocaleMetadataRow,
	locale string,
) *intrav1.ProgramEventLocaleMetadata {
	if source == nil || target == nil {
		return nil
	}
	projected := &intrav1.ProgramEventLocaleMetadata{
		Locale: locale, Title: source.Title, Summary: source.Summary,
	}
	if target.Summary != nil {
		projected.Summary = target.Summary
	}
	return projected
}

func validateProgramEventTargetBatch(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Program Event target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.InvalidArgument("batch", "Program Event target locale cannot mutate the shared Block graph")
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Program Event target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.InvalidArgument("batch", "Program Event target mutation must match the authorized locale")
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.InvalidArgument("batch", "Program Event target locale values use explicit empty and cannot be deleted")
		}
	}
	return nil
}

type programEventTargetMutationInput struct {
	EventID                  string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ExpectedLocaleExists     *bool
	AllowCreate              bool
	// SeedSourceOnCreate is the DCDP create/bootstrap contract. Provider and
	// interchange whole replacements leave omitted/new source units absent.
	SeedSourceOnCreate bool
	// AllowLocaleDeletes is reserved for authoritative whole-replacement
	// provider/interchange delivery. Interactive editing writes explicit empty.
	AllowLocaleDeletes bool
	// OverwriteCurrentTargetCAS lets an accepted provider response replace the
	// current target. Source/document coherence is still checked independently.
	OverwriteCurrentTargetCAS bool
	SetSummary                bool
	Summary                   *string
	Now                       time.Time
	Fence                     contentblock.DomainFence
}

type programEventTargetMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

func applyProgramEventTargetMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input programEventTargetMutationInput,
) (programEventTargetMutationResult, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return programEventTargetMutationResult{}, err
	}
	state, err := loadProgramEventExactLocaleState(
		ctx, tx, store, input.EventID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return programEventTargetMutationResult{}, err
	}
	if input.Locale == state.SourceLocale {
		return programEventTargetMutationResult{}, errs.InvalidArgument("locale", "Program Event target locale must differ from source locale")
	}
	if domain.SourceLocale != state.SourceLocale {
		return programEventTargetMutationResult{}, errs.FailedPrecondition("Program Event source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return programEventTargetMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return programEventTargetMutationResult{}, errs.FailedPrecondition(
			"Program Event target locale presence changed; reload before saving",
		)
	}
	if err := validateProgramEventTargetBatch(
		input.Batch, input.DocumentID, input.ExpectedDocumentRevision, input.Locale, input.AllowLocaleDeletes,
	); err != nil {
		return programEventTargetMutationResult{}, err
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.OverwriteCurrentTargetCAS,
	); err != nil {
		return programEventTargetMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return programEventTargetMutationResult{}, errs.FailedPrecondition(
			"Program Event target locale must be explicitly created before collaboration",
		)
	}
	batch := contentblock.CloneBatch(input.Batch)
	if state.TargetMetadata == nil && input.SeedSourceOnCreate {
		batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
		if err != nil {
			return programEventTargetMutationResult{}, err
		}
	}
	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	output := programEventTargetMutationResult{LocaleCreated: state.TargetMetadata == nil}
	var final programEventLocaleMetadataRow
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, input.Locale, input.Fence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			if state.TargetMetadata == nil {
				summary := state.SourceMetadata.Summary
				if input.SetSummary {
					summary = input.Summary
				}
				created := tx.WithContext(ctx).Exec(
					`INSERT INTO program_event_translation (entity_id, locale, summary, created_at, updated_at)
					 VALUES (?, ?, ?, ?, ?)`,
					input.EventID, input.Locale, summary, now, now,
				)
				if created.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(created.Error)
				}
				if created.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Program Event target locale could not be created")
				}
				final = programEventLocaleMetadataRow{Locale: input.Locale, Summary: summary, UpdatedAt: now}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			final = *state.TargetMetadata
			metadataChanged := input.SetSummary && !sameNullableString(final.Summary, input.Summary)
			if input.SetSummary {
				final.Summary = input.Summary
			}
			if !contentChanged && !metadataChanged {
				return contentblock.MetadataEffect{}, nil
			}
			final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
			updated := tx.WithContext(ctx).Table("program_event_translation").
				Where("entity_id = ? AND locale = ?", input.EventID, input.Locale).
				Updates(map[string]any{"summary": final.Summary, "updated_at": final.UpdatedAt})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Program Event target locale disappeared while saving")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		},
	)
	if err != nil {
		return programEventTargetMutationResult{}, normalizeProgramEventContentBlockError(err)
	}
	output.Result = result
	output.TargetRevision, err = deriveProgramEventTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return programEventTargetMutationResult{}, err
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.ProgramEvent{}).
			Where("id = ?", input.EventID).UpdateColumn("updated_at", now).Error; err != nil {
			return programEventTargetMutationResult{}, errs.Internal(err)
		}
	}
	return output, nil
}

func deleteProgramEventTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	eventID string,
	documentID uuid.UUID,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	now time.Time,
	fence contentblock.DomainFence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadProgramEventExactLocaleState(ctx, tx, store, eventID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale || state.TargetMetadata == nil {
		return contentblock.Result{}, errs.FailedPrecondition("Program Event target locale must exist before deletion")
	}
	if domain.SourceLocale != state.SourceLocale {
		return contentblock.Result{}, errs.FailedPrecondition("Program Event source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateExpectedTargetRevision(expectedTargetRevision, state.TargetRevision, true); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
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
	sort.Slice(group.Deletes, func(i, j int) bool { return group.Deletes[i].String() < group.Deletes[j].String() })
	if len(group.Deletes) != 0 {
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{group}
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, fence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec(
				"DELETE FROM program_event_translation WHERE entity_id = ? AND locale = ?", eventID, locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Program Event target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, normalizeProgramEventContentBlockError(err)
	}
	if result.Changed {
		if now.IsZero() {
			now = time.Now().UTC()
		}
		if err := tx.WithContext(ctx).Model(&model.ProgramEvent{}).
			Where("id = ?", eventID).UpdateColumn("updated_at", now.UTC()).Error; err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}
