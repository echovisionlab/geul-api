package page

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
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
	EnsureLocale bool
	SetTitle     bool
	Title        *string
	SetSummary   bool
	Summary      *string
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

// AIDocumentService owns Page edit authorization, root locking and the single
// revision transaction shared by Page metadata and generated section Blocks.
type AIDocumentService struct{ internal *InternalPageService }

var errRollbackPageAIDocumentValidation = errors.New("rollback Page AI document validation")

func NewAIDocumentService(internal *InternalPageService) (*AIDocumentService, error) {
	if internal == nil || internal.db == nil || internal.spiceDB == nil || internal.contentBlocks == nil {
		return nil, errors.New("page AI document service dependencies are required")
	}
	return &AIDocumentService{internal: internal}, nil
}

func (s *AIDocumentService) Load(ctx context.Context, pageID, locale string) (AIDocumentState, error) {
	if err := validatePageAIDocumentIdentity(pageID, locale); err != nil {
		return AIDocumentState{}, err
	}
	var state AIDocumentState
	err := s.internal.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		root, documentID, err := loadPageAIDocumentRoot(ctx, tx, pageID, "SHARE")
		if err != nil {
			return err
		}
		principal, err := authorizePagePermissionAfterRootLock(
			ctx, tx, s.internal.spiceDB, pageID, policyv1.Page.View,
		)
		if err != nil {
			return err
		}
		state, err = s.loadAIDocumentStateAfterAuthorization(
			ctx, tx, root, documentID, locale, principal.MemberID.String(),
		)
		return err
	})
	return state, err
}

func validatePageAIDocumentIdentity(pageID, locale string) error {
	if _, err := parsePageContentUUID("page_id", pageID); err != nil {
		return err
	}
	_, err := normalizePageDocumentLocale(locale)
	return err
}

