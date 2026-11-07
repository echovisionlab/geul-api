package page

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// pageEntityName names the owner in shared content document messages.
const pageEntityName = "Page"

type pageTargetLocaleState struct {
	Snapshot       contentblock.Snapshot
	SourceLocale   string
	SourceMetadata Locale
	TargetMetadata *Locale
	TargetRevision string
}

func (state pageTargetLocaleState) locked() targetlocale.State {
	return targetlocale.State{
		Entity: pageEntityName, Snapshot: state.Snapshot, SourceLocale: state.SourceLocale,
		TargetExists: state.TargetMetadata != nil, TargetRevision: state.TargetRevision,
	}
}

type pageTargetMetadataPatch struct {
	EnsureLocale  bool
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
	DeleteLocale  bool
}

func normalizePageDocumentLocale(locale string) (string, error) {
	normalized := localization.NormalizeExactSupportedLocale(locale)
	if normalized == nil {
		return "", domainerr.InvalidField("locale", "must be an exact canonical locale")
	}
	return *normalized, nil
}

// pageSourceRoomFence admits a source-locale room only while locale is still
// the Page's source locale.
func pageSourceRoomFence(locale string, base ContentFence) ContentFence {
	return func(ctx context.Context, store Store, documentID uuid.UUID) (contentblock.DomainContext, error) {
		domain, err := base(ctx, store, documentID)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if domain.SourceLocale != locale {
			return contentblock.DomainContext{}, store.LocaleOwnershipConflict(ctx, documentID,
				"Page source locale changed since the room was loaded; reload before saving",
			)
		}
		return domain, nil
	}
}

func derivePageTargetRevision(documentRevision string, metadata Locale) (string, error) {
	if metadata.IncarnationID == uuid.Nil {
		return "", errors.New("Page translation locale incarnation is missing")
	}
	updatedAt := metadata.UpdatedAt
	revision, err := translation.DeriveTargetRevision(translation.TargetRevisionFacts{
		LocaleExists: true, DocumentRevision: documentRevision,
		LocaleIncarnation: metadata.IncarnationID.String(), LocaleUpdatedAt: &updatedAt,
	})
	if err != nil {
		return "", err
	}
	return revision, nil
}

// localeLock is the lock a locale read takes: FOR UPDATE when the caller
// writes it.
func localeLock(forUpdate bool) RowLock {
	if forUpdate {
		return UpdateLock
	}
	return Unlocked
}

func loadPageTargetLocaleState(
	ctx context.Context,
	store aiDocumentStateReads,
	pageID string,
	documentID uuid.UUID,
	locale string,
	forUpdate bool,
) (pageTargetLocaleState, error) {
	locale, err := normalizePageDocumentLocale(locale)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	lock := ShareLock
	if forUpdate {
		lock = UpdateLock
	}
	_, lockedDocumentID, err := loadPageAIDocumentRoot(ctx, store, pageID, lock)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if lockedDocumentID != documentID {
		return pageTargetLocaleState{}, domainerr.Precondition("Page content document changed; reload before saving")
	}
	domain, err := loadPageContentDomainContext(ctx, store, pageID)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, domain.SourceLocale)
	if err != nil {
		return pageTargetLocaleState{}, normalizePageContentBlockError(err)
	}
	if snapshot.SourceLocale != domain.SourceLocale || snapshot.Document.Profile != "page" {
		return pageTargetLocaleState{}, domainerr.Precondition("Page content document source or profile is inconsistent")
	}
	sourceMetadata, sourceExists, err := store.Locale(ctx, pageID, domain.SourceLocale, localeLock(forUpdate))
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if !sourceExists {
		return pageTargetLocaleState{}, domainerr.Precondition("Page source locale metadata is missing")
	}
	state := pageTargetLocaleState{Snapshot: snapshot, SourceLocale: domain.SourceLocale, SourceMetadata: sourceMetadata}
	if locale == domain.SourceLocale {
		state.TargetMetadata = &sourceMetadata
		return state, nil
	}
	target, exists, err := store.Locale(ctx, pageID, locale, localeLock(forUpdate))
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	if !exists {
		if targetlocale.SnapshotHasLocale(snapshot, locale) {
			return pageTargetLocaleState{}, domainerr.Precondition("Page target locale Blocks exist without owning metadata")
		}
		return state, nil
	}
	state.TargetMetadata = &target
	state.TargetRevision, err = derivePageTargetRevision(snapshot.Document.Revision.String(), target)
	if err != nil {
		return pageTargetLocaleState{}, err
	}
	return state, nil
}

// pageTargetLocaleMutationInput is one write to a Page target locale.
type pageTargetLocaleMutationInput struct {
	PageID                 string
	DocumentID             uuid.UUID
	Locale                 string
	Batch                  contentblock.Batch
	ExpectedTargetRevision *string
	Patch                  pageTargetMetadataPatch
	Policy                 targetlocale.Policy
	Now                    time.Time
	Fence                  ContentFence
}

