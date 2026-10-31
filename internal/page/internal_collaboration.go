package page

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LoadPageBlockDocumentQuery reads one resident Page room for the browser
// session SessionID. Yjs state remains resident collaboration state and
// never crosses this boundary.
type LoadPageBlockDocumentQuery struct {
	PageID    string
	Locale    string
	SessionID string
}

// ApplyPageBlockBatchCommand saves one room's typed section batch. The
// batch names the one origin Member editor-collab derived from admitted
// frames.
type ApplyPageBlockBatchCommand struct {
	PageID                 string
	Locale                 string
	Batch                  *contentv1.PageSectionMutationBatch
	ExpectedTargetRevision *string
	AffectedLocaleValues   []*managev1.AIDocumentFieldTarget
}

// UpdatePageLocaleMetadataCommand saves the title and summary of one room's
// locale. SetSummary sets the summary and ClearSummary clears it; neither
// leaves it unchanged.
type UpdatePageLocaleMetadataCommand struct {
	PageID                 string
	Locale                 string
	ExpectedRevision       string
	ExpectedTargetRevision *string
	Title                  *string
	SetSummary             *string
	ClearSummary           bool
	ContributorMemberIDs   []string
}

// UpdatePageDocumentMetadataCommand saves the shared document layout from
// the source locale room.
type UpdatePageDocumentMetadataCommand struct {
	PageID               string
	Locale               string
	ExpectedRevision     string
	DocumentLayout       *commonv1.DocumentLayout
	ContributorMemberIDs []string
}

// CreatePageVersionCheckpointCommand checkpoints the source locale document
// the contributors saved.
type CreatePageVersionCheckpointCommand struct {
	PageID               string
	Locale               string
	ExpectedRevision     string
	ContributorMemberIDs []string
}

// lockedCollaboration is the access of an editor-collab save. editor-collab
// calls as itself: fence locks the Page root, then the contributors' current
// permission authorizes the save; the locked domain context is kept for the
// change.
func lockedCollaboration(fence contentblock.DomainFence, documentID uuid.UUID, domain *contentblock.DomainContext) mutation.Access {
	return mutation.Locked(func(ctx context.Context, tx *gorm.DB, _ mutation.Require) error {
		var err error
		*domain, err = fence(ctx, tx, documentID)
		return err
	})
}

// touchPage records a content change on the Page root.
func touchPage(ctx context.Context, tx *gorm.DB, pageID string, now time.Time) error {
	return tx.WithContext(ctx).Model(&model.Page{}).Where("id = ?", pageID).UpdateColumn("updated_at", now).Error
}

// localeContentAudit attributes a changed room locale to its origin Member.
func localeContentAudit(memberID, pageID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPageLocaleContentAuditRecord(metadata, pageID, locale, operation)
	})
}

// pageBlockDocument is the room state LoadPageBlockDocument reads in one
// snapshot.
type pageBlockDocument struct {
	state          pageTargetLocaleState
	documentLayout model.DocumentLayout
	blockMedia     []*contentv1.ContentBlockMediaItem
}

func (s *Service) LoadPageBlockDocument(ctx context.Context, query LoadPageBlockDocumentQuery) (_ *intrav1.LoadPageBlockDocumentResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.PageID))

	if _, err := parsePageContentUUID("page_id", query.PageID); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(query.Locale)
	if err != nil {
		return nil, err
	}
	var loaded pageBlockDocument
	if err := mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		var err error
		loaded, err = s.loadPageBlockDocument(ctx, tx, query, locale)
		return err
	}); err != nil {
		return nil, normalizePageContentBlockError(err)
	}

	state := loaded.state
	document, err := contentblock.MaterializeSnapshotPageLocale(state.Snapshot, locale)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentPageLocaleValues(state.Snapshot, locale)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	documentLayout, err := loaded.documentLayout.Proto()
	if err != nil {
		return nil, err
	}
	return &intrav1.LoadPageBlockDocumentResponse{
		Document: document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: &intrav1.PageLocaleMetadata{Locale: state.SourceLocale, Title: state.SourceMetadata.Title, Summary: state.SourceMetadata.Summary},
		Locale:         locale, LocaleExists: state.TargetMetadata != nil,
		DocumentLayout:      documentLayout,
		BlockMedia:          loaded.blockMedia,
		LocaleMetadata:      pageLocaleMetadataProto(state.TargetMetadata),
		TargetRevision:      clonePageTargetRevision(state.TargetRevision),
		PresentLocaleValues: presentLocaleValues,
	}, nil
}

