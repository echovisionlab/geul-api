package post

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LoadPostBlockDocumentQuery loads one collaboration room for the session
// editor-collab admitted. A nil SessionID names no principal.
type LoadPostBlockDocumentQuery struct {
	PostID    string
	Locale    string
	SessionID *string
}

// ApplyPostBlockBatchCommand saves one collaboration room's Block batch.
type ApplyPostBlockBatchCommand struct {
	PostID                 string
	Locale                 string
	Batch                  *contentv1.RichTextBlockMutationBatch
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
	ExpectedTargetRevision *string
}

// UpdatePostLocaleMetadataCommand saves one room's title or summary.
type UpdatePostLocaleMetadataCommand struct {
	PostID                 string
	Locale                 string
	TitleChange            *intrav1.PostNullableStringChange
	SummaryChange          *intrav1.PostNullableStringChange
	ExpectedRevision       string
	ExpectedTargetRevision *string
	ContributorMemberIDs   []string
}

// UpdatePostDocumentMetadataCommand replaces the source room's Categories or
// Tags; a nil list is left alone.
type UpdatePostDocumentMetadataCommand struct {
	PostID               string
	Locale               string
	CategoryIDs          *[]string
	TagIDs               *[]string
	ExpectedRevision     string
	ContributorMemberIDs []string
}

// CreatePostVersionCheckpointCommand records the source room as a version
// when it differs from the latest one.
type CreatePostVersionCheckpointCommand struct {
	PostID               string
	Locale               string
	ExpectedRevision     string
	ContributorMemberIDs []string
}

func (s *Service) LoadPostBlockDocument(ctx context.Context, query LoadPostBlockDocumentQuery) (_ *intrav1.LoadPostBlockDocumentResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	var response *intrav1.LoadPostBlockDocumentResponse
	err := mutation.LockedRead(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		response, err = s.loadPostBlockDocumentWithDB(ctx, s.db, tx, query)
		return err
	})
	return response, err
}

// postRoomState is a collaboration room read under the Post root lock.
type postRoomState struct {
	snapshot       contentblock.Snapshot
	sourceLocale   string
	sourceMetadata postLocaleMetadataRow
	localeMetadata *postLocaleMetadataRow
	targetRevision *string
	categoryIDs    []string
	tagIDs         []string
	mediaItems     []*contentv1.ContentBlockMediaItem
}

// loadPostBlockDocumentWithDB reads the room in tx; lookup resolves its
// content document.
func (s *Service) loadPostBlockDocumentWithDB(ctx context.Context, lookup, tx *gorm.DB, query LoadPostBlockDocumentQuery) (*intrav1.LoadPostBlockDocumentResponse, error) {
	locale, err := validatePostAIDocumentIdentity(query.PostID, query.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, lookup, query.PostID)
	if err != nil {
		return nil, err
	}
	state, err := s.loadPostRoomState(ctx, tx, query, documentID, locale)
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	document, err := postTargetRoomDocument(state.snapshot, locale)
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.snapshot, locale)
	if err != nil {
		return nil, err
	}
	var localeMetadataMessage *intrav1.PostLocaleMetadata
	if state.localeMetadata != nil {
		localeMetadataMessage = &intrav1.PostLocaleMetadata{
			Locale: state.localeMetadata.Locale, Title: state.localeMetadata.Title, Summary: state.localeMetadata.Summary,
		}
	}
	return &intrav1.LoadPostBlockDocumentResponse{
		Document:         document,
		DocumentRevision: state.snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.PostLocaleMetadata{
			Locale: state.sourceLocale, Title: state.sourceMetadata.Title, Summary: state.sourceMetadata.Summary,
		},
		CategoryIds:         state.categoryIDs,
		TagIds:              state.tagIDs,
		BlockMedia:          state.mediaItems,
		Locale:              locale,
		LocaleExists:        state.localeMetadata != nil,
		LocaleMetadata:      localeMetadataMessage,
		TargetRevision:      state.targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}, nil
}

