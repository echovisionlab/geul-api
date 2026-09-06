package release

import (
	"context"
	"errors"
	"maps"
	"sort"
	"strings"
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

type releaseLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type releaseTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata releaseLocaleMetadataRow
	SourceNotes    map[string]string
	TargetMetadata *releaseLocaleMetadataRow
	TargetNotes    map[string]string
	TargetRevision string
}

type releaseTargetLocaleMutationInput struct {
	ReleaseID                string
	DocumentID               uuid.UUID
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	AllowCreate              bool
	// AllowLocaleDeletes is reserved for authoritative whole-replacement
	// translation delivery. Interactive Collab and DCDP mutations preserve
	// locale units and represent an intentional empty value as an upsert.
	AllowLocaleDeletes        bool
	OverwriteCurrentTargetCAS bool
	Batch                     contentblock.Batch
	SetTitle                  bool
	Title                     *string
	CreditNotePatch           map[string]string
	ReplaceCreditNotes        *map[string]string
	Now                       time.Time
	Fence                     contentblock.DomainFence
}

type releaseTargetLocaleMutationResult struct {
	Content        contentblock.Result
	TargetRevision string
	LocaleCreated  bool
	TitleChanged   bool
}

func normalizeReleaseDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func loadOptionalReleaseLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
	locale string,
	forUpdate bool,
) (releaseLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("release_translation").
		Select("locale", "title", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", releaseID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row releaseLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return releaseLocaleMetadataRow{}, false, nil
		}
		return releaseLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func releaseSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		// Overlay presence is the durable locale resource boundary. An empty
		// overlay can still contain explicit-empty leaf values after restore,
		// and must not be mistaken for a missing target.
		if overlay.Locale == locale {
			return true
		}
	}
	return false
}

