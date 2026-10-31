package page

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/targetlocale"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// AIDocumentState is the Page-owned coherent metadata and typed section tree
// consumed by the DCDP adapter.
type AIDocumentState struct {
	Page           model.Page
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
	DocumentLayout    model.DocumentLayout
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
	access := mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		return s.lockAIDocumentRoot(ctx, tx, query.PageID, clause.LockingStrengthShare, policyv1.Page.View, &root)
	})
	err := s.readLocked(ctx, access,
		func(ctx context.Context, tx *gorm.DB) error {
			var err error
			state, err = s.loadAIDocumentStateAfterAuthorization(ctx, tx, root, query.Locale)
			return err
		})
	return state, err
}

// lockedAIDocumentRoot is the Page root an AI document read or mutation
// locked and the principal authorized on it.
type lockedAIDocumentRoot struct {
	page       model.Page
	documentID uuid.UUID
	principal  *auth.UserInfo
}

// lockAIDocumentRoot locks the Page root with lock into root, then checks
// the one exact action on it.
func (s *Service) lockAIDocumentRoot(ctx context.Context, tx *gorm.DB, pageID, lock string, action pageAction, root *lockedAIDocumentRoot) error {
	var err error
	if root.page, root.documentID, err = loadPageAIDocumentRoot(ctx, tx, pageID, lock); err != nil {
		return err
	}
	root.principal, err = authorizePagePermissionAfterRootLock(ctx, tx, s.permissions, pageID, action)
	return err
}

func validatePageAIDocumentIdentity(pageID, locale string) error {
	if _, err := parsePageContentUUID("page_id", pageID); err != nil {
		return err
	}
	_, err := normalizePageDocumentLocale(locale)
	return err
}

func (s *Service) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root lockedAIDocumentRoot,
	locale string,
) (AIDocumentState, error) {
	state, err := loadPageTargetLocaleState(ctx, tx, s.contentBlocks, root.page.ID, root.documentID, locale, false)
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

func stateMetadataTitle(row *pageLocaleMetadataRow) *string {
	if row == nil {
		return nil
	}
	return cloneOptionalString(row.Title)
}

func stateMetadataSummary(row *pageLocaleMetadataRow) *string {
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

func (s *Service) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
) (pageAIDocumentApply, error) {
	if err := validatePageAIDocumentAttribution(mutation); err != nil {
		return pageAIDocumentApply{}, err
	}
	if err := validatePageAIDocumentLocaleScope(mutation); err != nil {
		return pageAIDocumentApply{}, err
	}
	now := dbtime.Now()
	if mutation.Locale != mutation.ObservedSourceLocale {
		return s.applyAIDocumentTargetMutation(ctx, tx, mutation, fence, now)
	}
	return s.applyAIDocumentSourceMutation(ctx, tx, mutation, fence, now)
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

func (s *Service) applyAIDocumentTargetMutation(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
	now time.Time,
) (pageAIDocumentApply, error) {
	result, targetRevision, err := applyPageTargetLocaleMutation(ctx, tx, s.contentBlocks, pageTargetLocaleMutationInput{
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
	return applied, touchPage(ctx, tx, mutation.PageID, now)
}

func (s *Service) applyAIDocumentSourceMutation(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
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
	result, err := s.contentBlocks.ApplyBatchWithMetadata(
		ctx, tx, mutation.Batch, fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			effect, err := applyPageAIDocumentMetadata(
				ctx, tx, mutation.PageID, mutation.Locale,
				mutation.ObservedLocaleExists, mutation.Metadata, now,
			)
			applied.metadataChanged = effect.Changed
			if err != nil || !mutation.Metadata.SetDocumentLayout {
				return effect, err
			}
			layoutEffect, err := applyPageDocumentLayoutMetadata(ctx, tx, mutation.PageID, mutation.Metadata.DocumentLayout, now)
			applied.layoutChanged = layoutEffect.Changed
			effect.Changed = effect.Changed || layoutEffect.Changed
			return effect, err
		},
	)
	applied.result = result
	if err != nil || !result.Changed {
		return applied, err
	}
	return applied, touchPage(ctx, tx, mutation.PageID, now)
}

func loadPageAIDocumentRoot(ctx context.Context, tx *gorm.DB, pageID, lock string) (model.Page, uuid.UUID, error) {
	var root model.Page
	query := tx.WithContext(ctx).Table("page").Where("id = ?", pageID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Page{}, uuid.Nil, domainerr.ResourceNotFound("page", pageID)
		}
		return model.Page{}, uuid.Nil, err
	}
	if root.ContentDocumentID == nil {
		return model.Page{}, uuid.Nil, domainerr.Precondition("Page content document is not initialized")
	}
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	if err != nil {
		return model.Page{}, uuid.Nil, fmt.Errorf("page content document ID is invalid: parse Page content document ID: %w", err)
	}
	if documentID == uuid.Nil {
		return model.Page{}, uuid.Nil, errors.New("page content document ID is invalid")
	}
	return root, documentID, nil
}

type pageAIDocumentLocale struct {
	Title   *string
	Summary *string
}

func loadPageAIDocumentLocale(ctx context.Context, tx *gorm.DB, pageID, locale string, forUpdate bool) (pageAIDocumentLocale, bool, error) {
	var row struct {
		Title   sql.NullString `gorm:"column:title"`
		Summary sql.NullString `gorm:"column:summary"`
	}
	query := tx.WithContext(ctx).Table("page_translation").Select("title", "summary").
		Where("entity_id = ? AND locale = ?", pageID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return pageAIDocumentLocale{}, false, nil
	}
	if result.Error != nil {
		return pageAIDocumentLocale{}, false, result.Error
	}
	return pageAIDocumentLocale{Title: pageAINullableString(row.Title), Summary: pageAINullableString(row.Summary)}, true, nil
}

func applyPageAIDocumentMetadata(
	ctx context.Context,
	tx *gorm.DB,
	pageID, locale string,
	observedExists bool,
	patch AIDocumentMetadataPatch,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	source, err := lockPageAIDocumentSource(ctx, tx, pageID)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	current, exists, err := loadPageAIDocumentLocale(ctx, tx, pageID, locale, true)
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
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO page_translation (entity_id, locale, title, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title, summary = EXCLUDED.summary, updated_at = EXCLUDED.updated_at
	`, pageID, locale, nextTitle, nextSummary, now, now).Error; err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: locale == source.SourceLocale,
		SourceLocale: source.SourceLocale, ChangedLocales: []string{locale},
	}, nil
}

func lockPageAIDocumentSource(ctx context.Context, tx *gorm.DB, pageID string) (model.Page, error) {
	var page model.Page
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", pageID).Take(&page).Error; err != nil {
		return model.Page{}, err
	}
	return page, nil
}

func pageAINullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
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