func (s *Service) loadPostRoomState(
	ctx context.Context,
	tx *gorm.DB,
	query LoadPostBlockDocumentQuery,
	documentID uuid.UUID,
	locale string,
) (postRoomState, error) {
	var state postRoomState
	principal, err := authorizePostBlockBootstrap(ctx, tx, s.spiceDB, query.PostID, documentID, query.SessionID)
	if err != nil {
		return state, err
	}
	domain, err := loadPostContentDomainContext(ctx, tx, query.PostID)
	if err != nil {
		return state, err
	}
	state.sourceLocale = domain.SourceLocale
	if state.snapshot, err = s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, state.sourceLocale); err != nil {
		return state, err
	}
	if state.sourceMetadata, err = loadPostLocaleMetadataRow(ctx, tx, query.PostID, state.sourceLocale); err != nil {
		return state, err
	}
	if err := loadPostRoomLocale(ctx, tx, query.PostID, locale, &state); err != nil {
		return state, err
	}
	if state.categoryIDs, state.tagIDs, err = loadPostTaxonomyIDs(ctx, tx, query.PostID); err != nil {
		return state, err
	}
	state.mediaItems, err = loadPostBlockMedia(ctx, tx, query.PostID, documentID, principal, s.mediaLoader, s.mediaHydrator)
	return state, err
}

// loadPostRoomLocale reads the room locale's metadata and target revision;
// a target locale without its row has none.
func loadPostRoomLocale(ctx context.Context, tx *gorm.DB, postID, locale string, state *postRoomState) error {
	if locale == state.sourceLocale {
		projected := state.sourceMetadata
		state.localeMetadata = &projected
		return nil
	}
	target, exists, err := loadOptionalPostLocaleMetadataRow(ctx, tx, postID, locale, false)
	if err != nil {
		return err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(state.snapshot, locale) {
			return domainerr.Precondition("Post target locale Blocks exist without owning metadata")
		}
		return nil
	}
	projected := postTargetMetadataProjection(state.sourceMetadata, &target, locale)
	state.localeMetadata = &projected
	revision, err := derivePostTargetRevision(state.snapshot.Document.Revision.String(), target)
	if err != nil {
		return err
	}
	state.targetRevision = &revision
	return nil
}

// postRoomSave is the outcome of one collaboration save: the source or
// target locale result of its room.
type postRoomSave struct {
	sourceLocale string
	source       contentblock.Result
	target       postTargetLocaleMutationResult
}

func (saved postRoomSave) changed() bool {
	return saved.source.Changed || saved.target.Changed
}

func (saved postRoomSave) documentRevision(locale string) string {
	if locale != saved.sourceLocale {
		return saved.target.DocumentRevision
	}
	return saved.source.DocumentRevision.String()
}

// collaborationSave runs a collaboration save of postID's room locale:
// editor-collab calls as itself, so the room is locked and its contributors'
// current permission authorizes the save. A changed room signals fields.
func (s *Service) collaborationSave(
	ctx context.Context,
	postID string,
	documentID uuid.UUID,
	locale string,
	contributors []string,
	fields []string,
	change func(context.Context, *gorm.DB, *postEffects, contentblock.DomainContext, contentblock.DomainFence) (postRoomSave, error),
) (postRoomSave, error) {
	var domain contentblock.DomainContext
	saved, _, err := runPostMutation(ctx, s, mutation.Spec[postRoomSave]{
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			domain, err = postCollaborationDocumentFence(s.spiceDB, postID, contributors)(ctx, tx, documentID)
			return err
		}),
	}, func(ctx context.Context, tx *gorm.DB, effects *postEffects) (postRoomSave, error) {
		saved, err := change(ctx, tx, effects, domain, contentblock.LockedFence(postEntityName, documentID, domain))
		saved.sourceLocale = domain.SourceLocale
		if err != nil || !saved.changed() {
			return saved, err
		}
		effects.signal(buildPostLocaleContentUpdatedEvent(contentsignal.LocaleChange{
			EntityID:             postID,
			Fields:               fields,
			DocumentRevision:     saved.documentRevision(locale),
			Contributors:         contributors,
			Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
			Locale:               locale,
			TargetRevision:       postTargetRevisionSignal(locale, domain.SourceLocale, saved.target.TargetRevision),
			DocumentStateChanged: locale == domain.SourceLocale,
		}))
		return saved, nil
	})
	if err != nil {
		return postRoomSave{}, normalizePostContentBlockError(err)
	}
	return saved, nil
}

