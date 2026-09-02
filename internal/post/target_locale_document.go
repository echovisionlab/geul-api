package post

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type postTargetLocaleMutationInput struct {
	PostID                   string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	Storage                  *contentv1.ContentStorageMutationBatch
	Batch                    *contentblock.Batch
	AllowLocaleValueDeletes  bool
	AllowCreate              bool
	SeedSourceOnCreate       bool
	UseCurrentTargetRevision bool
	SetTitle                 bool
	Title                    *string
	SetSummary               bool
	Summary                  *string
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type postTargetLocaleMutationResult struct {
	DocumentRevision string
	TargetRevision   string
	Changed          bool
	LocaleCreated    bool
	ContentChanged   bool
	MetadataChanged  bool
	TitleChanged     bool
}

type postTargetLocaleDeleteInput struct {
	PostID                   string
	Locale                   string
	ExpectedDocumentRevision uuid.UUID
	ExpectedTargetRevision   *string
	ContributorMemberIDs     []uuid.UUID
	Now                      time.Time
	Fence                    contentblock.DomainFence
}

type postTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata postLocaleMetadataRow
	TargetMetadata *postLocaleMetadataRow
	TargetRevision string
}

func loadPostTargetLocaleStateForDocument(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	postID string,
	documentID uuid.UUID,
	locale string,
	documentLock string,
) (postTargetLocaleState, error) {
	if tx == nil || store == nil || documentID == uuid.Nil {
		return postTargetLocaleState{}, errs.InternalMsg("Post target locale dependencies are required")
	}
	normalizedLocale, err := validatePostAIDocumentIdentity(postID, locale)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	domain, err := loadPostContentDomainContext(ctx, tx, postID)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	if normalizedLocale == domain.SourceLocale {
		return postTargetLocaleState{}, errs.InvalidArgument("locale", "Post target locale must differ from the source locale")
	}

	if documentLock != "" {
		var document contentblock.Document
		if err := tx.WithContext(ctx).Table("content_document").
			Clauses(clause.Locking{Strength: documentLock}).
			Where("id = ?", documentID).
			Take(&document).Error; err != nil {
			return postTargetLocaleState{}, normalizePostContentBlockError(err)
		}
	}
	snapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return postTargetLocaleState{}, normalizePostContentBlockError(err)
	}
	if snapshot.Document.Profile != postContentDocumentProfile {
		return postTargetLocaleState{}, errs.FailedPrecondition("Post target locale requires the Post content profile")
	}
	sourceMetadata, err := loadPostLocaleMetadataRow(ctx, tx, postID, domain.SourceLocale)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	targetMetadata, exists, err := loadOptionalPostLocaleMetadataRow(
		ctx, tx, postID, normalizedLocale, documentLock != "",
	)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	if !exists && snapshotContainsLocale(snapshot, normalizedLocale) {
		return postTargetLocaleState{}, errs.FailedPrecondition("Post target locale Blocks exist without owning metadata")
	}
	state := postTargetLocaleState{
		Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: sourceMetadata,
	}
	if exists {
		state.TargetMetadata = &targetMetadata
		state.TargetRevision, err = derivePostTargetRevision(snapshot.Document.Revision.String(), targetMetadata)
		if err != nil {
			return postTargetLocaleState{}, err
		}
	}
	return state, nil
}

func loadOptionalPostLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	locale string,
	forUpdate bool,
) (postLocaleMetadataRow, bool, error) {
	query := tx.WithContext(ctx).Table("post_translation").
		Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", postID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var row postLocaleMetadataRow
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return postLocaleMetadataRow{}, false, nil
		}
		return postLocaleMetadataRow{}, false, errs.Internal(err)
	}
	return row, true, nil
}

func derivePostTargetRevision(documentRevision string, metadata postLocaleMetadataRow) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", errs.Internal(err)
	}
	return revision, nil
}

func postTargetRoomDocument(
	snapshot contentblock.Snapshot,
	locale string,
) (*contentv1.LocalizedRichTextDocument, error) {
	localized, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	return localized, nil
}