func deriveReleaseTargetRevision(documentRevision string, metadata releaseLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func normalizeReleaseTargetWriterError(err error) error {
	var conflict *translation.TargetRevisionConflict
	if errors.As(err, &conflict) {
		return errs.FailedPrecondition("Release target revision changed; reload before saving")
	}
	return normalizeReleaseContentBlockError(err)
}

func loadReleaseTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	releaseID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (releaseTargetLocaleState, error) {
	locale, err := normalizeReleaseDocumentLocale(locale)
	if err != nil {
		return releaseTargetLocaleState{}, err
	}
	strength := "SHARE"
	if forUpdate {
		strength = "UPDATE"
	}
	source, err := loadReleaseSourceLocaleWithStrength(ctx, tx, releaseID, strength)
	if err != nil {
		return releaseTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, source.SourceLocale)
	if err != nil {
		return releaseTargetLocaleState{}, normalizeReleaseContentBlockError(err)
	}
	if snapshot.SourceLocale != source.SourceLocale || snapshot.Document.Profile != releaseContentProfile {
		return releaseTargetLocaleState{}, errs.FailedPrecondition("Release content document source or profile is inconsistent")
	}
	sourceMetadata, exists, err := loadOptionalReleaseLocaleMetadataRow(
		ctx, tx, releaseID, source.SourceLocale, forUpdate,
	)
	if err != nil {
		return releaseTargetLocaleState{}, err
	}
	if !exists {
		return releaseTargetLocaleState{}, errs.FailedPrecondition("Release source locale metadata is missing")
	}
	sourceNotes, err := loadReleaseCreditLocaleNotes(ctx, tx, releaseID, source.SourceLocale)
	if err != nil {
		return releaseTargetLocaleState{}, errs.Internal(err)
	}
	state := releaseTargetLocaleState{
		Snapshot: snapshot, SourceLocale: source.SourceLocale,
		SourceMetadata: sourceMetadata, SourceNotes: sourceNotes,
	}
	if locale == source.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		state.TargetNotes = maps.Clone(sourceNotes)
		return state, nil
	}
	target, exists, err := loadOptionalReleaseLocaleMetadataRow(ctx, tx, releaseID, locale, forUpdate)
	if err != nil {
		return releaseTargetLocaleState{}, err
	}
	if !exists {
		if releaseSnapshotContainsLocale(snapshot, locale) {
			return releaseTargetLocaleState{}, errs.FailedPrecondition("Release target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	targetNotes, err := loadReleaseCreditLocaleNotes(ctx, tx, releaseID, locale)
	if err != nil {
		return releaseTargetLocaleState{}, errs.Internal(err)
	}
	state.TargetMetadata = &target
	state.TargetNotes = targetNotes
	state.TargetRevision, err = deriveReleaseTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return releaseTargetLocaleState{}, err
	}
	return state, nil
}

func applyReleaseTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input releaseTargetLocaleMutationInput,
) (releaseTargetLocaleMutationResult, error) {
	if store == nil || input.DocumentID == uuid.Nil || input.ExpectedDocumentRevision == uuid.Nil ||
		strings.TrimSpace(input.ReleaseID) == "" || strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return releaseTargetLocaleMutationResult{}, errs.InvalidArgument("target", "Release target identity, revision, and fence are required")
	}
	domain, err := input.Fence(ctx, tx, input.DocumentID)
	if err != nil {
		return releaseTargetLocaleMutationResult{}, err
	}
	state, err := loadReleaseTargetLocaleState(
		ctx, tx, store, input.ReleaseID, input.DocumentID, input.Locale, true,
	)
	if err != nil {
		return releaseTargetLocaleMutationResult{}, err
	}
	if input.Locale == state.SourceLocale || domain.SourceLocale != state.SourceLocale {
		return releaseTargetLocaleMutationResult{}, errs.InvalidArgument("locale", "Release target locale must differ from the current source locale")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return releaseTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.OverwriteCurrentTargetCAS,
	); err != nil {
		return releaseTargetLocaleMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return releaseTargetLocaleMutationResult{}, errs.FailedPrecondition(
			"Release target locale must be explicitly created before collaboration",
		)
	}
	if err := validateReleaseTargetBatch(
		input.Batch,
		input.DocumentID,
		input.ExpectedDocumentRevision,
		input.Locale,
		input.AllowLocaleDeletes,
	); err != nil {
		return releaseTargetLocaleMutationResult{}, err
	}
	batch := contentblock.CloneBatch(input.Batch)
	if state.TargetMetadata == nil {
		batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
		if err != nil {
			return releaseTargetLocaleMutationResult{}, err
		}
	}
	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	result := releaseTargetLocaleMutationResult{LocaleCreated: state.TargetMetadata == nil}
	var finalMetadata releaseLocaleMetadataRow
	authorizedFence := func(_ context.Context, _ *gorm.DB, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != input.DocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Release content document changed; reload before saving")
		}
		return domain, nil
	}
	storeResult, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx, tx, batch, input.Locale, authorizedFence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			currentMetadata := state.SourceMetadata
			currentNotes := maps.Clone(state.SourceNotes)
			if state.TargetMetadata != nil {
				currentMetadata = *state.TargetMetadata
				currentNotes = maps.Clone(state.TargetNotes)
			}
			nextTitle := currentMetadata.Title
			if input.SetTitle {
				result.TitleChanged = !releaseOptionalStringsEqual(currentMetadata.Title, input.Title)
				nextTitle = input.Title
			}
			nextNotes := maps.Clone(currentNotes)
			if input.ReplaceCreditNotes != nil {
				nextNotes = maps.Clone(*input.ReplaceCreditNotes)
			}
			for creditID, note := range input.CreditNotePatch {
				nextNotes[creditID] = note
			}
			if err := validateReleaseCreditNoteIdentityMap(ctx, tx, input.ReleaseID, nextNotes); err != nil {
				return contentblock.MetadataEffect{}, err
			}
			metadataChanged := result.TitleChanged || !maps.Equal(currentNotes, nextNotes)
			if state.TargetMetadata == nil {
				finalMetadata = releaseLocaleMetadataRow{Locale: input.Locale, Title: nextTitle, UpdatedAt: now}
				created := tx.WithContext(ctx).Exec(
					"INSERT INTO release_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?)",
					input.ReleaseID, input.Locale, nextTitle, now, now,
				)
				if created.Error != nil || created.RowsAffected != 1 {
					if created.Error != nil {
						return contentblock.MetadataEffect{}, errs.Internal(created.Error)
					}
					return contentblock.MetadataEffect{}, errs.InternalMsg("Release target locale could not be created")
				}
				if err := replaceReleaseCreditLocaleNotes(ctx, tx, input.ReleaseID, input.Locale, nextNotes, now); err != nil {
					return contentblock.MetadataEffect{}, errs.Internal(err)
				}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			if !contentChanged && !metadataChanged {
				finalMetadata = currentMetadata
				return contentblock.MetadataEffect{}, nil
			}
			updatedAt := translation.NextTargetUpdatedAt(now, currentMetadata.UpdatedAt)
			updated := tx.WithContext(ctx).Table("release_translation").
				Where("entity_id = ?::uuid AND locale = ?", input.ReleaseID, input.Locale).
				Updates(map[string]any{"title": nextTitle, "updated_at": updatedAt})
			if updated.Error != nil || updated.RowsAffected != 1 {
				if updated.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
				}
				return contentblock.MetadataEffect{}, errs.InternalMsg("Release target locale disappeared while saving")
			}
			if !maps.Equal(currentNotes, nextNotes) {
				if err := replaceReleaseCreditLocaleNotes(ctx, tx, input.ReleaseID, input.Locale, nextNotes, updatedAt); err != nil {
					return contentblock.MetadataEffect{}, errs.Internal(err)
				}
			}
			finalMetadata = releaseLocaleMetadataRow{Locale: input.Locale, Title: nextTitle, UpdatedAt: updatedAt}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		},
	)
	if err != nil {
		return releaseTargetLocaleMutationResult{}, normalizeReleaseContentBlockError(err)
	}
	result.Content = storeResult
	result.TargetRevision, err = deriveReleaseTargetRevision(storeResult.DocumentRevision.String(), finalMetadata)
	if err != nil {
		return releaseTargetLocaleMutationResult{}, err
	}
	return result, nil
}