// postTargetSaveEffects names the record and OG request of a target room
// save.
func postTargetSaveEffects(effects *postEffects, contributor, postID, locale string, target postTargetLocaleMutationResult, ogReason string) {
	if target.TitleChanged {
		effects.requestOG(postID, locale, false, ogReason)
	}
	if !target.Changed {
		return
	}
	operation := sharedtelemetry.AuditItemOperationUpdated
	if target.LocaleCreated {
		operation = sharedtelemetry.AuditItemOperationCreated
	}
	effects.audits = append(effects.audits, postLocaleContentAudit(contributor, postID, locale, operation))
}

// touchPost records a content change on the Post root.
func touchPost(ctx context.Context, tx *gorm.DB, postID string, now time.Time) error {
	return tx.WithContext(ctx).Model(&model.Post{}).Where("id = ?", postID).UpdateColumn("updated_at", now).Error
}

func (s *Service) ApplyPostBlockBatch(ctx context.Context, command ApplyPostBlockBatchCommand) (_ *intrav1.ApplyPostBlockBatchResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if command.Batch == nil {
		return nil, domainerr.Required("batch")
	}
	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(command.Batch.ContributorMemberIds)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, command.PostID)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		command.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return nil, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	expectedDocumentRevision, err := parsePostContentUUID("batch.expected_revision", command.Batch.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	now := dbtime.Now()
	saved, err := s.collaborationSave(ctx, command.PostID, documentID, locale, command.Batch.ContributorMemberIds, []string{"content"},
		func(ctx context.Context, tx *gorm.DB, effects *postEffects, domain contentblock.DomainContext, fence contentblock.DomainFence) (postRoomSave, error) {
			var saved postRoomSave
			var err error
			if locale != domain.SourceLocale {
				saved.target, err = applyPostTargetLocaleMutation(ctx, tx, s.contentBlocks, postTargetLocaleMutationInput{
					PostID: command.PostID, Locale: locale,
					ExpectedDocumentRevision: expectedDocumentRevision,
					ExpectedTargetRevision:   command.ExpectedTargetRevision,
					Storage:                  &storage,
					Now:                      now,
					Fence:                    fence,
				})
				if err == nil {
					postTargetSaveEffects(effects, contributorMemberID, command.PostID, locale, saved.target, "post_target_content_saved")
				}
				return saved, err
			}
			if command.ExpectedTargetRevision != nil {
				return saved, domainerr.InvalidField("expected_target_revision", "source Post room cannot carry a target revision")
			}
			if err := validatePostSourceStorage(storage, domain.SourceLocale); err != nil {
				return saved, err
			}
			batch, err := contentblock.BatchFromRichTextStorage(documentID, command.Batch.Profile, storage)
			if err != nil {
				return saved, err
			}
			if saved.source, err = s.contentBlocks.ApplyBatch(ctx, tx, batch, fence); err != nil || !saved.source.Changed {
				return saved, err
			}
			effects.audits = append(effects.audits, postLocaleContentAudit(contributorMemberID, command.PostID, locale, sharedtelemetry.AuditItemOperationUpdated))
			return saved, touchPost(ctx, tx, command.PostID, now)
		})
	if err != nil {
		return nil, err
	}
	response := &intrav1.ApplyPostBlockBatchResponse{
		DocumentRevision: saved.documentRevision(locale), Changed: saved.changed(),
		SourceChanged:  saved.source.TranslationSourceChanged,
		ChangedLocales: saved.source.ChangedLocales,
		Locale:         locale,
	}
	if locale != saved.sourceLocale {
		response.ChangedLocales = []string{locale}
		response.TargetRevision = &saved.target.TargetRevision
	}
	return response, nil
}