func (s *AIDocumentService) loadAIDocumentStateAfterAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	root model.Page,
	documentID uuid.UUID,
	locale string,
	viewerMemberID string,
) (AIDocumentState, error) {
	state, err := loadPageTargetLocaleState(ctx, tx, s.internal.contentBlocks, root.ID, documentID, locale, false)
	if err != nil {
		return AIDocumentState{}, err
	}
	document, err := contentblock.SnapshotToLocalizedPageDocument(state.Snapshot, locale)
	if err != nil {
		return AIDocumentState{}, normalizePageContentBlockError(err)
	}
	return AIDocumentState{
		Page: root, DocumentID: documentID, Revision: state.Snapshot.Document.Revision.String(),
		TargetRevision: clonePageTargetRevision(state.TargetRevision),
		SourceLocale:   state.SourceLocale,
		Locale:         locale, LocaleExists: state.TargetMetadata != nil,
		Title: stateMetadataTitle(state.TargetMetadata), Summary: stateMetadataSummary(state.TargetMetadata),
		Snapshot: state.Snapshot, Document: document, ViewerMemberID: viewerMemberID,
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

func (s *AIDocumentService) Apply(ctx context.Context, mutation AIDocumentMutation) (contentblock.Result, error) {
	output, err := s.ExecuteAIDocumentMutation(
		ctx, mutation.PageID, mutation.Locale, AIDocumentExecutionApply,
		func(AIDocumentState) (AIDocumentMutation, error) { return mutation, nil },
	)
	if err != nil {
		return contentblock.Result{}, err
	}
	revision, err := uuid.Parse(output.DocumentRevision)
	if err != nil {
		return contentblock.Result{}, err
	}
	return contentblock.Result{DocumentRevision: revision, Changed: output.Changed}, nil
}

func (s *AIDocumentService) Validate(ctx context.Context, mutation AIDocumentMutation) error {
	_, err := s.ExecuteAIDocumentMutation(
		ctx, mutation.PageID, mutation.Locale, AIDocumentExecutionValidate,
		func(AIDocumentState) (AIDocumentMutation, error) { return mutation, nil },
	)
	return err
}

func (s *AIDocumentService) applyAIDocumentMutationInTransaction(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	fence contentblock.DomainFence,
) (contentblock.Result, *string, error) {
	if _, err := parsePageContentUUID("page_id", mutation.PageID); err != nil {
		return contentblock.Result{}, nil, err
	}
	if mutation.ExpectedRevision == uuid.Nil || mutation.Batch.DocumentID == uuid.Nil {
		return contentblock.Result{}, nil, errs.InvalidArgument("expected_revision", "is required")
	}
	contributorIDs := pageAIContributorIDs(mutation.ContributorMemberID)
	if len(contributorIDs) != 1 || len(mutation.Batch.ContributorMemberIDs) != 1 ||
		mutation.Batch.ContributorMemberIDs[0] != contributorIDs[0] {
		return contentblock.Result{}, nil, errs.InvalidArgument("contributor", "compiled attribution must match the current Member")
	}
	if mutation.Batch.ExpectedRevision != mutation.ExpectedRevision {
		return contentblock.Result{}, nil, errs.InvalidArgument("expected_revision", "does not match the compiled section batch")
	}
	if strings.TrimSpace(mutation.ObservedSourceLocale) == "" {
		return contentblock.Result{}, nil, errs.InvalidArgument("source_locale", "observed source locale is required")
	}
	if mutation.Locale != mutation.ObservedSourceLocale {
		if len(mutation.Batch.Upserts) != 0 || len(mutation.Batch.Deletes) != 0 || len(mutation.Batch.Reorders) != 0 {
			return contentblock.Result{}, nil, errs.InvalidArgument("operations", "non-source locale cannot mutate the Page section graph")
		}
		for _, group := range mutation.Batch.LocaleGroups {
			if group.Locale != mutation.Locale {
				return contentblock.Result{}, nil, errs.InvalidArgument("operations", "compiled locale mutation targets another locale")
			}
		}
	}
	if mutation.Locale == mutation.ObservedSourceLocale && (mutation.Metadata.EnsureLocale || mutation.DeleteTranslation) {
		return contentblock.Result{}, nil, errs.InvalidArgument("locale", "Page source translation lifecycle cannot be changed")
	}
	if mutation.DeleteTranslation && (mutation.Metadata != AIDocumentMetadataPatch{} || hasPageAIDocumentBatchChanges(mutation.Batch)) {
		return contentblock.Result{}, nil, errs.InvalidArgument("operations", "translation deletion must be the only mutation")
	}
	now := time.Now().UTC()
	if mutation.Locale != mutation.ObservedSourceLocale {
		result, targetRevision, err := applyPageTargetLocaleBatch(
			ctx, tx, s.internal.contentBlocks, mutation.PageID, mutation.Batch.DocumentID,
			mutation.Locale, mutation.Batch, mutation.ExpectedTargetRevision,
			pageTargetMetadataPatch{
				EnsureLocale: mutation.Metadata.EnsureLocale,
				UpdateTitle:  mutation.Metadata.SetTitle, Title: mutation.Metadata.Title,
				UpdateSummary: mutation.Metadata.SetSummary, Summary: mutation.Metadata.Summary,
				DeleteLocale: mutation.DeleteTranslation,
			}, mutation.Metadata.EnsureLocale, false, now, fence,
		)
		if err != nil || !result.Changed {
			return result, targetRevision, err
		}
		if err := tx.WithContext(ctx).Model(&model.Page{}).Where("id = ?", mutation.PageID).UpdateColumn("updated_at", now).Error; err != nil {
			return contentblock.Result{}, nil, err
		}
		return result, targetRevision, nil
	}
	if mutation.ExpectedTargetRevision != nil {
		return contentblock.Result{}, nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	result, err := s.internal.contentBlocks.ApplyBatchWithMetadata(
		ctx, tx, mutation.Batch, fence,
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			return applyPageAIDocumentMetadata(
				ctx, tx, mutation.PageID, mutation.Locale,
				mutation.ObservedLocaleExists, mutation.Metadata, now,
			)
		},
	)
	if err != nil || !result.Changed {
		return result, nil, err
	}
	if err := tx.WithContext(ctx).Model(&model.Page{}).
		Where("id = ?", mutation.PageID).UpdateColumn("updated_at", now).Error; err != nil {
		return contentblock.Result{}, nil, err
	}
	return result, nil, nil
}

func loadPageAIDocumentRoot(ctx context.Context, tx *gorm.DB, pageID, lock string) (model.Page, uuid.UUID, error) {
	var root model.Page
	query := tx.WithContext(ctx).Table("page").Where("id = ?", pageID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Page{}, uuid.Nil, errs.NotFound("page", pageID)
		}
		return model.Page{}, uuid.Nil, errs.Internal(err)
	}
	if root.ContentDocumentID == nil {
		return model.Page{}, uuid.Nil, errs.FailedPrecondition("Page content document is not initialized")
	}
	documentID, err := uuid.Parse(*root.ContentDocumentID)
	if err != nil || documentID == uuid.Nil {
		return model.Page{}, uuid.Nil, errs.InternalMsg("Page content document ID is invalid")
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
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return pageAIDocumentLocale{}, false, nil
	}
	if result.Error != nil {
		return pageAIDocumentLocale{}, false, errs.Internal(result.Error)
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
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Page translation presence changed; reload before saving")
	}
	if locale == source.SourceLocale && !exists {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Page source translation is missing")
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
		return contentblock.MetadataEffect{}, errs.InvalidArgument("title", "Page source title cannot be empty")
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
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: locale == source.SourceLocale,
		SourceLocale: source.SourceLocale, ChangedLocales: []string{locale},
	}, nil
}

func lockPageAIDocumentSource(ctx context.Context, tx *gorm.DB, pageID string) (model.Page, error) {
	var page model.Page
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
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