func postTargetMetadataProjection(
	source postLocaleMetadataRow,
	target *postLocaleMetadataRow,
	locale string,
) postLocaleMetadataRow {
	projected := postLocaleMetadataRow{Locale: locale, Title: source.Title, Summary: source.Summary}
	if target == nil {
		return projected
	}
	if target.Title != nil {
		projected.Title = target.Title
	}
	if target.Summary != nil {
		projected.Summary = target.Summary
	}
	projected.UpdatedAt = target.UpdatedAt
	return projected
}

func applyPostTargetLocaleMutation(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input postTargetLocaleMutationInput,
) (postTargetLocaleMutationResult, error) {
	if input.ExpectedDocumentRevision == uuid.Nil || strings.TrimSpace(input.PostID) == "" ||
		strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return postTargetLocaleMutationResult{}, errs.InvalidArgument("target", "Post target identity, revision, and fence are required")
	}
	documentID, err := loadPostContentDocumentID(ctx, tx, input.PostID)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	domain, err := input.Fence(ctx, tx, documentID)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	state, err := loadPostTargetLocaleStateForDocument(
		ctx, tx, store, input.PostID, documentID, input.Locale, "UPDATE",
	)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	if state.SourceLocale != domain.SourceLocale {
		return postTargetLocaleMutationResult{}, errs.FailedPrecondition("Post source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return postTargetLocaleMutationResult{}, contentblockStaleRevision(state.Snapshot.Document.Revision)
	}
	if err := translation.ValidateTargetRevisionWrite(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil,
		input.UseCurrentTargetRevision,
	); err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	if state.TargetMetadata == nil && !input.AllowCreate {
		return postTargetLocaleMutationResult{}, errs.FailedPrecondition(
			"Post target locale must be explicitly created before collaboration",
		)
	}
	if input.Storage != nil && input.Batch != nil {
		return postTargetLocaleMutationResult{}, errs.InvalidArgument("batch", "Post target accepts one mutation representation")
	}
	if err := validatePostTargetStorage(
		input.Storage,
		input.ExpectedDocumentRevision,
		input.Locale,
		input.AllowLocaleValueDeletes,
	); err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	var batch contentblock.Batch
	if input.Batch != nil {
		batch = contentblock.CloneBatch(*input.Batch)
		if err := validatePostTargetBatch(
			batch,
			documentID,
			input.ExpectedDocumentRevision,
			input.Locale,
			input.AllowLocaleValueDeletes,
		); err != nil {
			return postTargetLocaleMutationResult{}, err
		}
		if state.TargetMetadata == nil && input.SeedSourceOnCreate {
			batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
			if err != nil {
				return postTargetLocaleMutationResult{}, err
			}
		}
	} else {
		storage := contentv1.ContentStorageMutationBatch{
			ExpectedRevision: input.ExpectedDocumentRevision.String(),
		}
		if input.Storage != nil {
			storage = *input.Storage
		}
		batch, err = contentblock.BatchFromRichTextStorage(
			documentID, contentv1.RichTextProfile_RICH_TEXT_PROFILE_POST, storage,
		)
		if err != nil {
			return postTargetLocaleMutationResult{}, normalizePostContentBlockError(err)
		}
		if state.TargetMetadata == nil && input.SeedSourceOnCreate {
			batch, err = contentblock.SeedTargetLocaleBatch(batch, state.Snapshot, state.SourceLocale, input.Locale)
			if err != nil {
				return postTargetLocaleMutationResult{}, err
			}
		}
	}

	now := input.Now.UTC().Truncate(time.Microsecond)
	if now.IsZero() {
		now = time.Now().UTC().Truncate(time.Microsecond)
	}
	result := postTargetLocaleMutationResult{
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		LocaleCreated:    state.TargetMetadata == nil,
	}
	var finalMetadata postLocaleMetadataRow
	authorizedFence := func(
		_ context.Context,
		_ *gorm.DB,
		requestedDocumentID uuid.UUID,
	) (contentblock.DomainContext, error) {
		if requestedDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Post content document changed; reload before saving")
		}
		return domain, nil
	}
	storeResult, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx,
		tx,
		batch,
		input.Locale,
		authorizedFence,
		func(ctx context.Context, tx *gorm.DB, contentChanged bool) (contentblock.MetadataEffect, error) {
			current := postLocaleMetadataRow{Locale: input.Locale}
			if state.TargetMetadata != nil {
				current = *state.TargetMetadata
			} else if input.SeedSourceOnCreate {
				current = state.SourceMetadata
			}
			title := current.Title
			summary := current.Summary
			if input.SetTitle {
				result.TitleChanged = !nullableStringEqual(title, input.Title)
				title = input.Title
			}
			metadataValueChanged := result.TitleChanged
			if input.SetSummary {
				metadataValueChanged = metadataValueChanged || !nullableStringEqual(summary, input.Summary)
				summary = input.Summary
			}
			if state.TargetMetadata == nil {
				finalMetadata = postLocaleMetadataRow{
					Locale: input.Locale, Title: title, Summary: summary, UpdatedAt: now,
				}
				insert := tx.WithContext(ctx).Exec(
					`INSERT INTO post_translation (
						entity_id, locale, title, summary, created_at, updated_at
					 ) VALUES (?::uuid, ?, ?, ?, ?, ?)`,
					input.PostID, input.Locale, title, summary, now, now,
				)
				if insert.Error != nil {
					return contentblock.MetadataEffect{}, errs.Internal(insert.Error)
				}
				if insert.RowsAffected != 1 {
					return contentblock.MetadataEffect{}, errs.InternalMsg("Post target locale could not be created")
				}
				return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
			}
			if !contentChanged && !metadataValueChanged {
				finalMetadata = current
				return contentblock.MetadataEffect{}, nil
			}
			updatedAt := translation.NextTargetUpdatedAt(now, current.UpdatedAt)
			update := tx.WithContext(ctx).Table("post_translation").
				Where("entity_id = ?::uuid AND locale = ?", input.PostID, input.Locale).
				Updates(map[string]any{"title": title, "summary": summary, "updated_at": updatedAt})
			if update.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(update.Error)
			}
			if update.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Post target locale disappeared while saving")
			}
			finalMetadata = postLocaleMetadataRow{
				Locale: input.Locale, Title: title, Summary: summary, UpdatedAt: updatedAt,
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		},
	)
	if err != nil {
		return postTargetLocaleMutationResult{}, normalizePostContentBlockError(err)
	}
	result.Changed = storeResult.Changed
	result.ContentChanged = storeResult.ContentChanged
	result.MetadataChanged = storeResult.MetadataChanged
	if result.Changed {
		if err := tx.WithContext(ctx).Model(&model.Post{}).
			Where("id = ?", input.PostID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return postTargetLocaleMutationResult{}, errs.Internal(err)
		}
	}
	result.TargetRevision, err = derivePostTargetRevision(result.DocumentRevision, finalMetadata)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	return result, nil
}