func (s *Service) UpdatePostLocaleMetadata(ctx context.Context, command UpdatePostLocaleMetadataCommand) (_ *intrav1.UpdatePostLocaleMetadataResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if command.TitleChange == nil && command.SummaryChange == nil {
		return nil, domainerr.New(domainerr.Invalid, "", "at least one typed locale metadata change is required")
	}
	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return nil, err
	}
	contributorMemberID, err := contentblock.MutationContributor(command.ContributorMemberIDs)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, command.PostID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	fields := make([]string, 0, 2)
	if command.TitleChange != nil {
		fields = append(fields, "title")
	}
	if command.SummaryChange != nil {
		fields = append(fields, "summary")
	}
	now := dbtime.Now()
	saved, err := s.collaborationSave(ctx, command.PostID, documentID, locale, command.ContributorMemberIDs, fields,
		func(ctx context.Context, tx *gorm.DB, effects *postEffects, domain contentblock.DomainContext, fence contentblock.DomainFence) (postRoomSave, error) {
			if locale != domain.SourceLocale {
				return s.savePostTargetLocaleMetadata(ctx, tx, effects, command, contributorMemberID, locale, expectedRevision, now, fence)
			}
			if command.ExpectedTargetRevision != nil {
				return postRoomSave{}, domainerr.InvalidField("expected_target_revision", "source Post room cannot carry a target revision")
			}
			return s.savePostSourceLocaleMetadata(ctx, tx, effects, command, contributorMemberID, domain.SourceLocale, documentID, expectedRevision, now, fence)
		})
	if err != nil {
		return nil, err
	}
	response := &intrav1.UpdatePostLocaleMetadataResponse{
		DocumentRevision: saved.documentRevision(locale), Changed: saved.changed(),
		SourceChanged: saved.source.TranslationSourceChanged,
		Locale:        locale,
	}
	if saved.changed() {
		response.ChangedLocales = []string{locale}
	}
	if locale != saved.sourceLocale {
		response.TargetRevision = &saved.target.TargetRevision
	}
	return response, nil
}

func (s *Service) savePostTargetLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	effects *postEffects,
	command UpdatePostLocaleMetadataCommand,
	contributor, locale string,
	expectedRevision uuid.UUID,
	now time.Time,
	fence contentblock.DomainFence,
) (postRoomSave, error) {
	title, titleSet, err := postOptionalMetadataChangeValue(command.TitleChange)
	if err != nil {
		return postRoomSave{}, domainerr.InvalidField("title_change", err.Error())
	}
	summary, summarySet, err := postOptionalMetadataChangeValue(command.SummaryChange)
	if err != nil {
		return postRoomSave{}, domainerr.InvalidField("summary_change", err.Error())
	}
	var saved postRoomSave
	saved.target, err = applyPostTargetLocaleMutation(ctx, tx, s.contentBlocks, postTargetLocaleMutationInput{
		PostID: command.PostID, Locale: locale,
		ExpectedDocumentRevision: expectedRevision,
		ExpectedTargetRevision:   command.ExpectedTargetRevision,
		SetTitle:                 titleSet,
		Title:                    title,
		SetSummary:               summarySet,
		Summary:                  summary,
		Now:                      now,
		Fence:                    fence,
	})
	if err == nil {
		postTargetSaveEffects(effects, contributor, command.PostID, locale, saved.target, "post_target_metadata_saved")
	}
	return saved, err
}

func (s *Service) savePostSourceLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	effects *postEffects,
	command UpdatePostLocaleMetadataCommand,
	contributor, sourceLocale string,
	documentID, expectedRevision uuid.UUID,
	now time.Time,
	fence contentblock.DomainFence,
) (postRoomSave, error) {
	var titleChanged bool
	advance, err := s.contentBlocks.AdvanceRevision(
		ctx, tx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			changed, changedTitle, err := updatePostLocaleMetadataRow(
				ctx, tx, command.PostID, sourceLocale, command.TitleChange, command.SummaryChange, sourceLocale, now,
			)
			titleChanged = changedTitle
			return contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: changed}, err
		},
	)
	saved := postRoomSave{source: contentblock.Result{
		DocumentRevision: advance.DocumentRevision, Changed: advance.Changed,
		TranslationSourceChanged: advance.TranslationSourceChanged,
	}}
	if err != nil || !advance.Changed {
		return saved, err
	}
	if err := touchPost(ctx, tx, command.PostID, now); err != nil {
		return saved, err
	}
	if titleChanged {
		effects.requestOG(command.PostID, sourceLocale, false, "post_source_metadata_saved")
	}
	effects.audits = append(effects.audits, postLocaleContentAudit(contributor, command.PostID, sourceLocale, sharedtelemetry.AuditItemOperationUpdated))
	return saved, nil
}

