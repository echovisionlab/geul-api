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

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
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

// StringChange sets or clears a nullable string. An empty StringChange,
// or one whose Clear is false, is invalid.
type StringChange struct {
	Set   *string
	Clear *bool
}

// UpdatePostLocaleMetadataCommand saves one room's title or summary.
type UpdatePostLocaleMetadataCommand struct {
	PostID                 string
	Locale                 string
	TitleChange            *StringChange
	SummaryChange          *StringChange
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

// RoomDocument is one collaboration room as editor-collab loads it.
type RoomDocument struct {
	Document            *contentv1.LocalizedRichTextDocument
	DocumentRevision    string
	SourceMetadata      LocaleMetadata
	CategoryIDs         []string
	TagIDs              []string
	BlockMedia          []*contentv1.ContentBlockMediaItem
	Locale              string
	LocaleExists        bool
	LocaleMetadata      *LocaleMetadata
	TargetRevision      *string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
}

// RoomSave is the outcome of one collaboration save.
type RoomSave struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	Locale           string
	TargetRevision   *string
}

// Checkpoint is the outcome of a version checkpoint request.
type Checkpoint struct {
	Created   bool
	Revision  string
	Locale    string
	VersionID *string
}

func (s *Service) LoadPostBlockDocument(ctx context.Context, query LoadPostBlockDocumentQuery) (_ RoomDocument, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	// The content document is resolved on its own before the room's
	// snapshot opens, so a load holds one connection at a time.
	if _, err := validatePostAIDocumentIdentity(query.PostID, query.Locale); err != nil {
		return RoomDocument{}, err
	}
	documentID, err := s.contentDocumentID(ctx, query.PostID)
	if err != nil {
		return RoomDocument{}, err
	}
	var room RoomDocument
	err = s.units.ReadLocked(ctx, func(store Store) error {
		var err error
		room, err = s.loadPostBlockDocument(ctx, func(string) (uuid.UUID, error) { return documentID, nil }, store, query)
		return err
	})
	return room, err
}

// postRoomState is a collaboration room read under the Post root lock.
type postRoomState struct {
	snapshot       contentblock.Snapshot
	sourceLocale   string
	sourceMetadata LocaleMetadata
	localeMetadata *LocaleMetadata
	targetRevision *string
	categoryIDs    []string
	tagIDs         []string
	mediaItems     []*contentv1.ContentBlockMediaItem
}