func (s *Service) loadPageBlockDocument(ctx context.Context, tx *gorm.DB, query LoadPageBlockDocumentQuery, locale string) (pageBlockDocument, error) {
	documentID, err := loadPageContentDocumentIDForRead(ctx, tx, query.PageID)
	if err != nil {
		return pageBlockDocument{}, err
	}
	principal, err := authorizePageBlockBootstrap(ctx, tx, s.permissions, query.PageID, query.SessionID)
	if err != nil {
		return pageBlockDocument{}, err
	}
	var loaded pageBlockDocument
	if loaded.state, err = loadPageTargetLocaleState(ctx, tx, s.contentBlocks, query.PageID, documentID, locale, false); err != nil {
		return pageBlockDocument{}, err
	}
	var row struct {
		DocumentLayout model.DocumentLayout `gorm:"column:document_layout"`
	}
	if err := tx.WithContext(ctx).Table("page").Select("document_layout").Where("id = ?", query.PageID).Take(&row).Error; err != nil {
		return pageBlockDocument{}, err
	}
	loaded.documentLayout = row.DocumentLayout
	blockMedia, err := contentblock.LoadMediaReferences(ctx, tx, documentID)
	if err != nil {
		return pageBlockDocument{}, err
	}
	loaded.blockMedia, err = s.mediaHydrator.HydrateAuthorizedPageBlockMediaWithDB(
		auth.WithUser(ctx, principal), tx, query.PageID, documentID, principal, blockMedia,
	)
	if err != nil {
		return pageBlockDocument{}, err
	}
	return loaded, nil
}

// pageRoomSave is the outcome of a room save: the content result, the
// target revision of a non-source room, and whether the source room path
// ran.
type pageRoomSave struct {
	result         contentblock.Result
	targetRevision *string
	source         bool
}

func (s *Service) ApplyPageBlockBatch(ctx context.Context, command ApplyPageBlockBatchCommand) (_ *intrav1.ApplyPageBlockBatchResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if command.Batch == nil {
		return nil, domainerr.Required("batch")
	}
	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	contributors := command.Batch.ContributorMemberIds
	contributor, err := contentblock.MutationContributor(contributors)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, command.PageID)
	if err != nil {
		return nil, err
	}
	batch, err := contentblock.BatchFromPageProtoWithAffectedLocaleValues(documentID, command.Batch, locale, command.AffectedLocaleValues)
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}

	now := dbtime.Now()
	var domain contentblock.DomainContext
	saved, err := mutation.Run(ctx, s.mutations, mutation.Spec[pageRoomSave]{
		Access: lockedCollaboration(s.pageCollaborationDocumentFence(command.PageID, policyv1.Page.Edit, contributors), documentID, &domain),
		Change: func(ctx context.Context, tx *gorm.DB) (pageRoomSave, error) {
			saved, err := s.savePageRoomBatch(ctx, tx, command, documentID, locale, batch, domain, now)
			if err != nil || !saved.result.Changed {
				return saved, err
			}
			return saved, touchPage(ctx, tx, command.PageID, now)
		},
		Audit: func(saved pageRoomSave) mutation.Audit {
			if !saved.result.Changed {
				return mutation.Unchanged()
			}
			return localeContentAudit(contributor, command.PageID, locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, saved pageRoomSave) error {
			if !saved.result.Changed {
				return nil
			}
			return s.publish(ctx, tx, buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             command.PageID,
				Fields:               []string{"content"},
				DocumentRevision:     saved.result.DocumentRevision.String(),
				Contributors:         contributors,
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               locale,
				TargetRevision:       saved.targetRevision,
				DocumentStateChanged: saved.source,
			}))
		},
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	return &intrav1.ApplyPageBlockBatchResponse{
		DocumentRevision: saved.result.DocumentRevision.String(), Changed: saved.result.Changed,
		SourceChanged:  saved.result.TranslationSourceChanged,
		ChangedLocales: saved.result.ChangedLocales, Locale: locale,
		TargetRevision: saved.targetRevision,
	}, nil
}

