package legal

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type legalTargetLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

type legalTargetLocaleState struct {
	Root              legalContentDocumentRoot
	Snapshot          contentblock.Snapshot
	SourceLocale      string
	SourceMetadata    legalTargetLocaleMetadataRow
	TargetMetadata    *legalTargetLocaleMetadataRow
	TargetRevision    string
	LocalizedDocument *contentv1.LocalizedRichTextDocument
}

type legalTargetLocaleMutationInput struct {
	EntityType               string
	EntityID                 string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	Batch                    contentblock.Batch
	AllowCreate              bool
	SeedSourceOnCreate       bool
	OverwriteExistingTarget  bool
	AllowLocaleDeletes       bool
	SetTitle                 bool
	Title                    *string
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type legalTargetLocaleMutationResult struct {
	DocumentRevision string
	TargetRevision   string
	Changed          bool
	LocaleCreated    bool
	ContentChanged   bool
	MetadataChanged  bool
}

type legalTargetLocaleDeleteInput struct {
	EntityType               string
	EntityID                 string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ContributorMemberIDs     []uuid.UUID
	OverwriteExistingTarget  bool
	Fence                    contentblock.DomainFence
}

func canonicalLegalLocale(value string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(value)
	if normalized == nil {
		return "", errs.InvalidArgument("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

func loadLegalTargetLocaleState(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
	locale string,
	lockTarget bool,
) (legalTargetLocaleState, error) {
	if tx == nil || store == nil {
		return legalTargetLocaleState{}, errs.InternalMsg("legal target locale dependencies are required")
	}
	locale, err := canonicalLegalLocale(locale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	root, err := loadLegalContentDocumentRoot(ctx, tx, entityType, entityID, lockTarget)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, *root.ContentDocumentID, root.SourceLocale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	if snapshot.SourceLocale != root.SourceLocale {
		return legalTargetLocaleState{}, errs.FailedPrecondition(
			entityType + " source locale does not match its content document",
		)
	}
	if snapshot.Document.Profile != legalContentDocumentProfile {
		return legalTargetLocaleState{}, errs.FailedPrecondition(entityType + " target locale requires the policy content profile")
	}
	// Collaboration consumers need the complete current block graph with
	// source fallback for leaves that are absent in this target locale. The
	// snapshot remains available separately for sparse presence and exact
	// target writes; never persist this materialized view as target state.
	document, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	result := legalTargetLocaleState{
		Root:         root,
		Snapshot:     snapshot,
		SourceLocale: root.SourceLocale,
		SourceMetadata: legalTargetLocaleMetadataRow{
			Locale: root.SourceLocale,
			Title:  stringPointer(root.Title),
		},
		LocalizedDocument: document,
	}
	if locale == root.SourceLocale {
		return result, nil
	}
	query := tx.WithContext(ctx).Table(entityType+"_translation").
		Select("locale", "title", "updated_at").
		Where("entity_id = ? AND locale = ?", entityID, locale)
	if lockTarget {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var target legalTargetLocaleMetadataRow
	if err := query.Take(&target).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return legalTargetLocaleState{}, errs.Internal(err)
		}
		if legalSnapshotContainsLocale(snapshot, locale) {
			return legalTargetLocaleState{}, errs.FailedPrecondition(
				entityType + " target locale Blocks exist without owning metadata",
			)
		}
		return result, nil
	}
	result.TargetMetadata = &target
	result.TargetRevision, err = deriveLegalTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return legalTargetLocaleState{}, err
	}
	return result, nil
}

func deriveLegalTargetRevision(
	documentRevision string,
	metadata legalTargetLocaleMetadataRow,
) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func legalSnapshotContainsLocale(snapshot contentblock.Snapshot, locale string) bool {
	for _, overlay := range snapshot.LocaleOverlays {
		if overlay.Locale == locale {
			return true
		}
	}
	return false
}

func validateLegalTargetBatch(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedRevision uuid.UUID,
	locale string,
	allowLocaleDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedRevision {
		return errs.InvalidArgument("batch", "legal target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"legal target locale cannot mutate the shared Block graph or File relations",
		)
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
			"legal target locale contains multiple locale groups",
		)
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"legal target locale mutation must match the authorized room locale",
			)
		}
		if len(group.Deletes) != 0 && !allowLocaleDeletes {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"legal target locale values must be set explicitly; ordinary edits cannot unset or delete locale Blocks",
			)
		}
	}
	return nil
}

func applyLegalTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input legalTargetLocaleMutationInput,
) (legalTargetLocaleMutationResult, error) {
	if tx == nil || store == nil || input.ExpectedDocumentRevision == uuid.Nil || input.Fence == nil {
		return legalTargetLocaleMutationResult{}, errs.InvalidArgument("target", "legal target identity, revision, and fence are required")
	}
	if err := validateAIDocumentIdentity(input.EntityType, input.EntityID, input.Locale); err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	locale, err := canonicalLegalLocale(input.Locale)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	state, err := loadLegalTargetLocaleState(
		ctx, tx, store, input.EntityType, input.EntityID, locale, true,
	)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	if locale == state.SourceLocale {
		return legalTargetLocaleMutationResult{}, errs.InvalidArgument("locale", "legal target locale must differ from the source locale")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return legalTargetLocaleMutationResult{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.OverwriteExistingTarget,
	); err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return legalTargetLocaleMutationResult{}, errs.FailedPrecondition(
			input.EntityType + " target locale must be explicitly created before collaboration editing",
		)
	}
	documentID := *state.Root.ContentDocumentID
	batch := input.Batch
	if err := validateLegalTargetBatch(
		batch, documentID, input.ExpectedDocumentRevision, locale, input.AllowLocaleDeletes,
	); err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	if state.TargetMetadata == nil && input.SeedSourceOnCreate {
		batch, err = contentblock.SeedTargetLocaleBatch(
			batch, state.Snapshot, state.Snapshot.SourceLocale, locale,
		)
		if err != nil {
			return legalTargetLocaleMutationResult{}, err
		}
	}
	domain, err := input.Fence(ctx, tx, documentID)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	if domain.SourceLocale != state.SourceLocale {
		return legalTargetLocaleMutationResult{}, errs.FailedPrecondition("legal source locale changed; reload before saving")
	}
	authorizedFence := func(
		_ context.Context,
		_ *gorm.DB,
		requestedDocumentID uuid.UUID,
	) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("legal content document changed; reload before saving")
		}
		return domain, nil
	}
	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	result := legalTargetLocaleMutationResult{
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		LocaleCreated:    state.TargetMetadata == nil,
	}
	var finalMetadata legalTargetLocaleMetadataRow
	storeResult, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx,
		tx,
		batch,
		locale,
		authorizedFence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			currentTitle := state.SourceMetadata.Title
			if state.TargetMetadata != nil {
				currentTitle = state.TargetMetadata.Title
			}
			title := currentTitle
			metadataChanged := false
			if input.SetTitle {
				title = input.Title
				metadataChanged = !sameNullableString(currentTitle, title)
			}
			if state.TargetMetadata == nil {
				finalMetadata = legalTargetLocaleMetadataRow{Locale: locale, Title: title, UpdatedAt: now}
				insert := tx.WithContext(ctx).Exec(
					"INSERT INTO "+input.EntityType+"_translation (entity_id, locale, title, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
					input.EntityID, locale, title, now, now,
				)
				if insert.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(insert.Error)
				}
				if insert.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("legal target locale could not be created")
				}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
			}
			if !contentChanged && !metadataChanged {
				finalMetadata = *state.TargetMetadata
				return contentblock.MetadataEffect{}, nil
			}
			updatedAt := translation.NextTargetUpdatedAt(now, state.TargetMetadata.UpdatedAt)
			updated := tx.WithContext(ctx).Table(input.EntityType+"_translation").
				Where("entity_id = ? AND locale = ?", input.EntityID, locale).
				Updates(structured.Fields{"title": title, "updated_at": updatedAt})
			if updated.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(updated.Error)
			}
			if updated.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("legal target locale disappeared while saving")
			}
			finalMetadata = legalTargetLocaleMetadataRow{Locale: locale, Title: title, UpdatedAt: updatedAt}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	result.Changed = storeResult.Changed
	result.ContentChanged = storeResult.ContentChanged
	result.MetadataChanged = storeResult.MetadataChanged
	result.TargetRevision, err = deriveLegalTargetRevision(result.DocumentRevision, finalMetadata)
	if err != nil {
		return legalTargetLocaleMutationResult{}, err
	}
	return result, nil
}

func deleteLegalTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input legalTargetLocaleDeleteInput,
) (contentblock.Result, error) {
	if tx == nil || store == nil || input.ExpectedDocumentRevision == uuid.Nil || input.Fence == nil {
		return contentblock.Result{}, errs.InvalidArgument("target", "legal target identity, revision, and fence are required")
	}
	if err := validateAIDocumentIdentity(input.EntityType, input.EntityID, input.Locale); err != nil {
		return contentblock.Result{}, err
	}
	locale, err := canonicalLegalLocale(input.Locale)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadLegalTargetLocaleState(
		ctx, tx, store, input.EntityType, input.EntityID, locale, true,
	)
	if err != nil {
		return contentblock.Result{}, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, errs.InvalidArgument("locale", "source locale is not a translation resource")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return contentblock.Result{}, &contentblock.StaleRevisionError{CurrentRevision: state.Snapshot.Document.Revision}
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil, input.OverwriteExistingTarget,
	); err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil {
		return contentblock.Result{
			DocumentRevision: state.Snapshot.Document.Revision,
		}, nil
	}
	documentID := *state.Root.ContentDocumentID
	domain, err := input.Fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	if domain.SourceLocale != state.SourceLocale {
		return contentblock.Result{}, errs.FailedPrecondition("legal source locale changed; reload before deleting target")
	}
	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: input.ExpectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), input.ContributorMemberIDs...),
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
	sort.Slice(group.Deletes, func(i, j int) bool {
		return group.Deletes[i].String() < group.Deletes[j].String()
	})
	if len(group.Deletes) != 0 {
		batch.LocaleGroups = []contentblock.LocaleMutationGroup{group}
	}
	authorizedFence := func(
		_ context.Context,
		_ *gorm.DB,
		requestedDocumentID uuid.UUID,
	) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("legal content document changed; reload before deleting target")
		}
		return domain, nil
	}
	return store.ApplyTargetLocaleBatchWithMetadata(
		ctx,
		tx,
		batch,
		locale,
		authorizedFence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Table(input.EntityType+"_translation").
				Where("entity_id = ? AND locale = ?", input.EntityID, locale).Delete(nil)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("legal target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{locale}}, nil
		},
	)
}