// loadPostBlockDocument reads the room in store; documentOf resolves its
// content document.
func (s *Service) loadPostBlockDocument(ctx context.Context, documentOf func(postID string) (uuid.UUID, error), store Store, query LoadPostBlockDocumentQuery) (RoomDocument, error) {
	locale, err := validatePostAIDocumentIdentity(query.PostID, query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	documentID, err := documentOf(query.PostID)
	if err != nil {
		return RoomDocument{}, err
	}
	state, err := s.loadPostRoomState(ctx, store, query, documentID, locale)
	if err != nil {
		return RoomDocument{}, normalizePostContentBlockError(err)
	}
	document, err := postTargetRoomDocument(state.snapshot, locale)
	if err != nil {
		return RoomDocument{}, normalizePostContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.snapshot, locale)
	if err != nil {
		return RoomDocument{}, err
	}
	var localeMetadata *LocaleMetadata
	if state.localeMetadata != nil {
		localeMetadata = &LocaleMetadata{Locale: state.localeMetadata.Locale, Title: state.localeMetadata.Title, Summary: state.localeMetadata.Summary}
	}
	return RoomDocument{
		Document:         document,
		DocumentRevision: state.snapshot.Document.Revision.String(),
		SourceMetadata: LocaleMetadata{
			Locale: state.sourceLocale, Title: state.sourceMetadata.Title, Summary: state.sourceMetadata.Summary,
		},
		CategoryIDs:         state.categoryIDs,
		TagIDs:              state.tagIDs,
		BlockMedia:          state.mediaItems,
		Locale:              locale,
		LocaleExists:        state.localeMetadata != nil,
		LocaleMetadata:      localeMetadata,
		TargetRevision:      state.targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}, nil
}

func (s *Service) loadPostRoomState(
	ctx context.Context,
	store Store,
	query LoadPostBlockDocumentQuery,
	documentID uuid.UUID,
	locale string,
) (postRoomState, error) {
	var state postRoomState
	principal, err := authorizePostBlockBootstrap(ctx, store, s.spiceDB, query.PostID, documentID, query.SessionID)
	if err != nil {
		return state, err
	}
	domain, err := loadPostContentDomainContext(ctx, store, query.PostID)
	if err != nil {
		return state, err
	}
	state.sourceLocale = domain.SourceLocale
	if state.snapshot, err = store.LockedContentSnapshot(ctx, documentID, state.sourceLocale); err != nil {
		return state, err
	}
	if state.sourceMetadata, err = loadPostLocaleMetadataRow(ctx, store, query.PostID, state.sourceLocale, false); err != nil {
		return state, err
	}
	if err := loadPostRoomLocale(ctx, store, query.PostID, locale, &state); err != nil {
		return state, err
	}
	if state.categoryIDs, state.tagIDs, err = store.TaxonomyIDs(ctx, query.PostID); err != nil {
		return state, err
	}
	state.mediaItems, err = store.BlockMedia(ctx, query.PostID, documentID, principal)
	return state, err
}

// loadPostRoomLocale reads the room locale's metadata and target revision;
// a target locale without its row has none.
func loadPostRoomLocale(ctx context.Context, store Store, postID, locale string, state *postRoomState) error {
	if locale == state.sourceLocale {
		projected := state.sourceMetadata
		state.localeMetadata = &projected
		return nil
	}
	target, exists, err := store.OptionalLocaleMetadata(ctx, postID, locale, false)
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
	change func(context.Context, Store, *postEffects, contentblock.DomainContext, ContentFence) (postRoomSave, error),
) (postRoomSave, error) {
	var domain contentblock.DomainContext
	saved, _, err := runPostMutation(ctx, s, mutation.StoreSpec[postRoomSave, Store]{
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			domain, err = postCollaborationDocumentFence(s.spiceDB, postID, contributors)(ctx, store, documentID)
			return err
		}),
	}, func(ctx context.Context, store Store, effects *postEffects) (postRoomSave, error) {
		saved, err := change(ctx, store, effects, domain, lockedContentFence(documentID, domain))
		saved.sourceLocale = domain.SourceLocale
		if err != nil || !saved.changed() {
			return saved, err
		}
		effects.signal(ContentUpdate{
			PostID:               postID,
			Fields:               fields,
			DocumentRevision:     saved.documentRevision(locale),
			Contributors:         contributors,
			Source:               SourceCollaboration,
			Locale:               locale,
			TargetRevision:       postTargetRevisionSignal(locale, domain.SourceLocale, saved.target.TargetRevision),
			DocumentStateChanged: locale == domain.SourceLocale,
		})
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

func (s *Service) ApplyPostBlockBatch(ctx context.Context, command ApplyPostBlockBatchCommand) (_ RoomSave, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if command.Batch == nil {
		return RoomSave{}, domainerr.Required("batch")
	}
	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	contributorMemberID, err := contentblock.MutationContributor(command.Batch.ContributorMemberIds)
	if err != nil {
		return RoomSave{}, err
	}
	documentID, err := s.contentDocumentID(ctx, command.PostID)
	if err != nil {
		return RoomSave{}, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		command.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return RoomSave{}, domainerr.InvalidField("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		command.Batch.Profile, locale, &storage, command.AffectedLocaleValues,
	); err != nil {
		return RoomSave{}, domainerr.InvalidField("affected_locale_values", err.Error())
	}
	expectedDocumentRevision, err := parsePostContentUUID("batch.expected_revision", command.Batch.ExpectedRevision)
	if err != nil {
		return RoomSave{}, err
	}
	now := dbtime.Now()
	saved, err := s.collaborationSave(ctx, command.PostID, documentID, locale, command.Batch.ContributorMemberIds, []string{"content"},
		func(ctx context.Context, store Store, effects *postEffects, domain contentblock.DomainContext, fence ContentFence) (postRoomSave, error) {
			var saved postRoomSave
			var err error
			if locale != domain.SourceLocale {
				saved.target, err = applyPostTargetLocaleMutation(ctx, store, postTargetLocaleMutationInput{
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
			if saved.source, err = store.ApplyContentBatch(ctx, batch, fence); err != nil || !saved.source.Changed {
				return saved, err
			}
			effects.audits = append(effects.audits, postLocaleContentAudit(contributorMemberID, command.PostID, locale, sharedtelemetry.AuditItemOperationUpdated))
			return saved, store.TouchPost(ctx, command.PostID, now)
		})
	if err != nil {
		return RoomSave{}, err
	}
	result := RoomSave{
		DocumentRevision: saved.documentRevision(locale), Changed: saved.changed(),
		SourceChanged:  saved.source.TranslationSourceChanged,
		ChangedLocales: saved.source.ChangedLocales,
		Locale:         locale,
	}
	if locale != saved.sourceLocale {
		result.ChangedLocales = []string{locale}
		result.TargetRevision = &saved.target.TargetRevision
	}
	return result, nil
}

// contentDocumentID resolves the Post's content document in a snapshot of
// its own, before the save that writes it.
func (s *Service) contentDocumentID(ctx context.Context, postID string) (uuid.UUID, error) {
	return readStore(ctx, s, func(store Store) (uuid.UUID, error) { return loadPostContentDocumentID(ctx, store, postID) })
}

func (s *Service) UpdatePostLocaleMetadata(ctx context.Context, command UpdatePostLocaleMetadataCommand) (_ RoomSave, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if command.TitleChange == nil && command.SummaryChange == nil {
		return RoomSave{}, domainerr.New(domainerr.Invalid, "", "at least one typed locale metadata change is required")
	}
	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	contributorMemberID, err := contentblock.MutationContributor(command.ContributorMemberIDs)
	if err != nil {
		return RoomSave{}, err
	}
	documentID, err := s.contentDocumentID(ctx, command.PostID)
	if err != nil {
		return RoomSave{}, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return RoomSave{}, err
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
		func(ctx context.Context, store Store, effects *postEffects, domain contentblock.DomainContext, fence ContentFence) (postRoomSave, error) {
			if locale != domain.SourceLocale {
				return savePostTargetLocaleMetadata(ctx, store, effects, command, contributorMemberID, locale, expectedRevision, now, fence)
			}
			if command.ExpectedTargetRevision != nil {
				return postRoomSave{}, domainerr.InvalidField("expected_target_revision", "source Post room cannot carry a target revision")
			}
			return savePostSourceLocaleMetadata(ctx, store, effects, command, contributorMemberID, domain.SourceLocale, documentID, expectedRevision, now, fence)
		})
	if err != nil {
		return RoomSave{}, err
	}
	result := RoomSave{
		DocumentRevision: saved.documentRevision(locale), Changed: saved.changed(),
		SourceChanged: saved.source.TranslationSourceChanged,
		Locale:        locale,
	}
	if saved.changed() {
		result.ChangedLocales = []string{locale}
	}
	if locale != saved.sourceLocale {
		result.TargetRevision = &saved.target.TargetRevision
	}
	return result, nil
}

func savePostTargetLocaleMetadata(
	ctx context.Context,
	store Store,
	effects *postEffects,
	command UpdatePostLocaleMetadataCommand,
	contributor, locale string,
	expectedRevision uuid.UUID,
	now time.Time,
	fence ContentFence,
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
	saved.target, err = applyPostTargetLocaleMutation(ctx, store, postTargetLocaleMutationInput{
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

func savePostSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	effects *postEffects,
	command UpdatePostLocaleMetadataCommand,
	contributor, sourceLocale string,
	documentID, expectedRevision uuid.UUID,
	now time.Time,
	fence ContentFence,
) (postRoomSave, error) {
	var titleChanged bool
	advance, err := store.AdvanceContentRevision(
		ctx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		fence,
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			changed, changedTitle, err := updatePostLocaleMetadataRow(
				ctx, store, command.PostID, sourceLocale, command.TitleChange, command.SummaryChange, sourceLocale, now,
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
	if err := store.TouchPost(ctx, command.PostID, now); err != nil {
		return saved, err
	}
	if titleChanged {
		effects.requestOG(command.PostID, sourceLocale, false, "post_source_metadata_saved")
	}
	effects.audits = append(effects.audits, postLocaleContentAudit(contributor, command.PostID, sourceLocale, sharedtelemetry.AuditItemOperationUpdated))
	return saved, nil
}

func (s *Service) UpdatePostDocumentMetadata(ctx context.Context, command UpdatePostDocumentMetadataCommand) (_ RoomSave, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	if command.CategoryIDs == nil && command.TagIDs == nil {
		return RoomSave{}, domainerr.New(domainerr.Invalid, "", "at least one typed document metadata change is required")
	}
	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	documentID, err := s.contentDocumentID(ctx, command.PostID)
	if err != nil {
		return RoomSave{}, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return RoomSave{}, err
	}
	now := time.Now().UTC()
	var domain contentblock.DomainContext
	result, _, err := runPostMutation(ctx, s, mutation.StoreSpec[contentblock.AdvanceResult, Store]{
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			if domain, err = postCollaborationDocumentFence(s.spiceDB, command.PostID, command.ContributorMemberIDs)(ctx, store, documentID); err != nil {
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
	}, func(ctx context.Context, store Store, _ *postEffects) (contentblock.AdvanceResult, error) {
		result, err := store.AdvanceContentRevision(
			ctx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			lockedContentFence(documentID, domain),
			func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
				changed, err := replacePostDocumentTaxonomy(ctx, store, command)
				return contentblock.MetadataEffect{Changed: changed}, err
			},
		)
		if err != nil || !result.Changed {
			return result, err
		}
		return result, store.TouchPost(ctx, command.PostID, now)
	})
	if err != nil {
		return RoomSave{}, normalizePostContentBlockError(err)
	}
	return RoomSave{
		DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
		SourceChanged: result.TranslationSourceChanged,
		Locale:        locale,
	}, nil
}

// replacePostDocumentTaxonomy replaces the Categories and Tags command
// names and reports whether either changed.
func replacePostDocumentTaxonomy(ctx context.Context, store Store, command UpdatePostDocumentMetadataCommand) (bool, error) {
	currentCategories, currentTags, err := store.TaxonomyIDs(ctx, command.PostID)
	if err != nil {
		return false, err
	}
	changed := false
	for _, taxonomy := range []struct {
		requested *[]string
		current   []string
		taxonomy  Taxonomy
	}{
		{command.CategoryIDs, currentCategories, Categories},
		{command.TagIDs, currentTags, Tags},
	} {
		if taxonomy.requested == nil {
			continue
		}
		next := sliceutil.UniqueTrimmed(*taxonomy.requested)
		slices.Sort(next)
		if slices.Equal(taxonomy.current, next) {
			continue
		}
		if err := replacePostTaxonomyLinks(ctx, store, taxonomy.taxonomy, command.PostID, next); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// postVersionCheckpoint is the outcome of a checkpoint request.
type postVersionCheckpoint struct {
	created *StoredVersion
}

func (s *Service) CreatePostVersionCheckpoint(ctx context.Context, command CreatePostVersionCheckpointCommand) (_ Checkpoint, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", command.PostID))

	locale, err := validatePostAIDocumentIdentity(command.PostID, command.Locale)
	if err != nil {
		return Checkpoint{}, err
	}
	documentID, err := s.contentDocumentID(ctx, command.PostID)
	if err != nil {
		return Checkpoint{}, err
	}
	expectedRevision, err := parsePostContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return Checkpoint{}, err
	}
	var domain contentblock.DomainContext
	checkpoint, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[postVersionCheckpoint, Store]{
		Access: mutation.Checked(func(ctx context.Context, store Store) error {
			var err error
			domain, err = postCollaborationDocumentFence(s.spiceDB, command.PostID, command.ContributorMemberIDs)(ctx, store, documentID)
			return err
		}),
		Change: func(ctx context.Context, store Store) (postVersionCheckpoint, error) {
			created, err := createPostVersion(ctx, store, command, documentID, domain.SourceLocale, locale, expectedRevision)
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
		return Checkpoint{}, normalizePostContentBlockError(err)
	}
	result := Checkpoint{Created: checkpoint.created != nil, Revision: expectedRevision.String(), Locale: locale}
	if checkpoint.created != nil {
		result.VersionID = &checkpoint.created.ID
	}
	return result, nil
}

// createPostVersion records the source room as the next version, or returns
// nil for a target room or content equal to the latest version.
func createPostVersion(
	ctx context.Context,
	store Store,
	command CreatePostVersionCheckpointCommand,
	documentID uuid.UUID,
	sourceLocale, locale string,
	expectedRevision uuid.UUID,
) (*StoredVersion, error) {
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return nil, err
	}
	if snapshot.Document.Revision != expectedRevision {
		return nil, &contentblock.StaleRevisionError{CurrentRevision: snapshot.Document.Revision}
	}
	if locale != sourceLocale {
		return nil, nil
	}
	metadata, err := loadPostLocaleMetadataRow(ctx, store, command.PostID, sourceLocale, false)
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
	last, exists, err := store.LockLatestVersion(ctx, command.PostID)
	if err != nil {
		return nil, err
	}
	if exists && bytes.Equal(last.ContentSnapshot, encoded) {
		return nil, nil
	}
	nextVersion := int32(1)
	if exists {
		nextVersion = last.Version + 1
	}
	version := &StoredVersion{
		PostID: command.PostID, Version: nextVersion, ContentSnapshot: encoded,
		ContributorMemberIDs: normalizeContributorMemberIDs(command.ContributorMemberIDs),
	}
	if err := store.InsertVersion(ctx, version); err != nil {
		return nil, err
	}
	return version, nil
}

// loadPostLocaleMetadataRow reads one locale row, locked for update when
// forUpdate is set; a missing one answers not found.
func loadPostLocaleMetadataRow(ctx context.Context, store Store, postID, locale string, forUpdate bool) (LocaleMetadata, error) {
	row, err := store.LocaleMetadata(ctx, postID, locale, forUpdate)
	if errors.Is(err, ErrLocaleNotFound) {
		return LocaleMetadata{}, domainerr.ResourceNotFound("post_translation", postID+":"+locale)
	}
	return row, err
}

// replacePostTaxonomyLinks replaces the Post's links of taxonomy with the
// existing targets rawIDs names.
func replacePostTaxonomyLinks(ctx context.Context, store Store, taxonomy Taxonomy, postID string, rawIDs []string) error {
	ids := sliceutil.UniqueTrimmed(rawIDs)
	slices.Sort(ids)
	if len(ids) > 0 {
		count, err := store.CountTaxonomy(ctx, taxonomy, ids)
		if err != nil {
			return err
		}
		if count != int64(len(ids)) {
			return domainerr.New(domainerr.Invalid, "", "one or more "+string(taxonomy)+" IDs do not exist")
		}
	}
	return store.ReplaceTaxonomyLinks(ctx, taxonomy, postID, ids)
}

func updatePostLocaleMetadataRow(
	ctx context.Context,
	store Store,
	postID string,
	locale string,
	titleChange *StringChange,
	summaryChange *StringChange,
	sourceLocale string,
	now time.Time,
) (bool, bool, error) {
	if strings.TrimSpace(sourceLocale) == "" {
		return false, false, domainerr.Precondition("Post source locale is not initialized")
	}
	if locale != sourceLocale {
		return false, false, domainerr.Precondition("target Post locale metadata is read-only")
	}
	if err := ensurePostLocaleMetadataRow(ctx, store, postID, locale, sourceLocale); err != nil {
		return false, false, err
	}
	current, err := loadPostLocaleMetadataRow(ctx, store, postID, locale, true)
	if err != nil {
		return false, false, err
	}
	change := LocaleChange{UpdatedAt: now}
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
			change.SetTitle, change.Title = true, next
			titleChanged = true
		}
	}
	if summaryChange != nil {
		next, changeErr := postNullableStringChangeValue(summaryChange)
		if changeErr != nil {
			return false, false, domainerr.InvalidField("summary_change", changeErr.Error())
		}
		if !nullableStringEqual(current.Summary, next) {
			change.SetSummary, change.Summary = true, next
		}
	}
	if !change.SetTitle && !change.SetSummary {
		return false, false, nil
	}
	if _, err := store.UpdateLocaleMetadata(ctx, postID, locale, change); err != nil {
		return false, false, err
	}
	return true, titleChanged, nil
}

func ensurePostLocaleMetadataRow(
	ctx context.Context,
	store Store,
	postID string,
	locale string,
	sourceLocale string,
) error {
	if locale != sourceLocale {
		return domainerr.Precondition("target Post locale metadata is read-only")
	}
	count, err := store.CountLocale(ctx, postID, locale)
	if err != nil {
		return err
	}
	if count != 1 {
		return domainerr.Precondition("Post source locale metadata is not initialized")
	}
	return nil
}

func postNullableStringChangeValue(change *StringChange) (*string, error) {
	switch {
	case change == nil:
		return nil, errors.New("change is required")
	case change.Set != nil:
		return change.Set, nil
	case change.Clear != nil:
		if !*change.Clear {
			return nil, errors.New("clear must be true")
		}
		return nil, nil
	default:
		return nil, errors.New("set or clear is required")
	}
}

func postOptionalMetadataChangeValue(
	change *StringChange,
) (*string, bool, error) {
	if change == nil {
		return nil, false, nil
	}
	value, err := postNullableStringChangeValue(change)
	return value, true, err
}