// savePageRoomBatch applies the batch of a source room to the shared
// document, or of a target room to its locale.
func (s *Service) savePageRoomBatch(
	ctx context.Context,
	tx *gorm.DB,
	command ApplyPageBlockBatchCommand,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	domain contentblock.DomainContext,
	now time.Time,
) (pageRoomSave, error) {
	fence := contentblock.LockedFence(pageEntityName, documentID, domain)
	if locale != domain.SourceLocale {
		result, targetRevision, err := applyPageTargetLocaleMutation(ctx, tx, s.contentBlocks, pageTargetLocaleMutationInput{
			PageID: command.PageID, DocumentID: documentID, Locale: locale, Batch: batch,
			ExpectedTargetRevision: command.ExpectedTargetRevision, Now: now, Fence: fence,
		})
		return pageRoomSave{result: result, targetRevision: targetRevision}, err
	}
	if command.ExpectedTargetRevision != nil {
		return pageRoomSave{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	if err := validatePageSourceLocaleMutations(command.PageID, domain.SourceLocale, batch.LocaleGroups); err != nil {
		return pageRoomSave{}, err
	}
	result, err := s.contentBlocks.ApplyBatch(ctx, tx, batch, pageSourceRoomFence(locale, fence))
	return pageRoomSave{result: result, source: true}, err
}

func pageLocaleMetadataProto(row *pageLocaleMetadataRow) *intrav1.PageLocaleMetadata {
	if row == nil {
		return nil
	}
	return &intrav1.PageLocaleMetadata{Locale: row.Locale, Title: cloneOptionalString(row.Title), Summary: cloneOptionalString(row.Summary)}
}

func clonePageTargetRevision(value string) *string {
	if value == "" {
		return nil
	}
	cloned := value
	return &cloned
}

type pageLocaleMetadataMutationPlan struct {
	Locale        string
	UpdateTitle   bool
	Title         *string
	UpdateSummary bool
	Summary       *string
}

type pageLocaleMetadataMutationResult struct {
	Effect         contentblock.MetadataEffect
	Title          *string
	Summary        *string
	TitleChanged   bool
	SummaryChanged bool
}

func planPageLocaleMetadataMutation(
	command UpdatePageLocaleMetadataCommand,
	locale string,
	isSource bool,
) (pageLocaleMetadataMutationPlan, error) {
	plan := pageLocaleMetadataMutationPlan{Locale: locale}
	if command.Title != nil {
		title := *command.Title
		if isSource {
			title = strings.TrimSpace(title)
		}
		if isSource && title == "" {
			return pageLocaleMetadataMutationPlan{}, domainerr.InvalidField("title", "must not be empty")
		}
		plan.UpdateTitle = true
		plan.Title = &title
	}
	switch {
	case command.SetSummary != nil:
		plan.UpdateSummary = true
		plan.Summary = cloneOptionalString(command.SetSummary)
	case command.ClearSummary:
		plan.UpdateSummary = true
	}
	if !plan.UpdateTitle && !plan.UpdateSummary {
		return pageLocaleMetadataMutationPlan{}, domainerr.InvalidField("metadata", "title or summary_change is required")
	}
	return plan, nil
}

// pageMetadataSave is the outcome of a room metadata save.
type pageMetadataSave struct {
	changed          bool
	documentRevision string
	sourceChanged    bool
	targetRevision   *string
	source           bool
}

func (s *Service) UpdatePageLocaleMetadata(ctx context.Context, command UpdatePageLocaleMetadataCommand) (_ *intrav1.UpdatePageLocaleMetadataResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	contributor, err := contentblock.MutationContributor(command.ContributorMemberIDs)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, command.PageID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := dbtime.Now()
	var domain contentblock.DomainContext
	saved, err := mutation.Run(ctx, s.mutations, mutation.Spec[pageMetadataSave]{
		Access: lockedCollaboration(s.pageCollaborationDocumentFence(command.PageID, policyv1.Page.Edit, command.ContributorMemberIDs), documentID, &domain),
		Change: func(ctx context.Context, tx *gorm.DB) (pageMetadataSave, error) {
			plan, err := planPageLocaleMetadataMutation(command, locale, locale == domain.SourceLocale)
			if err != nil {
				return pageMetadataSave{}, err
			}
			fence := contentblock.LockedFence(pageEntityName, documentID, domain)
			if locale != domain.SourceLocale {
				return s.saveTargetLocaleMetadata(ctx, tx, command, documentID, expectedRevision, plan, fence, now)
			}
			return s.saveSourceLocaleMetadata(ctx, tx, command, documentID, expectedRevision, plan, fence, now)
		},
		Audit: func(saved pageMetadataSave) mutation.Audit {
			if !saved.changed {
				return mutation.Unchanged()
			}
			return localeContentAudit(contributor, command.PageID, locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, saved pageMetadataSave) error {
			if !saved.changed {
				return nil
			}
			fields := make([]string, 0, 2)
			if command.Title != nil {
				fields = append(fields, "title")
			}
			if command.SetSummary != nil || command.ClearSummary {
				fields = append(fields, "summary")
			}
			return s.publish(ctx, tx, buildPageLocaleContentUpdatedEvent(contentsignal.LocaleChange{
				EntityID:             command.PageID,
				Fields:               fields,
				DocumentRevision:     saved.documentRevision,
				Contributors:         command.ContributorMemberIDs,
				Source:               managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				Locale:               locale,
				TargetRevision:       saved.targetRevision,
				DocumentStateChanged: saved.source,
			}))
		},
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	var changedLocales []string
	if saved.changed {
		changedLocales = []string{locale}
	}
	return &intrav1.UpdatePageLocaleMetadataResponse{
		DocumentRevision: saved.documentRevision, Changed: saved.changed, SourceChanged: saved.sourceChanged,
		ChangedLocales: changedLocales, Locale: locale,
		TargetRevision: saved.targetRevision,
	}, nil
}

// saveTargetLocaleMetadata writes the title and summary of a target room
// and derives its next target revision.
func (s *Service) saveTargetLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	command UpdatePageLocaleMetadataCommand,
	documentID, expectedRevision uuid.UUID,
	plan pageLocaleMetadataMutationPlan,
	fence contentblock.DomainFence,
	now time.Time,
) (pageMetadataSave, error) {
	contributors, err := pageContributorUUIDs(command.ContributorMemberIDs)
	if err != nil {
		return pageMetadataSave{}, err
	}
	result, targetRevision, err := applyPageTargetLocaleMutation(ctx, tx, s.contentBlocks, pageTargetLocaleMutationInput{
		PageID: command.PageID, DocumentID: documentID, Locale: plan.Locale,
		Batch:                  contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision, ContributorMemberIDs: contributors},
		ExpectedTargetRevision: command.ExpectedTargetRevision,
		Patch:                  pageTargetMetadataPatch{UpdateTitle: plan.UpdateTitle, Title: plan.Title, UpdateSummary: plan.UpdateSummary, Summary: plan.Summary},
		Now:                    now, Fence: fence,
	})
	if err != nil {
		return pageMetadataSave{}, err
	}
	saved := pageMetadataSave{changed: result.Changed, documentRevision: result.DocumentRevision.String(), targetRevision: targetRevision}
	if !result.Changed {
		return saved, nil
	}
	if err := touchPage(ctx, tx, command.PageID, now); err != nil {
		return pageMetadataSave{}, err
	}
	if plan.UpdateTitle {
		if err := s.requestLocaleMetadataOG(ctx, tx, command.PageID, plan.Locale); err != nil {
			return pageMetadataSave{}, err
		}
	}
	return saved, nil
}

// saveSourceLocaleMetadata writes the title and summary of the source room
// and advances the shared document revision.
func (s *Service) saveSourceLocaleMetadata(
	ctx context.Context,
	tx *gorm.DB,
	command UpdatePageLocaleMetadataCommand,
	documentID, expectedRevision uuid.UUID,
	plan pageLocaleMetadataMutationPlan,
	fence contentblock.DomainFence,
	now time.Time,
) (pageMetadataSave, error) {
	if command.ExpectedTargetRevision != nil {
		return pageMetadataSave{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	var metadata pageLocaleMetadataMutationResult
	advance, err := s.contentBlocks.AdvanceRevision(
		ctx, tx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		pageSourceRoomFence(plan.Locale, fence),
		func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
			var err error
			metadata, err = applyPageLocaleMetadataMutation(ctx, tx, command.PageID, plan, now)
			return metadata.Effect, err
		},
	)
	if err != nil {
		return pageMetadataSave{}, err
	}
	saved := pageMetadataSave{
		changed: advance.Changed, documentRevision: advance.DocumentRevision.String(),
		sourceChanged: advance.TranslationSourceChanged, source: true,
	}
	if !advance.Changed {
		return saved, nil
	}
	if err := touchPage(ctx, tx, command.PageID, now); err != nil {
		return pageMetadataSave{}, err
	}
	if metadata.TitleChanged {
		if err := s.requestLocaleMetadataOG(ctx, tx, command.PageID, plan.Locale); err != nil {
			return pageMetadataSave{}, err
		}
	}
	return saved, nil
}

// requestLocaleMetadataOG requests the OG image of a room locale whose
// title was saved.
func (s *Service) requestLocaleMetadataOG(ctx context.Context, tx *gorm.DB, pageID, locale string) error {
	_, err := s.runtime.RequestCurrentWithDB(
		ctx, tx, managev1.OgEntityType_OG_ENTITY_TYPE_PAGE, pageID, locale, false, "page_locale_metadata_saved",
	)
	return err
}

// pageLocaleMetadataTexts is the title and summary of a locale metadata row.
type pageLocaleMetadataTexts struct {
	Title   *string `gorm:"column:title"`
	Summary *string `gorm:"column:summary"`
}

func applyPageLocaleMetadataMutation(
	ctx context.Context,
	tx *gorm.DB,
	pageID string,
	plan pageLocaleMetadataMutationPlan,
	now time.Time,
) (pageLocaleMetadataMutationResult, error) {
	row, err := lockSourceLocaleMetadataTexts(ctx, tx, pageID, plan.Locale)
	if err != nil {
		return pageLocaleMetadataMutationResult{}, err
	}
	nextTitle := cloneOptionalString(row.Title)
	nextSummary := cloneOptionalString(row.Summary)
	if plan.UpdateTitle {
		nextTitle = cloneOptionalString(plan.Title)
	}
	if plan.UpdateSummary {
		nextSummary = cloneOptionalString(plan.Summary)
	}
	titleChanged := plan.UpdateTitle && !nullableStringEqual(row.Title, nextTitle)
	summaryChanged := plan.UpdateSummary && !nullableStringEqual(row.Summary, nextSummary)
	changed := titleChanged || summaryChanged
	output := pageLocaleMetadataMutationResult{
		Effect:         contentblock.MetadataEffect{Changed: changed, AffectsTranslationSource: changed},
		Title:          nextTitle,
		Summary:        nextSummary,
		TitleChanged:   titleChanged,
		SummaryChanged: summaryChanged,
	}
	if !changed {
		return output, nil
	}
	updates := structured.Fields{"updated_at": now}
	if titleChanged {
		updates["title"] = nextTitle
	}
	if summaryChanged {
		updates["summary"] = nextSummary
	}
	update := tx.WithContext(ctx).
		Table("page_translation").
		Where("entity_id = ? AND locale = ?", pageID, plan.Locale).
		Updates(updates)
	if update.Error != nil {
		return pageLocaleMetadataMutationResult{}, update.Error
	}
	if update.RowsAffected != 1 {
		return pageLocaleMetadataMutationResult{}, domainerr.Precondition("Page locale metadata changed; reload before saving")
	}
	return output, nil
}

// lockSourceLocaleMetadataTexts locks the Page root and the metadata row of
// its source locale; locale must still be the source locale.
func lockSourceLocaleMetadataTexts(ctx context.Context, tx *gorm.DB, pageID, locale string) (pageLocaleMetadataTexts, error) {
	var root struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("page").Select("source_locale").Where("id = ?", pageID).
		Take(&root).Error; err != nil {
		return pageLocaleMetadataTexts{}, err
	}
	if locale != root.SourceLocale {
		return pageLocaleMetadataTexts{}, domainerr.Precondition("target Page locale metadata is read-only")
	}
	var row pageLocaleMetadataTexts
	result := tx.WithContext(ctx).Raw(`
		SELECT title, summary
		FROM page_translation
		WHERE entity_id = ? AND locale = ?
		FOR UPDATE
	`, pageID, locale).Scan(&row)
	if result.Error != nil {
		return pageLocaleMetadataTexts{}, result.Error
	}
	if result.RowsAffected == 0 {
		return pageLocaleMetadataTexts{}, domainerr.ResourceNotFound("page_translation", pageID+":"+locale)
	}
	return row, nil
}

func (s *Service) UpdatePageDocumentMetadata(ctx context.Context, command UpdatePageDocumentMetadataCommand) (_ *intrav1.UpdatePageDocumentMetadataResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	nextLayout, err := model.DocumentLayoutFromProto(command.DocumentLayout)
	if err != nil {
		return nil, domainerr.InvalidField("document_layout", err.Error())
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, command.PageID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	var domain contentblock.DomainContext
	advance, err := mutation.Run(ctx, s.mutations, mutation.Spec[contentblock.AdvanceResult]{
		Access: lockedCollaboration(pageSourceRoomFence(locale, s.pageCollaborationDocumentFence(
			command.PageID, policyv1.Page.Manage, command.ContributorMemberIDs,
		)), documentID, &domain),
		Change: func(ctx context.Context, tx *gorm.DB) (contentblock.AdvanceResult, error) {
			return s.contentBlocks.AdvanceRevision(ctx, tx,
				contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
				contentblock.LockedFence(pageEntityName, documentID, domain),
				func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
					return applyPageDocumentLayoutMetadata(ctx, tx, command.PageID, nextLayout, now)
				},
			)
		},
		Audit: func(advance contentblock.AdvanceResult) mutation.Audit {
			if !advance.Changed {
				return mutation.Unchanged()
			}
			return pageLayoutAudit(command.PageID)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, advance contentblock.AdvanceResult) error {
			if !advance.Changed {
				return nil
			}
			return s.publish(ctx, tx, buildContentUpdatedEvent(
				managev1.ContentEntityType_CONTENT_ENTITY_TYPE_PAGE,
				command.PageID,
				managev1.ContentUpdateSource_CONTENT_UPDATE_SOURCE_COLLAB,
				buildContentUpdatedFields([]string{"documentLayout"}, pageContentUpdatedFieldSpecs),
				false,
				advance.DocumentRevision.String(),
				command.ContributorMemberIDs,
				nil,
			))
		},
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	return &intrav1.UpdatePageDocumentMetadataResponse{
		DocumentRevision: advance.DocumentRevision.String(), Changed: advance.Changed,
		SourceChanged: advance.TranslationSourceChanged, Locale: locale,
	}, nil
}

// pageLayoutAudit records a changed Page document layout.
func pageLayoutAudit(pageID string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPageConfigurationAuditRecord(metadata, pageID, []string{"document_layout"})
	})
}

// pageCheckpoint is the outcome of a version checkpoint; created is nil when
// the source document equals the previous version.
type pageCheckpoint struct {
	created  *model.PageVersion
	revision string
}

func (s *Service) CreatePageVersionCheckpoint(ctx context.Context, command CreatePageVersionCheckpointCommand) (_ *intrav1.CreatePageVersionCheckpointResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return nil, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadPageContentDocumentID(ctx, s.db, command.PageID)
	if err != nil {
		return nil, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return nil, err
	}

	var domain contentblock.DomainContext
	checkpoint, err := mutation.Run(ctx, s.mutations, mutation.Spec[pageCheckpoint]{
		Access: lockedCollaboration(pageSourceRoomFence(locale, s.pageCollaborationDocumentFence(
			command.PageID, policyv1.Page.Edit, command.ContributorMemberIDs,
		)), documentID, &domain),
		Change: func(ctx context.Context, tx *gorm.DB) (pageCheckpoint, error) {
			return s.checkpointPageVersion(ctx, tx, command, documentID, expectedRevision, domain.SourceLocale)
		},
		Audit: func(checkpoint pageCheckpoint) mutation.Audit {
			version := checkpoint.created
			if version == nil {
				return mutation.Unchanged()
			}
			return mutation.VersionAudit(sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewPageVersionCreatedAuditRecord(metadata, command.PageID, version.ID, version.ContributorMemberIDs)
			})
		},
	})
	if err != nil {
		return nil, normalizePageContentBlockError(err)
	}
	response := &intrav1.CreatePageVersionCheckpointResponse{
		Created:  checkpoint.created != nil,
		Revision: checkpoint.revision,
		Locale:   locale,
	}
	if checkpoint.created != nil {
		response.VersionId = &checkpoint.created.ID
	}
	return response, nil
}

// checkpointPageVersion stores the source document at expectedRevision as
// the next version unless it equals the latest one.
func (s *Service) checkpointPageVersion(
	ctx context.Context,
	tx *gorm.DB,
	command CreatePageVersionCheckpointCommand,
	documentID, expectedRevision uuid.UUID,
	sourceLocale string,
) (pageCheckpoint, error) {
	snapshot, err := s.contentBlocks.LoadSnapshotInTransaction(ctx, tx, documentID, sourceLocale)
	if err != nil {
		return pageCheckpoint{}, err
	}
	if snapshot.Document.Revision != expectedRevision {
		return pageCheckpoint{}, &contentblock.StaleRevisionError{CurrentRevision: snapshot.Document.Revision}
	}
	checkpoint := pageCheckpoint{revision: snapshot.Document.Revision.String()}
	metadata, err := loadRequiredPageSourceLocaleMetadata(ctx, tx, command.PageID)
	if err != nil {
		return pageCheckpoint{}, err
	}
	document, err := MaterializeLocalizedPageContentDocument(snapshot, sourceLocale)
	if err != nil {
		return pageCheckpoint{}, err
	}
	encoded, err := EncodeVersionContentSnapshot(sourceLocale, metadata.Title, metadata.Summary, document)
	if err != nil {
		return pageCheckpoint{}, err
	}
	nextVersion, unchanged, err := nextPageVersion(ctx, tx, command.PageID, encoded)
	if err != nil || unchanged {
		return checkpoint, err
	}
	checkpoint.created = &model.PageVersion{
		PageID:               command.PageID,
		Version:              nextVersion,
		Title:                cloneOptionalString(metadata.Title),
		Summary:              cloneOptionalString(metadata.Summary),
		ContentSnapshot:      encoded,
		ContributorMemberIDs: normalizeContributorMemberIDs(command.ContributorMemberIDs),
		CreatedAt:            time.Now().UTC(),
	}
	if err := tx.WithContext(ctx).Create(checkpoint.created).Error; err != nil {
		return pageCheckpoint{}, err
	}
	return checkpoint, nil
}

// nextPageVersion locks the latest version of the Page and returns the
// number after it, or unchanged when the latest version already stores
// encoded.
func nextPageVersion(ctx context.Context, tx *gorm.DB, pageID string, encoded []byte) (int32, bool, error) {
	var previous model.PageVersion
	previousResult := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("page_id = ?", pageID).
		Order("version DESC").
		First(&previous)
	if errors.Is(previousResult.Error, gorm.ErrRecordNotFound) {
		return 1, false, nil
	}
	if previousResult.Error != nil {
		return 0, false, previousResult.Error
	}
	if !staleCatalogSnapshot(previous.ContentSnapshot) {
		previousSnapshot, err := DecodeVersionContentSnapshot(previous.ContentSnapshot)
		if err != nil {
			return 0, false, err
		}
		canonicalPrevious, err := EncodeVersionContentSnapshot(
			previousSnapshot.SourceLocale, previousSnapshot.Title, previousSnapshot.Summary, previousSnapshot.Document,
		)
		if err != nil {
			return 0, false, err
		}
		if bytes.Equal(canonicalPrevious, encoded) {
			return 0, true, nil
		}
	}
	return previous.Version + 1, false, nil
}

// applyPageDocumentLayoutMetadata stores nextLayout on the Page root; the
// caller audits a change.
func applyPageDocumentLayoutMetadata(ctx context.Context, tx *gorm.DB, pageID string, nextLayout model.DocumentLayout, now time.Time) (contentblock.MetadataEffect, error) {
	var row struct {
		DocumentLayout model.DocumentLayout `gorm:"column:document_layout"`
	}
	if err := tx.WithContext(ctx).
		Table("page").
		Select("document_layout").
		Where("id = ?", pageID).
		Take(&row).Error; err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if row.DocumentLayout == nextLayout {
		return contentblock.MetadataEffect{}, nil
	}
	if err := tx.WithContext(ctx).Model(&model.Page{}).
		Where("id = ?", pageID).
		Updates(structured.Fields{
			"document_layout": nextLayout,
			"updated_at":      now,
		}).Error; err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true}, nil
}