func deletePostTargetLocale(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	input postTargetLocaleDeleteInput,
) (contentblock.Result, error) {
	if input.ExpectedDocumentRevision == uuid.Nil || strings.TrimSpace(input.PostID) == "" ||
		strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return contentblock.Result{}, errs.InvalidArgument("target", "Post target delete identity, revision, and fence are required")
	}
	documentID, err := loadPostContentDocumentID(ctx, tx, input.PostID)
	if err != nil {
		return contentblock.Result{}, err
	}
	domain, err := input.Fence(ctx, tx, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadPostTargetLocaleStateForDocument(
		ctx, tx, store, input.PostID, documentID, input.Locale, "UPDATE",
	)
	if err != nil {
		return contentblock.Result{}, err
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, errs.FailedPrecondition("Post source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return contentblock.Result{}, contentblockStaleRevision(state.Snapshot.Document.Revision)
	}
	if err := translation.ValidateExpectedTargetRevision(
		input.ExpectedTargetRevision, state.TargetRevision, state.TargetMetadata != nil,
	); err != nil {
		return contentblock.Result{}, err
	}
	if state.TargetMetadata == nil {
		return contentblock.Result{
			DocumentRevision: state.Snapshot.Document.Revision,
		}, nil
	}

	batch := contentblock.Batch{
		DocumentID: documentID, ExpectedRevision: input.ExpectedDocumentRevision,
		ContributorMemberIDs: append([]uuid.UUID(nil), input.ContributorMemberIDs...),
	}
	group := contentblock.LocaleMutationGroup{Locale: input.Locale}
	for _, overlay := range state.Snapshot.LocaleOverlays {
		if overlay.Locale != input.Locale {
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
			return contentblock.DomainContext{}, errs.FailedPrecondition("Post content document changed; reload before saving")
		}
		return domain, nil
	}
	result, err := store.ApplyTargetLocaleBatchWithMetadata(
		ctx,
		tx,
		batch,
		input.Locale,
		authorizedFence,
		func(ctx context.Context, tx *gorm.DB, _ bool) (contentblock.MetadataEffect, error) {
			deleted := tx.WithContext(ctx).Exec(
				"DELETE FROM post_translation WHERE entity_id = ?::uuid AND locale = ?",
				input.PostID, input.Locale,
			)
			if deleted.Error != nil {
				return contentblock.MetadataEffect{}, errs.Internal(deleted.Error)
			}
			if deleted.RowsAffected != 1 {
				return contentblock.MetadataEffect{}, errs.InternalMsg("Post target locale disappeared while deleting")
			}
			return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{input.Locale}}, nil
		},
	)
	if err != nil {
		return contentblock.Result{}, normalizePostContentBlockError(err)
	}
	if result.Changed {
		now := input.Now.UTC()
		if now.IsZero() {
			now = time.Now().UTC()
		}
		if err := tx.WithContext(ctx).Model(&model.Post{}).
			Where("id = ?", input.PostID).
			UpdateColumn("updated_at", now).Error; err != nil {
			return contentblock.Result{}, errs.Internal(err)
		}
	}
	return result, nil
}