func (s *Service) UpdatePostDocumentMetadata(ctx context.Context, command UpdatePostDocumentMetadataCommand) (_ *intrav1.UpdatePostDocumentMetadataResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if command.CategoryIDs == nil && command.TagIDs == nil {
		return nil, domainerr.New(domainerr.Invalid, "", "at least one typed document metadata change is required")
	}
	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, command.PostID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var domain contentblock.DomainContext
	result, _, err := runPostMutation(ctx, s, mutation.Spec[contentblock.AdvanceResult]{
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			if domain, err = postCollaborationDocumentFence(s.spiceDB, command.PostID, command.ContributorMemberIDs)(ctx, tx, documentID); err != nil {
				return err
			}
			if locale != domain.SourceLocale {
				return domainerr.CollaborationRejection(
					intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_DOCUMENT_METADATA_FORBIDDEN,
					"Post target locale cannot mutate shared document metadata",
				)
			}
			return nil
		}),
	}, func(ctx context.Context, tx *gorm.DB, _ *postEffects) (contentblock.AdvanceResult, error) {
		result, err := s.contentBlocks.AdvanceRevision(
			ctx, tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			contentblock.LockedFence(postEntityName, documentID, domain),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				changed, err := replacePostDocumentTaxonomy(ctx, tx, command)
				return contentblock.MetadataEffect{Changed: changed}, err
			},
		)
		if err != nil || !result.Changed {
			return result, err
		}
		return result, touchPost(ctx, tx, command.PostID, now)
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	return &intrav1.UpdatePostDocumentMetadataResponse{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged: result.TranslationSourceChanged,
		Locale:        locale,
	}, nil
}

// replacePostDocumentTaxonomy replaces the Categories and Tags command
// names and reports whether either changed.
func replacePostDocumentTaxonomy(ctx context.Context, tx *gorm.DB, command UpdatePostDocumentMetadataCommand) (bool, error) {
	currentCategories, currentTags, err := loadPostTaxonomyIDs(ctx, tx, command.PostID)
	if err != nil {
		return false, err
	}
	changed := false
	for _, taxonomy := range []struct {
		requested *[]string
		current   []string
		table     string
		junction  string
		column    string
	}{
		{command.CategoryIDs, currentCategories, "category", "post_category", "category_id"},
		{command.TagIDs, currentTags, "tag", "post_tag", "tag_id"},
	} {
		if taxonomy.requested == nil {
			continue
		}
		next := sliceutil.UniqueTrimmed(*taxonomy.requested)
		slices.Sort(next)
		if slices.Equal(taxonomy.current, next) {
			continue
		}
		if err := replacePostTaxonomyLinks(ctx, tx, taxonomy.table, taxonomy.junction, taxonomy.column, command.PostID, next); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// postVersionCheckpoint is the outcome of a checkpoint request.
type postVersionCheckpoint struct {
	created *model.PostVersion
}

func (s *Service) CreatePostVersionCheckpoint(ctx context.Context, command CreatePostVersionCheckpointCommand) (_ *intrav1.CreatePostVersionCheckpointResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPostContentDocumentID(ctx, s.db, command.PostID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}
	var domain contentblock.DomainContext
	checkpoint, err := mutation.Run(ctx, s.mutations, mutation.Spec[postVersionCheckpoint]{
		Access: mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
			var err error
			domain, err = postCollaborationDocumentFence(s.spiceDB, command.PostID, command.ContributorMemberIDs)(ctx, tx, documentID)
			return err
		}),
		Change: func(ctx context.Context, tx *gorm.DB) (postVersionCheckpoint, error) {
			created, err := s.createPostVersion(ctx, tx, command, documentID, domain.SourceLocale, locale, expectedRevision)
			return postVersionCheckpoint{created: created}, err
		},
		Audit: func(checkpoint postVersionCheckpoint) mutation.Audit {
			if checkpoint.created == nil {
				return mutation.Unchanged()
			}
			created := checkpoint.created
			return mutation.VersionAudit(sharedtelemetry.AuditPostUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPostVersionCreatedAuditRecord(metadata, command.PostID, created.ID, created.ContributorMemberIDs)
			})
		},
	})
	if err != nil {
		return nil, normalizePostContentBlockError(err)
	}
	response := &intrav1.CreatePostVersionCheckpointResponse{
		Created: checkpoint.created != nil, Revision: expectedRevision.String(), Locale: locale,
	}
	if checkpoint.created != nil {
		response.VersionId = &checkpoint.created.ID
	}
	return response, nil
}

