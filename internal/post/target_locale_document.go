package post

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
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
	Policy                   targetlocale.Policy
	SetTitle                 bool
	Title                    *string
	SetSummary               bool
	Summary                  *string
	Now                      time.Time
	Fence                    ContentFence
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
	Fence                    ContentFence
}

type postTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata LocaleMetadata
	TargetMetadata *LocaleMetadata
	TargetRevision string
}

func (state postTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: postEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

// loadPostTargetLocaleStateForDocument reads a target locale's state; with
// forUpdate it locks the content document and the locale row first.
func loadPostTargetLocaleStateForDocument(
	ctx context.Context,
	store Store,
	postID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (postTargetLocaleState, error) {
	if store == nil || documentID == uuid.Nil {
		return postTargetLocaleState{}, errors.New("Post target locale dependencies are required")
	}
	normalizedLocale, err := validatePostAIDocumentIdentity(postID, locale)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	domain, err := loadPostContentDomainContext(ctx, store, postID)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	if normalizedLocale == domain.SourceLocale {
		return postTargetLocaleState{}, domainerr.InvalidField("locale", "Post target locale must differ from the source locale")
	}

	if forUpdate {
		if err := store.LockContentDocument(ctx, documentID); err != nil {
			return postTargetLocaleState{}, normalizePostContentBlockError(err)
		}
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return postTargetLocaleState{}, normalizePostContentBlockError(err)
	}
	if snapshot.Document.Profile != postContentDocumentProfile {
		return postTargetLocaleState{}, domainerr.Precondition("Post target locale requires the Post content profile")
	}
	sourceMetadata, err := loadPostLocaleMetadataRow(ctx, store, postID, domain.SourceLocale, false)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	targetMetadata, exists, err := store.OptionalLocaleMetadata(ctx, postID, normalizedLocale, forUpdate)
	if err != nil {
		return postTargetLocaleState{}, err
	}
	if !exists && targetlocale.SnapshotHasLocale(snapshot, normalizedLocale) {
		return postTargetLocaleState{}, domainerr.Precondition("Post target locale Blocks exist without owning metadata")
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

func derivePostTargetRevision(documentRevision string, metadata LocaleMetadata) (string, error) {
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision, LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
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
	source LocaleMetadata,
	target *LocaleMetadata,
	locale string,
) LocaleMetadata {
	projected := LocaleMetadata{Locale: locale, Title: source.Title, Summary: source.Summary}
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
	store Store,
	input postTargetLocaleMutationInput,
) (postTargetLocaleMutationResult, error) {
	if input.ExpectedDocumentRevision == uuid.Nil || strings.TrimSpace(input.PostID) == "" ||
		strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return postTargetLocaleMutationResult{}, domainerr.InvalidField("target", "Post target identity, revision, and fence are required")
	}
	documentID, err := loadPostContentDocumentID(ctx, store, input.PostID)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	domain, err := input.Fence(ctx, store, documentID)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	state, err := loadPostTargetLocaleStateForDocument(ctx, store, input.PostID, documentID, input.Locale, true)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	if state.SourceLocale != domain.SourceLocale {
		return postTargetLocaleMutationResult{}, domainerr.Precondition("Post source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return postTargetLocaleMutationResult{}, contentblockStaleRevision(state.Snapshot.Document.Revision)
	}
	if input.Storage != nil && input.Batch != nil {
		return postTargetLocaleMutationResult{}, domainerr.InvalidField("batch", "Post target accepts one mutation representation")
	}
	var batch contentblock.Batch
	if input.Batch != nil {
		batch = *input.Batch
	} else {
		if err := validatePostTargetStorage(
			input.Storage, input.ExpectedDocumentRevision, input.Locale, input.Policy.AllowLocaleDeletes,
		); err != nil {
			return postTargetLocaleMutationResult{}, err
		}
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
	}
	batch, err = input.Policy.Admit(state.locked(), input.Locale, batch, input.ExpectedTargetRevision)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	now := targetlocale.WriteTime(input.Now)
	result := postTargetLocaleMutationResult{
		DocumentRevision: state.Snapshot.Document.Revision.String(),
		LocaleCreated:    state.TargetMetadata == nil,
	}
	write := &postTargetMetadataWrite{input: input, state: state, now: now}
	storeResult, err := store.ApplyTargetLocaleContentBatch(
		ctx, batch, input.Locale, lockedContentFence(documentID, domain), write.apply,
	)
	if err != nil {
		return postTargetLocaleMutationResult{}, normalizePostContentBlockError(err)
	}
	result.Changed, result.TitleChanged = storeResult.Changed, write.titleChanged
	result.ContentChanged = storeResult.ContentChanged
	result.MetadataChanged = storeResult.MetadataChanged
	if result.Changed {
		if err := store.TouchPost(ctx, input.PostID, now); err != nil {
			return postTargetLocaleMutationResult{}, err
		}
	}
	result.TargetRevision, err = derivePostTargetRevision(result.DocumentRevision, write.final)
	if err != nil {
		return postTargetLocaleMutationResult{}, err
	}
	return result, nil
}

// postTargetMetadataWrite stores the Post target locale row of one write and
// keeps the row it leaves behind.
type postTargetMetadataWrite struct {
	input        postTargetLocaleMutationInput
	state        postTargetLocaleState
	now          time.Time
	final        LocaleMetadata
	titleChanged bool
}

func (w *postTargetMetadataWrite) apply(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error) {
	current := LocaleMetadata{Locale: w.input.Locale}
	if w.state.TargetMetadata != nil {
		current = *w.state.TargetMetadata
	} else if w.input.Policy.SeedSource {
		current = w.state.SourceMetadata
	}
	title := current.Title
	summary := current.Summary
	if w.input.SetTitle {
		w.titleChanged = !nullableStringEqual(title, w.input.Title)
		title = w.input.Title
	}
	metadataValueChanged := w.titleChanged
	if w.input.SetSummary {
		metadataValueChanged = metadataValueChanged || !nullableStringEqual(summary, w.input.Summary)
		summary = w.input.Summary
	}
	if w.state.TargetMetadata == nil {
		w.final = LocaleMetadata{
			Locale: w.input.Locale, Title: title, Summary: summary, UpdatedAt: w.now,
		}
		created, err := store.InsertLocale(ctx, w.input.PostID, w.input.Locale, title, summary, w.now)
		if err != nil {
			return contentblock.MetadataEffect{}, err
		}
		if created != 1 {
			return contentblock.MetadataEffect{}, errors.New("Post target locale could not be created")
		}
		return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.input.Locale}}, nil
	}
	if !contentChanged && !metadataValueChanged {
		w.final = current
		return contentblock.MetadataEffect{}, nil
	}
	updatedAt := translation.NextTargetUpdatedAt(w.now, current.UpdatedAt)
	updated, err := store.UpdateLocaleMetadata(ctx, w.input.PostID, w.input.Locale, LocaleChange{
		SetTitle: true, Title: title, SetSummary: true, Summary: summary, UpdatedAt: updatedAt,
	})
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if updated != 1 {
		return contentblock.MetadataEffect{}, errors.New("Post target locale disappeared while saving")
	}
	w.final = LocaleMetadata{
		Locale: w.input.Locale, Title: title, Summary: summary, UpdatedAt: updatedAt,
	}
	return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.input.Locale}}, nil
}