func validatePostTargetStorage(
	storage *contentv1.ContentStorageMutationBatch,
	expectedDocumentRevision uuid.UUID,
	locale string,
	allowLocaleValueDeletes bool,
) error {
	if storage == nil {
		return nil
	}
	if storage.ExpectedRevision != expectedDocumentRevision.String() {
		return errs.InvalidArgument("batch.expected_revision", "must match the observed shared Post document revision")
	}
	if len(storage.BaseUpserts) != 0 || len(storage.Deletes) != 0 || len(storage.Moves) != 0 {
		return errs.CollaborationMutationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Post target locale cannot mutate the shared Block graph",
		)
	}
	if len(storage.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Post target locale contains multiple locale groups")
	}
	for _, group := range storage.LocaleGroups {
		if group.Locale != locale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Post target locale mutation must match the authenticated room locale",
			)
		}
		if !allowLocaleValueDeletes && len(group.Deletes) != 0 {
			return errs.InvalidArgument(
				"batch",
				"Post target locale values cannot be cleared; write an explicit empty value or delete the translation",
			)
		}
	}
	return nil
}

func validatePostTargetBatch(
	batch contentblock.Batch,
	documentID uuid.UUID,
	expectedDocumentRevision uuid.UUID,
	locale string,
	allowLocaleValueDeletes bool,
) error {
	if batch.DocumentID != documentID || batch.ExpectedRevision != expectedDocumentRevision {
		return errs.InvalidArgument("batch", "Post target document identity and revision must match the locked state")
	}
	if len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 {
		return errs.InvalidArgument("batch", "Post target locale cannot mutate the shared Block graph")
	}
	if len(batch.LocaleGroups) > 1 {
		return errs.InvalidArgument("batch", "Post target locale contains multiple locale groups")
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return errs.InvalidArgument("batch", "Post target locale mutation must match the authorized locale")
		}
		if !allowLocaleValueDeletes && len(group.Deletes) != 0 {
			return errs.InvalidArgument(
				"batch",
				"Post target locale values cannot be cleared; write an explicit empty value or delete the translation",
			)
		}
	}
	return nil
}

func validatePostSourceStorage(
	storage contentv1.ContentStorageMutationBatch,
	sourceLocale string,
) error {
	for _, group := range storage.LocaleGroups {
		if group.Locale != sourceLocale {
			return errs.CollaborationMutationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Post source room mutation must match the current source locale",
			)
		}
	}
	return nil
}

func contentblockStaleRevision(current uuid.UUID) error {
	return &contentblock.StaleRevisionError{CurrentRevision: current}
}
