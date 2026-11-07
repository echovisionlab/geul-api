package page

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AIDocumentState is the Page-owned coherent metadata and typed section tree
// consumed by the DCDP adapter.
type AIDocumentState struct {
	Page           Page
	DocumentID     uuid.UUID
	Revision       string
	TargetRevision *string
	SourceLocale   string
	Locale         string
	LocaleExists   bool
	Title          *string
	Summary        *string
	Snapshot       contentblock.Snapshot
	Document       *contentv1.LocalizedPageDocument
	ViewerMemberID string
}

type AIDocumentMetadataPatch struct {
	EnsureLocale      bool
	SetTitle          bool
	Title             *string
	SetSummary        bool
	Summary           *string
	SetDocumentLayout bool
	DocumentLayout    DocumentLayout
}

type AIDocumentMutation struct {
	PageID                 string
	Locale                 string
	ObservedSourceLocale   string
	ExpectedRevision       uuid.UUID
	ExpectedTargetRevision *string
	ObservedLocaleExists   bool
	Batch                  contentblock.Batch
	Metadata               AIDocumentMetadataPatch
	DeleteTranslation      bool
	ContributorMemberID    string
}

// LoadAIDocumentQuery reads the AI document of one Page locale.
type LoadAIDocumentQuery struct {
	PageID string
	Locale string
}

// LoadAIDocument reads the coherent metadata and typed section tree of a
// Page locale the caller may view, under one share lock of the Page root.
func (s *Service) LoadAIDocument(ctx context.Context, query LoadAIDocumentQuery) (AIDocumentState, error) {
	if err := validatePageAIDocumentIdentity(query.PageID, query.Locale); err != nil {
		return AIDocumentState{}, err
	}
	var root lockedAIDocumentRoot
	var state AIDocumentState
	access := mutation.Locked(func(ctx context.Context, store Store, require mutation.Require) error {
		return lockAIDocumentRoot(ctx, store, require, query.PageID, ShareLock, policyv1.Page.View, &root)
	})
	err := s.readLocked(ctx, access,
		func(ctx context.Context, store Store) error {
			var err error
			state, err = loadAIDocumentStateAfterAuthorization(ctx, store, root, query.Locale)
			return err
		})
	return state, err
}

// lockedAIDocumentRoot is the Page root an AI document read or mutation
// locked and the principal authorized on it.
type lockedAIDocumentRoot struct {
	page       Page
	documentID uuid.UUID
	principal  *auth.UserInfo
}

// lockAIDocumentRoot locks the Page root with lock into root, then checks
// the one exact action on it.
func lockAIDocumentRoot(ctx context.Context, store Store, require mutation.Require, pageID string, lock RowLock, action pageAction, root *lockedAIDocumentRoot) error {
	var err error
	if root.page, root.documentID, err = loadPageAIDocumentRoot(ctx, store, pageID, lock); err != nil {
		return err
	}
	root.principal, err = authorizePagePermissionAfterRootLock(ctx, require, pageID, action)
	return err
}

func validatePageAIDocumentIdentity(pageID, locale string) error {
	if _, err := parsePageContentUUID("page_id", pageID); err != nil {
		return err
	}
	_, err := normalizePageDocumentLocale(locale)
	return err
}

func loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	store aiDocumentStateReads,
	root lockedAIDocumentRoot,
	locale string,
) (AIDocumentState, error) {
	state, err := loadPageTargetLocaleState(ctx, store, root.page.ID, root.documentID, locale, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := contentblock.SnapshotToLocalizedPageDocument(state.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizePageContentBlockError(err)
	}
	return AIDocumentState{
		Page: root.page, DocumentID: root.documentID, Revision: state.Snapshot.Document.Revision.String(),
		TargetRevision: clonePageTargetRevision(state.TargetRevision),
		SourceLocale:   state.SourceLocale,
		Locale:         locale, LocaleExists: state.TargetMetadata != nil,
		Title: stateMetadataTitle(state.TargetMetadata), Summary: stateMetadataSummary(state.TargetMetadata),
		Snapshot: state.Snapshot, Document: document, ViewerMemberID: root.principal.MemberID.String(),
	}, nil
}

func stateMetadataTitle(row *Locale) *string {
	if row == nil {
		return nil
	}
	return cloneOptionalString(row.Title)
}

func stateMetadataSummary(row *Locale) *string {
	if row == nil {
		return nil
	}
	return cloneOptionalString(row.Summary)
}

// pageAIDocumentApply is the outcome of a compiled AI document mutation.
type pageAIDocumentApply struct {
	result         contentblock.Result
	targetRevision *string
	// metadataChanged reports a changed source title or summary.
	metadataChanged bool
	layoutChanged   bool
}

func applyAIDocumentMutationInTransaction(
	ctx context.Context,
	store Store,
	mutation AIDocumentMutation,
	fence ContentFence,
) (pageAIDocumentApply, error) {
	if err := validatePageAIDocumentAttribution(mutation); err != nil {
		return pageAIDocumentApply{}, err
	}
	if err := validatePageAIDocumentLocaleScope(mutation); err != nil {
		return pageAIDocumentApply{}, err
	}
	now := dbtime.Now()
	if mutation.Locale != mutation.ObservedSourceLocale {
		return applyAIDocumentTargetMutation(ctx, store, mutation, fence, now)
	}
	return applyAIDocumentSourceMutation(ctx, store, mutation, fence, now)
}

// validatePageAIDocumentAttribution requires the compiled batch to carry
// the mutation's identity, revision and one current Member.
func validatePageAIDocumentAttribution(mutation AIDocumentMutation) error {
	if _, err := parsePageContentUUID("page_id", mutation.PageID); err != nil {
		return err
	}
	if mutation.ExpectedRevision == uuid.Nil || mutation.Batch.DocumentID == uuid.Nil {
		return domainerr.InvalidField("expected_revision", "is required")
	}
	contributorIDs := pageAIContributorIDs(mutation.ContributorMemberID)
	if len(contributorIDs) != 1 || len(mutation.Batch.ContributorMemberIDs) != 1 ||
		mutation.Batch.ContributorMemberIDs[0] != contributorIDs[0] {
		return domainerr.InvalidField("contributor", "compiled attribution must match the current Member")
	}
	if mutation.Batch.ExpectedRevision != mutation.ExpectedRevision {
		return domainerr.InvalidField("expected_revision", "does not match the compiled section batch")
	}
	if strings.TrimSpace(mutation.ObservedSourceLocale) == "" {
		return domainerr.InvalidField("source_locale", "observed source locale is required")
	}
	return nil
}

// validatePageAIDocumentLocaleScope keeps a non-source mutation to its own
// locale and the translation lifecycle to target locales.
func validatePageAIDocumentLocaleScope(mutation AIDocumentMutation) error {
	source := mutation.Locale == mutation.ObservedSourceLocale
	if !source {
		if mutation.Metadata.SetDocumentLayout {
			return domainerr.InvalidField("document_layout", "only the source locale may change Page document layout")
		}
		if len(mutation.Batch.Upserts) != 0 || len(mutation.Batch.Deletes) != 0 || len(mutation.Batch.Reorders) != 0 {
			return domainerr.InvalidField("operations", "non-source locale cannot mutate the Page section graph")
		}
		for _, group := range mutation.Batch.LocaleGroups {
			if group.Locale != mutation.Locale {
				return domainerr.InvalidField("operations", "compiled locale mutation targets another locale")
			}
		}
	}
	if source && (mutation.Metadata.EnsureLocale || mutation.DeleteTranslation) {
		return domainerr.InvalidField("locale", "Page source translation lifecycle cannot be changed")
	}
	if mutation.DeleteTranslation && (mutation.Metadata != AIDocumentMetadataPatch{} || hasPageAIDocumentBatchChanges(mutation.Batch)) {
		return domainerr.InvalidField("operations", "translation deletion must be the only mutation")
	}
	return nil
}

func applyAIDocumentTargetMutation(
	ctx context.Context,
	store Store,
	mutation AIDocumentMutation,
	fence ContentFence,
	now time.Time,
) (pageAIDocumentApply, error) {
	result, targetRevision, err := applyPageTargetLocaleMutation(ctx, store, pageTargetLocaleMutationInput{
		PageID: mutation.PageID, DocumentID: mutation.Batch.DocumentID, Locale: mutation.Locale,
		Batch: mutation.Batch, ExpectedTargetRevision: mutation.ExpectedTargetRevision,
		Patch: pageTargetMetadataPatch{
			EnsureLocale: mutation.Metadata.EnsureLocale,
			UpdateTitle:  mutation.Metadata.SetTitle, Title: mutation.Metadata.Title,
			UpdateSummary: mutation.Metadata.SetSummary, Summary: mutation.Metadata.Summary,
			DeleteLocale: mutation.DeleteTranslation,
		},
		Policy: targetlocale.Policy{AllowCreate: mutation.Metadata.EnsureLocale},
		Now:    now, Fence: fence,
	})
	applied := pageAIDocumentApply{result: result, targetRevision: targetRevision}
	if err != nil || !result.Changed {
		return applied, err
	}
	return applied, store.TouchPage(ctx, mutation.PageID, now)
}

func applyAIDocumentSourceMutation(
	ctx context.Context,
	store Store,
	mutation AIDocumentMutation,
	fence ContentFence,
	now time.Time,
) (pageAIDocumentApply, error) {
	if mutation.ExpectedTargetRevision != nil {
		return pageAIDocumentApply{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	if mutation.Metadata.SetDocumentLayout {
		if err := mutation.Metadata.DocumentLayout.Validate(); err != nil {
			return pageAIDocumentApply{}, domainerr.InvalidField("document_layout", err.Error())
		}
	}
	var applied pageAIDocumentApply
	result, err := store.ApplyContentBatchWithMetadata(
		ctx, mutation.Batch, fence,
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			effect, err := applyPageAIDocumentMetadata(
				ctx, store, mutation.PageID, mutation.Locale,
				mutation.ObservedLocaleExists, mutation.Metadata, now,
			)
			applied.metadataChanged = effect.Changed
			if err != nil || !mutation.Metadata.SetDocumentLayout {
				return effect, err
			}
			layoutEffect, err := applyPageDocumentLayoutMetadata(ctx, store, mutation.PageID, mutation.Metadata.DocumentLayout, now)
			applied.layoutChanged = layoutEffect.Changed
			effect.Changed = effect.Changed || layoutEffect.Changed
			return effect, err
		},
	)
	applied.result = result
	if err != nil || !result.Changed {
		return applied, err
	}
	return applied, store.TouchPage(ctx, mutation.PageID, now)
}

func applyPageAIDocumentMetadata(
	ctx context.Context,
	store Store,
	pageID, locale string,
	observedExists bool,
	patch AIDocumentMetadataPatch,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	source, err := store.FindPage(ctx, ByID, pageID, UpdateLock)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	current, exists, err := store.Locale(ctx, pageID, locale, UpdateLock)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != observedExists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("Page translation presence changed; reload before saving")
	}
	if locale == source.SourceLocale && !exists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("Page source translation is missing")
	}
	if !exists && !patch.EnsureLocale && !patch.SetTitle && !patch.SetSummary {
		return contentblock.MetadataEffect{}, nil
	}
	nextTitle := cloneOptionalString(current.Title)
	nextSummary := cloneOptionalString(current.Summary)
	if patch.SetTitle {
		nextTitle = cloneOptionalString(patch.Title)
	}
	if patch.SetSummary {
		nextSummary = cloneOptionalString(patch.Summary)
	}
	if locale == source.SourceLocale && (nextTitle == nil || strings.TrimSpace(*nextTitle) == "") {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("title", "Page source title cannot be empty")
	}
	changed := !exists || !nullableStringEqual(current.Title, nextTitle) || !nullableStringEqual(current.Summary, nextSummary)
	if !changed {
		return contentblock.MetadataEffect{}, nil
	}
	if err := store.UpsertLocale(ctx, pageID, locale, nextTitle, nextSummary, now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: locale == source.SourceLocale,
		SourceLocale: source.SourceLocale, ChangedLocales: []string{locale},
	}, nil
}

func pageAIContributorIDs(memberID string) []uuid.UUID {
	parsed, err := uuid.Parse(memberID)
	if err != nil || parsed == uuid.Nil {
		return nil
	}
	return []uuid.UUID{parsed}
}

func hasPageAIDocumentBatchChanges(batch contentblock.Batch) bool {
	return len(batch.Upserts) != 0 || len(batch.Deletes) != 0 || len(batch.Reorders) != 0 || len(batch.LocaleGroups) != 0
}