func deletePostTargetLocale(
	ctx context.Context,
	store Store,
	input postTargetLocaleDeleteInput,
) (contentblock.Result, error) {
	if input.ExpectedDocumentRevision == uuid.Nil || strings.TrimSpace(input.PostID) == "" ||
		strings.TrimSpace(input.Locale) == "" || input.Fence == nil {
		return contentblock.Result{}, domainerr.InvalidField("target", "Post target delete identity, revision, and fence are required")
	}
	documentID, err := loadPostContentDocumentID(ctx, store, input.PostID)
	if err != nil {
		return contentblock.Result{}, err
	}
	domain, err := input.Fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, err
	}
	state, err := loadPostTargetLocaleStateForDocument(ctx, store, input.PostID, documentID, input.Locale, true)
	if err != nil {
		return contentblock.Result{}, err
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, domainerr.Precondition("Post source locale changed; reload before saving")
	}
	if state.Snapshot.Document.Revision != input.ExpectedDocumentRevision {
		return contentblock.Result{}, contentblockStaleRevision(state.Snapshot.Document.Revision)
	}
	if err := state.locked().CheckRevision(input.ExpectedTargetRevision, targetlocale.RevisionObserved); err != nil {
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
		LocaleGroups:         targetlocale.DeleteGroups(state.Snapshot, input.Locale),
	}
	result, err := store.ApplyTargetLocaleContentBatch(
		ctx,
		batch,
		input.Locale,
		lockedContentFence(documentID, domain),
		func(ctx context.Context, store Store, _ bool) (contentblock.MetadataEffect, error) {
			deleted, err := store.DeleteLocale(ctx, input.PostID, input.Locale)
			if err != nil {
				return contentblock.MetadataEffect{}, err
			}
			if deleted != 1 {
				return contentblock.MetadataEffect{}, errors.New("Post target locale disappeared while deleting")
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
		if err := store.TouchPost(ctx, input.PostID, now); err != nil {
			return contentblock.Result{}, err
		}
	}
	return result, nil
}

// validatePostTargetStorage applies contentblock.ValidateTargetLocaleBatch's
// rules to the storage form, before conversion can reject the same input for
// another reason.
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
		return domainerr.InvalidField("batch.expected_revision", "must match the observed shared Post document revision")
	}
	if len(storage.BaseUpserts) != 0 || len(storage.Deletes) != 0 || len(storage.Moves) != 0 {
		return domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
			"Post target locale cannot mutate the shared Block graph or File ownership",
		)
	}
	if len(storage.LocaleGroups) > 1 {
		return domainerr.CollaborationRejection(
			intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
			"Post target locale contains multiple locale groups",
		)
	}
	for _, group := range storage.LocaleGroups {
		if group.Locale != locale {
			return domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_ROOM_LOCALE_MISMATCH,
				"Post target mutation must match the authorized locale",
			)
		}
		if !allowLocaleValueDeletes && len(group.Deletes) != 0 {
			return domainerr.CollaborationRejection(
				intrav1.CollaborationMutationRejectionReason_COLLABORATION_MUTATION_REJECTION_REASON_NON_SOURCE_STRUCTURE_FORBIDDEN,
				"Post target patch cannot unset locale Blocks; write explicit empty values instead",
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
			return domainerr.CollaborationRejection(
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
