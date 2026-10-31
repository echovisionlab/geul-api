package programevent

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// programEventEntityName names the owner in shared content document messages.
const programEventEntityName = "Program Event"

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

func (state programEventExactLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: programEventEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

func normalizeProgramEventDocumentLocale(value string) (string, error) {
	locale := localization.NormalizeExactSupportedLocale(value)
	if locale == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
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
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row programEventLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programEventLocaleMetadataRow{}, false, nil
		}
		return programEventLocaleMetadataRow{}, false, err
	}
	return row, true, nil
}

func deriveProgramEventTargetRevision(documentRevision string, metadata programEventLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
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
		return programEventExactLocaleState{}, domainerr.Precondition("Program Event content document profile changed")
	}
	sourceMetadata, exists, err := loadOptionalProgramEventLocaleMetadataRow(
		ctx, tx, eventID, source.SourceLocale, forUpdate,
	)
	if err != nil {
		return programEventExactLocaleState{}, err
	}
	if !exists {
		return programEventExactLocaleState{}, domainerr.Precondition("Program Event source locale metadata is not initialized")
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
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return programEventExactLocaleState{}, domainerr.Precondition("Program Event target locale Blocks exist without owning metadata")
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

type programEventTargetLocaleMutationInput struct {
	EventID                  string
	DocumentID               uuid.UUID
	Locale                   string
	Batch                    contentblock.Batch
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ExpectedLocaleExists     *bool
	Policy                   targetlocale.Policy
	SetSummary               bool
	Summary                  *string
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type programEventTargetLocaleMutationResult struct {
	Result         contentblock.Result
	TargetRevision string
	LocaleCreated  bool
}

func applyProgramEventTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input programEventTargetLocaleMutationInput,
) (programEventTargetLocaleMutationResult, error) {
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return programEventTargetLocaleMutationResult{}, err
	}
	state, err := loadProgramEventExactLocaleState(
		ctx, tx, store, input.EventID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return programEventTargetLocaleMutationResult{}, err
	}
	if input.Locale == state.SourceLocale {
		return programEventTargetLocaleMutationResult{}, domainerr.InvalidField("locale", "Program Event target locale must differ from source locale")
	}
	if domain.SourceLocale != state.SourceLocale {
		return programEventTargetLocaleMutationResult{}, domainerr.Precondition("Program Event source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return programEventTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if input.ExpectedLocaleExists != nil && *input.ExpectedLocaleExists != (state.TargetMetadata != nil) {
		return programEventTargetLocaleMutationResult{}, domainerr.Precondition(
			"Program Event target locale presence changed; reload before saving",
		)
	}
	batch, err := input.Policy.Admit(state.locked(), input.Locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return programEventTargetLocaleMutationResult{}, err
	}
	now := targetlocale.WriteTime(input.Now)
	output := programEventTargetLocaleMutationResult{LocaleCreated: state.TargetMetadata == nil}
	var final programEventLocaleMetadataRow
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, input.Locale, input.Fence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			var effect contentblock.MetadataEffect
			var err error
			final, effect, err = writeTargetLocaleMetadata(ctx, tx, input, state, now, contentChanged)
			return effect, err
		},
	)
	if err != nil {
		return programEventTargetLocaleMutationResult{}, normalizeProgramEventContentBlockError(err)
	}
	output.Result = result
	output.TargetRevision, err = deriveProgramEventTargetRevision(result.DocumentRevision.String(), final)
	if err != nil {
		return programEventTargetLocaleMutationResult{}, err
	}
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.ProgramEvent{}).
			Where("id = ?", input.EventID).UpdateColumn("updated_at", now).Error; err != nil {
			return programEventTargetLocaleMutationResult{}, err
		}
	}
	return output, nil
}

// writeTargetLocaleMetadata creates the target locale's metadata row,
// seeded from the source summary, or updates its summary and revision
// clock when the content or the summary changed. It returns the row as
// stored.
func writeTargetLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	input programEventTargetLocaleMutationInput,
	state programEventExactLocaleState,
	now time.Time,
	contentChanged bool,
) (programEventLocaleMetadataRow, contentblock.MetadataEffect, error) {
	changed := contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}
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
			return programEventLocaleMetadataRow{}, contentblock.MetadataEffect{}, created.Error
		}
		if created.RowsAffected != 1 {
			return programEventLocaleMetadataRow{}, contentblock.MetadataEffect{}, errors.New("program Event target locale could not be created")
		}
		return programEventLocaleMetadataRow{Locale: input.Locale, Summary: summary, UpdatedAt: now}, changed, nil
	}
	final := *state.TargetMetadata
	metadataChanged := input.SetSummary && !sameNullableString(final.Summary, input.Summary)
	if input.SetSummary {
		final.Summary = input.Summary
	}
	if !contentChanged && !metadataChanged {
		return final, contentblock.MetadataEffect{}, nil
	}
	final.UpdatedAt = translation.NextTargetUpdatedAt(now, final.UpdatedAt)
	updated := tx.WithContext(ctx).Table("program_event_translation").
		Where("entity_id = ? AND locale = ?", input.EventID, input.Locale).
		Updates(map[string]any{"summary": final.Summary, "updated_at": final.UpdatedAt})
	if updated.Error != nil {
		return programEventLocaleMetadataRow{}, contentblock.MetadataEffect{}, updated.Error
	}
	if updated.RowsAffected != 1 {
		return programEventLocaleMetadataRow{}, contentblock.MetadataEffect{}, errors.New("program Event target locale disappeared while saving")
	}
	return final, changed, nil
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
		return contentblock.Result{}, domainerr.Precondition("Program Event target locale must exist before deletion")
	}
	if domain.SourceLocale != state.SourceLocale {
		return contentblock.Result{}, domainerr.Precondition("Program Event source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != expectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := state.locked().CheckRevision(expectedTargetRevision, targetlocale.RevisionObserved); err != nil {
		return contentblock.Result{}, err
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: expectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), contributors...),
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, locale),
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, locale, fence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec(
				"DELETE FROM program_event_translation WHERE entity_id = ? AND locale = ?", eventID, locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, deleted.Error
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errors.New("program Event target locale disappeared while deleting")
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
			return contentblock.Result{}, err
		}
	}
	return result, nil
}