func applyPageTargetLocaleMutation(
	ctx context.Context,
	store Store,
	input pageTargetLocaleMutationInput,
) (contentblock.Result, *string, error) {
	pageID, documentID, locale, patch := input.PageID, input.DocumentID, input.Locale, input.Patch
	domain, err := input.Fence(ctx, store, documentID)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	state, err := loadPageTargetLocaleState(ctx, store, pageID, documentID, locale, true)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if locale == state.SourceLocale {
		return contentblock.Result{}, nil, domainerr.InvalidField("locale", "Page target locale must differ from source locale")
	}
	if state.SourceLocale != domain.SourceLocale {
		return contentblock.Result{}, nil, domainerr.Precondition("Page source locale changed; reload before saving")
	}
	if patch.DeleteLocale && (patch.EnsureLocale || patch.UpdateTitle || patch.UpdateSummary || len(input.Batch.LocaleGroups) != 0) {
		return contentblock.Result{}, nil, domainerr.InvalidField("operations", "Page translation deletion must be exclusive")
	}
	batch, err := input.Policy.Admit(state.locked(), locale, input.Batch, input.ExpectedTargetRevision)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	if patch.DeleteLocale && state.TargetMetadata == nil {
		return contentblock.Result{}, nil, domainerr.Precondition("Page target locale must exist before deletion")
	}
	now := targetlocale.WriteTime(input.Now)
	if patch.DeleteLocale {
		batch.LocaleGroups = targetlocale.DeleteGroups(state.Snapshot, locale)
	}
	write := &pageTargetMetadataWrite{pageID: pageID, locale: locale, state: state, patch: patch, now: now}
	result, err := store.ApplyTargetLocaleContentBatch(ctx, batch, locale, lockedContentFence(documentID, domain), write.apply)
	if err != nil {
		return contentblock.Result{}, nil, normalizePageContentBlockError(err)
	}
	if patch.DeleteLocale {
		return result, nil, nil
	}
	targetRevision, err := derivePageTargetRevision(result.DocumentRevision.String(), write.final)
	if err != nil {
		return contentblock.Result{}, nil, err
	}
	return result, &targetRevision, nil
}

// pageTargetMetadataWrite stores the Page target locale row of one write and
// keeps the row it leaves behind.
type pageTargetMetadataWrite struct {
	pageID string
	locale string
	state  pageTargetLocaleState
	patch  pageTargetMetadataPatch
	now    time.Time
	final  Locale
}

func (w *pageTargetMetadataWrite) apply(ctx context.Context, store Store, contentChanged bool) (contentblock.MetadataEffect, error) {
	switch {
	case w.patch.DeleteLocale:
		deleted, err := store.DeleteLocale(ctx, w.pageID, w.locale)
		if err != nil {
			return contentblock.MetadataEffect{}, err
		}
		if !deleted {
			return contentblock.MetadataEffect{}, errors.New("Page target locale disappeared while deleting")
		}
	case w.state.TargetMetadata == nil:
		if err := w.create(ctx, store); err != nil {
			return contentblock.MetadataEffect{}, err
		}
	default:
		changed, err := w.update(ctx, store, contentChanged)
		if err != nil || !changed {
			return contentblock.MetadataEffect{}, err
		}
	}
	return contentblock.MetadataEffect{Changed: true, ChangedLocales: []string{w.locale}}, nil
}

func (w *pageTargetMetadataWrite) create(ctx context.Context, store Store) error {
	incarnationID, err := store.CreateTargetLocale(ctx, w.pageID, w.locale, w.patch.Title, w.patch.Summary, w.now)
	if err != nil {
		return err
	}
	if incarnationID == uuid.Nil {
		return errors.New("Page target locale could not be created")
	}
	w.final = Locale{
		Locale: w.locale, Title: cloneOptionalString(w.patch.Title), Summary: cloneOptionalString(w.patch.Summary),
		IncarnationID: incarnationID, UpdatedAt: w.now,
	}
	return nil
}

func (w *pageTargetMetadataWrite) update(ctx context.Context, store Store, contentChanged bool) (bool, error) {
	w.final = *w.state.TargetMetadata
	metadataChanged := false
	change := LocaleChange{}
	if w.patch.UpdateTitle && !nullableStringEqual(w.final.Title, w.patch.Title) {
		w.final.Title, change.SetTitle, change.Title, metadataChanged = cloneOptionalString(w.patch.Title), true, w.patch.Title, true
	}
	if w.patch.UpdateSummary && !nullableStringEqual(w.final.Summary, w.patch.Summary) {
		w.final.Summary, change.SetSummary, change.Summary, metadataChanged = cloneOptionalString(w.patch.Summary), true, w.patch.Summary, true
	}
	if !contentChanged && !metadataChanged {
		return false, nil
	}
	w.final.UpdatedAt = translation.NextTargetUpdatedAt(w.now, w.final.UpdatedAt)
	change.UpdatedAt = w.final.UpdatedAt
	exists, err := store.UpdateLocale(ctx, w.pageID, w.locale, change)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, errors.New("Page target locale disappeared while saving")
	}
	return true, nil
}

func pageContributorUUIDs(values []string) ([]uuid.UUID, error) {
	contributors := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		contributor, err := parsePageContentUUID("contributor_member_ids", value)
		if err != nil {
			return nil, err
		}
		contributors = append(contributors, contributor)
	}
	return contributors, nil
}

// loadPageAIDocumentRoot reads the Page root with lock and its content
// document.
func loadPageAIDocumentRoot(ctx context.Context, store aiDocumentStateReads, pageID string, lock RowLock) (Page, uuid.UUID, error) {
	root, err := store.FindPage(ctx, ByID, pageID, lock)
	if err != nil {
		if errors.Is(err, ErrPageNotFound) {
			return Page{}, uuid.Nil, domainerr.ResourceNotFound("page", pageID)
		}
		return Page{}, uuid.Nil, err
	}
	if root.ContentDocumentID == nil {
		return Page{}, uuid.Nil, domainerr.Precondition("Page content document is not initialized")
	}
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	if err != nil {
		return Page{}, uuid.Nil, fmt.Errorf("page content document ID is invalid: parse Page content document ID: %w", err)
	}
	if documentID == uuid.Nil {
		return Page{}, uuid.Nil, errors.New("page content document ID is invalid")
	}
	return root, documentID, nil
}