// createPostVersion records the source room as the next version, or returns
// nil for a target room or content equal to the latest version.
func (s *Service) createPostVersion(
	ctx context.Context,
	tx *gorm.DB,
	command CreatePostVersionCheckpointCommand,
	documentID uuid.UUID,
	sourceLocale, locale string,
	expectedRevision uuid.UUID,
) (*model.PostVersion, error) {
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
	if err != nil {
		return nil, err
	}
	if snapshot.Document.Revision != expectedRevision {
		return nil, &contentblock.StaleRevisionError{CurrentRevision: snapshot.Document.Revision}
	}
	if locale != sourceLocale {
		return nil, nil
	}
	metadata, err := loadPostLocaleMetadataRow(ctx, tx, command.PostID, sourceLocale)
	if err != nil {
		return nil, err
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, sourceLocale)
	if err != nil {
		return nil, err
	}
	encoded, _, err := marshalPostVersionContentSnapshot(document, sourceLocale, metadata.Title, metadata.Summary)
	if err != nil {
		return nil, err
	}
	var last model.PostVersion
	lastResult := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("post_id = ?", command.PostID).
		Order("version DESC").
		First(&last)
	if lastResult.Error == nil && bytes.Equal(last.ContentSnapshot, encoded) {
		return nil, nil
	}
	if lastResult.Error != nil && !errors.Is(lastResult.Error, gorm.ErrRecordNotFound) {
		return nil, lastResult.Error
	}
	nextVersion := int32(1)
	if lastResult.Error == nil {
		nextVersion = last.Version + 1
	}
	version := &model.PostVersion{
		PostID: command.PostID, Version: nextVersion, ContentSnapshot: encoded,
		ContributorMemberIDs: normalizeContributorMemberIDs(command.ContributorMemberIDs),
	}
	if err := tx.WithContext(ctx).Create(version).Error; err != nil {
		return nil, err
	}
	return version, nil
}