func validateReleaseTargetBatch(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "Release target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Release target locale cannot mutate the shared Block graph",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Release target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Release target mutation must match the authorized locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Release target locale units cannot be deleted by interactive editing; write an explicit empty value instead",
			)
		}
	}
	return nil
}

func validateReleaseCreditNoteIdentityMap(
	ctx context.Context,
	tx *gorm.DB,
	releaseID string,
	notes map[string]string,
) error {
	creditIDs, err := loadReleaseCreditIDs(ctx, tx, releaseID)
	if err != nil {
		return err
	}
	allowed := make(map[string]struct{}, len(creditIDs))
	for _, creditID := range creditIDs {
		allowed[creditID] = struct{}{}
	}
	for creditID := range notes {
		if _, ok := allowed[creditID]; !ok {
			return errs.InvalidArgument("credit_notes", "credit_id does not belong to the Release")
		}
	}
	return nil
}

func deleteReleaseTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	releaseID string,
	documentID uuid.UUID,
	locale string,
	expectedDocumentRevision uuid.UUID,
	expectedTargetRevision *string,
	contributors []uuid.UUID,
	fence contentblock.DomainFence,
) (contentblock.Result, error) {
	domain, err := fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadReleaseTargetLocaleState(ctx, tx, store, releaseID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale || state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, errs.InvalidArgument("locale", "Release source translation cannot be deleted")
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
	batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedDocumentRevision, ContributorMemberIDs: append([]uuid.UUID(nil), contributors...)}
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
	authorizedFence := func(_ context.Context, _ *gorm.DB, requested uuid.UUID) (contentblock.DomainContext, error) {
		if requested != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Release content document changed; reload before saving")
		}
		return domain, nil
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(ctx, tx, batch, locale, authorizedFence, func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
		if _, err := deleteReleaseAIDocumentTranslation(ctx, tx, releaseID, locale); err != nil {
			return contentblock.MetadataEffect{}, err
		}
		return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
	})
	if err != nil {
		return contentblock.Result{}, normalizeReleaseContentBlockError(err)
	}
	return result, nil
}