type postLocaleMetadataRow struct {
	Locale    string    `gorm:"column:locale"`
	Title     *string   `gorm:"column:title"`
	Summary   *string   `gorm:"column:summary"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func loadPostLocaleMetadataRow(
	ctx context.Context,
	db *gorm.DB,
	postID string,
	locale string,
) (postLocaleMetadataRow, error) {
	var row postLocaleMetadataRow
	if err := db.WithContext(ctx).
		Table("post_translation").
		Select("locale", "title", "summary", "updated_at").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return postLocaleMetadataRow{}, domainerr.ResourceNotFound("post_translation", postID+":"+locale)
		}
		return postLocaleMetadataRow{}, err
	}
	return row, nil
}

func loadPostTaxonomyIDs(
	ctx context.Context,
	db *gorm.DB,
	postID string,
) ([]string, []string, error) {
	var row struct {
		CategoryIDs pq.StringArray `gorm:"column:category_ids;type:text[]"`
		TagIDs      pq.StringArray `gorm:"column:tag_ids;type:text[]"`
	}
	result := db.WithContext(ctx).Raw(
		`SELECT ARRAY(
		          SELECT post_category.category_id::text
		          FROM post_category
		          WHERE post_category.post_id = ?::uuid
		          ORDER BY post_category.category_id
		        ) AS category_ids,
		        ARRAY(
		          SELECT post_tag.tag_id::text
		          FROM post_tag
		          WHERE post_tag.post_id = ?::uuid
		          ORDER BY post_tag.tag_id
		        ) AS tag_ids`,
		postID, postID,
	).Scan(&row)
	if result.Error != nil {
		return nil, nil, result.Error
	}
	return []string(row.CategoryIDs), []string(row.TagIDs), nil
}

func replacePostTaxonomyLinks(
	ctx context.Context,
	tx *gorm.DB,
	targetTable string,
	junctionTable string,
	targetColumn string,
	postID string,
	rawIDs []string,
) error {
	ids := sliceutil.UniqueTrimmed(rawIDs)
	slices.Sort(ids)
	if len(ids) > 0 {
		var count int64
		if err := tx.WithContext(ctx).Table(targetTable).Where("id IN ?", ids).Count(&count).Error; err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return domainerr.New(domainerr.Invalid, "", "one or more "+targetTable+" IDs do not exist")
		}
	}
	if err := tx.WithContext(ctx).Table(junctionTable).
		Where("post_id = ?", postID).
		Delete(nil).Error; err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	values := make([]structured.Fields, len(ids))
	for i, id := range ids {
		values[i] = structured.Fields{"post_id": postID, targetColumn: id}
	}
	return tx.WithContext(ctx).Table(junctionTable).Create(values).Error
}

func updatePostLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	locale string,
	titleChange *intrav1.PostNullableStringChange,
	summaryChange *intrav1.PostNullableStringChange,
	sourceLocale string,
	now time.Time,
) (bool, bool, error) {
	if strings.TrimSpace(sourceLocale) == "" {
		return false, false, domainerr.Precondition("Post source locale is not initialized")
	}
	if locale != sourceLocale {
		return false, false, domainerr.Precondition("target Post locale metadata is read-only")
	}
	if err := ensurePostLocaleMetadataRow(ctx, tx, postID, locale, sourceLocale); err != nil {
		return false, false, err
	}
	current, err := loadPostLocaleMetadataRow(
		ctx, tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}), postID, locale,
	)
	if err != nil {
		return false, false, err
	}
	updates := structured.Fields{}
	titleChanged := false
	if titleChange != nil {
		next, changeErr := postNullableStringChangeValue(titleChange)
		if changeErr != nil {
			return false, false, domainerr.InvalidField("title_change", changeErr.Error())
		}
		if next != nil {
			trimmed := strings.TrimSpace(*next)
			if trimmed == "" {
				return false, false, domainerr.InvalidField("title_change", "title must not be empty")
			}
			next = &trimmed
		} else if locale == sourceLocale {
			return false, false, domainerr.InvalidField("title_change", "source title cannot be cleared")
		}
		if !nullableStringEqual(current.Title, next) {
			updates["title"] = next
			titleChanged = true
		}
	}
	if summaryChange != nil {
		next, changeErr := postNullableStringChangeValue(summaryChange)
		if changeErr != nil {
			return false, false, domainerr.InvalidField("summary_change", changeErr.Error())
		}
		if !nullableStringEqual(current.Summary, next) {
			updates["summary"] = next
		}
	}
	if len(updates) == 0 {
		return false, false, nil
	}
	updates["updated_at"] = now
	if err := tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Updates(updates).Error; err != nil {
		return false, false, err
	}
	return true, titleChanged, nil
}

func ensurePostLocaleMetadataRow(
	ctx context.Context,
	tx *gorm.DB,
	postID string,
	locale string,
	sourceLocale string,
) error {
	if locale != sourceLocale {
		return domainerr.Precondition("target Post locale metadata is read-only")
	}
	var count int64
	if err := tx.WithContext(ctx).Table("post_translation").
		Where("entity_id = ? AND locale = ?", postID, locale).
		Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return domainerr.Precondition("Post source locale metadata is not initialized")
	}
	return nil
}

func postNullableStringChangeValue(change *intrav1.PostNullableStringChange) (*string, error) {
	if change == nil {
		return nil, errors.New("change is required")
	}
	switch value := change.Value.(type) {
	case *intrav1.PostNullableStringChange_SetValue:
		return &value.SetValue, nil
	case *intrav1.PostNullableStringChange_Clear:
		if !value.Clear {
			return nil, errors.New("clear must be true")
		}
		return nil, nil
	default:
		return nil, errors.New("set or clear is required")
	}
}

func postOptionalMetadataChangeValue(
	change *intrav1.PostNullableStringChange,
) (*string, bool, error) {
	if change == nil {
		return nil, false, nil
	}
	value, err := postNullableStringChangeValue(change)
	return value, true, err
}
