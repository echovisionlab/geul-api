package page

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
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
	PageID           string
	Locale           string
	ExpectedRevision string
	// DocumentLayout is the requested layout as the API named it, nil when
	// the request names none.
	DocumentLayout       *DocumentLayout
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
func lockedCollaboration(fence ContentFence, documentID uuid.UUID, domain *contentblock.DomainContext) mutation.Access {
	return mutation.Locked(func(ctx context.Context, store Store, _ mutation.Require) error {
		var err error
		*domain, err = fence(ctx, store, documentID)
		return err
	})
}

// localeContentAudit attributes a changed room locale to its origin Member.
func localeContentAudit(memberID, pageID, locale string, operation sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditPageUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPageLocaleContentAuditRecord(metadata, pageID, locale, operation)
	})
}

// LocaleMetadata is the title and summary of one Page locale a room shows.
type LocaleMetadata struct {
	Locale  string
	Title   *string
	Summary *string
}

// RoomDocument is a collaboration room's Page document: the current source
// graph with the room locale's values overlaid, its locale metadata, the
// document layout and the Block media the session may receive.
type RoomDocument struct {
	Document            *contentv1.LocalizedPageDocument
	DocumentRevision    string
	SourceMetadata      LocaleMetadata
	Locale              string
	LocaleExists        bool
	DocumentLayout      DocumentLayout
	BlockMedia          []*contentv1.ContentBlockMediaItem
	LocaleMetadata      *LocaleMetadata
	TargetRevision      *string
	PresentLocaleValues []*managev1.AIDocumentFieldTarget
}

// pageBlockDocument is the room state LoadPageBlockDocument reads in one
// snapshot.
type pageBlockDocument struct {
	state          pageTargetLocaleState
	documentLayout DocumentLayout
	blockMedia     []*contentv1.ContentBlockMediaItem
}

func (s *Service) LoadPageBlockDocument(ctx context.Context, query LoadPageBlockDocumentQuery) (_ RoomDocument, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.PageID))

	if _, err := parsePageContentUUID("page_id", query.PageID); err != nil {
		return RoomDocument{}, err
	}
	locale, err := normalizePageDocumentLocale(query.Locale)
	if err != nil {
		return RoomDocument{}, err
	}
	var loaded pageBlockDocument
	// A session or Identity change that commits while the snapshot waits at
	// the identity fence aborts it (SQLSTATE 40001); the read is retried and
	// then answers from the committed state.
	if err := dberrors.RetrySerializationFailure(ctx, func() error {
		return s.units.ReadLocked(ctx, func(store Store) error {
			var err error
			loaded, err = s.loadPageBlockDocument(ctx, store, query, locale)
			return err
		})
	}); err != nil {
		return RoomDocument{}, normalizePageContentBlockError(err)
	}

	state := loaded.state
	document, err := contentblock.MaterializeSnapshotPageLocale(state.Snapshot, locale)
	if err != nil {
		return RoomDocument{}, normalizePageContentBlockError(err)
	}
	presentLocaleValues, err := contentblock.PresentPageLocaleValues(state.Snapshot, locale)
	if err != nil {
		return RoomDocument{}, normalizePageContentBlockError(err)
	}
	return RoomDocument{
		Document: document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: LocaleMetadata{Locale: state.SourceLocale, Title: state.SourceMetadata.Title, Summary: state.SourceMetadata.Summary},
		Locale:         locale, LocaleExists: state.TargetMetadata != nil,
		DocumentLayout:      loaded.documentLayout,
		BlockMedia:          loaded.blockMedia,
		LocaleMetadata:      roomLocaleMetadata(state.TargetMetadata),
		TargetRevision:      clonePageTargetRevision(state.TargetRevision),
		PresentLocaleValues: presentLocaleValues,
	}, nil
}

func (s *Service) loadPageBlockDocument(ctx context.Context, store Store, query LoadPageBlockDocumentQuery, locale string) (pageBlockDocument, error) {
	documentID, err := loadPageContentDocumentID(ctx, store, query.PageID, ShareLock)
	if err != nil {
		return pageBlockDocument{}, err
	}
	principal, err := authorizePageBlockBootstrap(ctx, store, s.spiceDB, query.PageID, query.SessionID)
	if err != nil {
		return pageBlockDocument{}, err
	}
	var loaded pageBlockDocument
	if loaded.state, err = loadPageTargetLocaleState(ctx, store, query.PageID, documentID, locale, false); err != nil {
		return pageBlockDocument{}, err
	}
	if loaded.documentLayout, err = store.DocumentLayout(ctx, query.PageID); err != nil {
		return pageBlockDocument{}, err
	}
	blockMedia, err := store.MediaReferences(ctx, documentID)
	if err != nil {
		return pageBlockDocument{}, err
	}
	loaded.blockMedia, err = store.HydrateBlockMedia(auth.WithUser(ctx, principal), query.PageID, documentID, principal, blockMedia)
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

// RoomSave is the outcome of a room content save.
type RoomSave struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	ChangedLocales   []string
	Locale           string
	TargetRevision   *string
}

func (s *Service) ApplyPageBlockBatch(ctx context.Context, command ApplyPageBlockBatchCommand) (_ RoomSave, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if command.Batch == nil {
		return RoomSave{}, domainerr.Required("batch")
	}
	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return RoomSave{}, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	contributors := command.Batch.ContributorMemberIds
	contributor, err := contentblock.MutationContributor(contributors)
	if err != nil {
		return RoomSave{}, err
	}
	documentID, err := s.readPageContentDocumentID(ctx, command.PageID)
	if err != nil {
		return RoomSave{}, err
	}
	batch, err := contentblock.BatchFromPageProtoWithAffectedLocaleValues(documentID, command.Batch, locale, command.AffectedLocaleValues)
	if err != nil {
		return RoomSave{}, normalizePageContentBlockError(err)
	}

	now := dbtime.Now()
	var domain contentblock.DomainContext
	saved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[pageRoomSave, Store]{
		Access: lockedCollaboration(s.pageCollaborationDocumentFence(command.PageID, policyv1.Page.Edit, contributors), documentID, &domain),
		Change: func(ctx context.Context, store Store) (pageRoomSave, error) {
			saved, err := savePageRoomBatch(ctx, store, command, documentID, locale, batch, domain, now)
			if err != nil || !saved.result.Changed {
				return saved, err
			}
			return saved, store.TouchPage(ctx, command.PageID, now)
		},
		Audit: func(saved pageRoomSave) mutation.Audit {
			if !saved.result.Changed {
				return mutation.Unchanged()
			}
			return localeContentAudit(contributor, command.PageID, locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, store Store, saved pageRoomSave) error {
			if !saved.result.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: command.PageID, Kind: LocaleUpdate, Locale: LocaleUpdateChange{
				Fields:               []string{"content"},
				DocumentRevision:     saved.result.DocumentRevision.String(),
				Contributors:         contributors,
				Source:               SourceCollaboration,
				Locale:               locale,
				TargetRevision:       saved.targetRevision,
				DocumentStateChanged: saved.source,
			}})
		},
	})
	if err != nil {
		return RoomSave{}, normalizePageContentBlockError(err)
	}
	return RoomSave{
		DocumentRevision: saved.result.DocumentRevision.String(), Changed: saved.result.Changed,
		SourceChanged:  saved.result.TranslationSourceChanged,
		ChangedLocales: saved.result.ChangedLocales, Locale: locale,
		TargetRevision: saved.targetRevision,
	}, nil
}

// savePageRoomBatch applies the batch of a source room to the shared
// document, or of a target room to its locale.
func savePageRoomBatch(
	ctx context.Context,
	store Store,
	command ApplyPageBlockBatchCommand,
	documentID uuid.UUID,
	locale string,
	batch contentblock.Batch,
	domain contentblock.DomainContext,
	now time.Time,
) (pageRoomSave, error) {
	fence := lockedContentFence(documentID, domain)
	if locale != domain.SourceLocale {
		result, targetRevision, err := applyPageTargetLocaleMutation(ctx, store, pageTargetLocaleMutationInput{
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
	result, err := store.ApplyContentBatch(ctx, batch, pageSourceRoomFence(locale, fence))
	return pageRoomSave{result: result, source: true}, err
}

func roomLocaleMetadata(row *Locale) *LocaleMetadata {
	if row == nil {
		return nil
	}
	return &LocaleMetadata{Locale: row.Locale, Title: cloneOptionalString(row.Title), Summary: cloneOptionalString(row.Summary)}
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

func (s *Service) UpdatePageLocaleMetadata(ctx context.Context, command UpdatePageLocaleMetadataCommand) (_ RoomSave, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return RoomSave{}, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return RoomSave{}, err
	}
	contributor, err := contentblock.MutationContributor(command.ContributorMemberIDs)
	if err != nil {
		return RoomSave{}, err
	}
	documentID, err := s.readPageContentDocumentID(ctx, command.PageID)
	if err != nil {
		return RoomSave{}, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return RoomSave{}, err
	}

	now := dbtime.Now()
	var domain contentblock.DomainContext
	saved, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[pageMetadataSave, Store]{
		Access: lockedCollaboration(s.pageCollaborationDocumentFence(command.PageID, policyv1.Page.Edit, command.ContributorMemberIDs), documentID, &domain),
		Change: func(ctx context.Context, store Store) (pageMetadataSave, error) {
			plan, err := planPageLocaleMetadataMutation(command, locale, locale == domain.SourceLocale)
			if err != nil {
				return pageMetadataSave{}, err
			}
			fence := lockedContentFence(documentID, domain)
			if locale != domain.SourceLocale {
				return saveTargetLocaleMetadata(ctx, store, command, documentID, expectedRevision, plan, fence, now)
			}
			return saveSourceLocaleMetadata(ctx, store, command, documentID, expectedRevision, plan, fence, now)
		},
		Audit: func(saved pageMetadataSave) mutation.Audit {
			if !saved.changed {
				return mutation.Unchanged()
			}
			return localeContentAudit(contributor, command.PageID, locale, sharedtelemetry.AuditItemOperationUpdated)
		},
		Outbox: func(ctx context.Context, store Store, saved pageMetadataSave) error {
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
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: command.PageID, Kind: LocaleUpdate, Locale: LocaleUpdateChange{
				Fields:               fields,
				DocumentRevision:     saved.documentRevision,
				Contributors:         command.ContributorMemberIDs,
				Source:               SourceCollaboration,
				Locale:               locale,
				TargetRevision:       saved.targetRevision,
				DocumentStateChanged: saved.source,
			}})
		},
	})
	if err != nil {
		return RoomSave{}, normalizePageContentBlockError(err)
	}
	var changedLocales []string
	if saved.changed {
		changedLocales = []string{locale}
	}
	return RoomSave{
		DocumentRevision: saved.documentRevision, Changed: saved.changed, SourceChanged: saved.sourceChanged,
		ChangedLocales: changedLocales, Locale: locale,
		TargetRevision: saved.targetRevision,
	}, nil
}

// saveTargetLocaleMetadata writes the title and summary of a target room
// and derives its next target revision.
func saveTargetLocaleMetadata(
	ctx context.Context,
	store Store,
	command UpdatePageLocaleMetadataCommand,
	documentID, expectedRevision uuid.UUID,
	plan pageLocaleMetadataMutationPlan,
	fence ContentFence,
	now time.Time,
) (pageMetadataSave, error) {
	contributors, err := pageContributorUUIDs(command.ContributorMemberIDs)
	if err != nil {
		return pageMetadataSave{}, err
	}
	result, targetRevision, err := applyPageTargetLocaleMutation(ctx, store, pageTargetLocaleMutationInput{
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
	if err := store.TouchPage(ctx, command.PageID, now); err != nil {
		return pageMetadataSave{}, err
	}
	if plan.UpdateTitle {
		if err := requestLocaleMetadataOG(ctx, store, command.PageID, plan.Locale); err != nil {
			return pageMetadataSave{}, err
		}
	}
	return saved, nil
}

// saveSourceLocaleMetadata writes the title and summary of the source room
// and advances the shared document revision.
func saveSourceLocaleMetadata(
	ctx context.Context,
	store Store,
	command UpdatePageLocaleMetadataCommand,
	documentID, expectedRevision uuid.UUID,
	plan pageLocaleMetadataMutationPlan,
	fence ContentFence,
	now time.Time,
) (pageMetadataSave, error) {
	if command.ExpectedTargetRevision != nil {
		return pageMetadataSave{}, domainerr.InvalidField("expected_target_revision", "must be omitted for the source locale")
	}
	var metadata pageLocaleMetadataMutationResult
	advance, err := store.AdvanceContentRevision(ctx,
		contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
		pageSourceRoomFence(plan.Locale, fence),
		func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
			var err error
			metadata, err = applyPageLocaleMetadataMutation(ctx, store, command.PageID, plan, now)
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
	if err := store.TouchPage(ctx, command.PageID, now); err != nil {
		return pageMetadataSave{}, err
	}
	if metadata.TitleChanged {
		if err := requestLocaleMetadataOG(ctx, store, command.PageID, plan.Locale); err != nil {
			return pageMetadataSave{}, err
		}
	}
	return saved, nil
}

// requestLocaleMetadataOG requests the OG image of a room locale whose
// title was saved.
func requestLocaleMetadataOG(ctx context.Context, store Store, pageID, locale string) error {
	_, err := store.RequestOG(ctx, pageID, locale, false, "page_locale_metadata_saved")
	return err
}

func applyPageLocaleMetadataMutation(
	ctx context.Context,
	store Store,
	pageID string,
	plan pageLocaleMetadataMutationPlan,
	now time.Time,
) (pageLocaleMetadataMutationResult, error) {
	row, err := lockSourceLocaleMetadataTexts(ctx, store, pageID, plan.Locale)
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
	exists, err := store.UpdateLocale(ctx, pageID, plan.Locale, LocaleChange{
		SetTitle: titleChanged, Title: nextTitle, SetSummary: summaryChanged, Summary: nextSummary, UpdatedAt: now,
	})
	if err != nil {
		return pageLocaleMetadataMutationResult{}, err
	}
	if !exists {
		return pageLocaleMetadataMutationResult{}, domainerr.Precondition("Page locale metadata changed; reload before saving")
	}
	return output, nil
}

// lockSourceLocaleMetadataTexts locks the Page root and the metadata row of
// its source locale; locale must still be the source locale.
func lockSourceLocaleMetadataTexts(ctx context.Context, store Store, pageID, locale string) (Locale, error) {
	root, err := store.DocumentRoot(ctx, pageID, UpdateLock)
	if err != nil {
		return Locale{}, err
	}
	if locale != root.SourceLocale {
		return Locale{}, domainerr.Precondition("target Page locale metadata is read-only")
	}
	row, exists, err := store.Locale(ctx, pageID, locale, UpdateLock)
	if err != nil {
		return Locale{}, err
	}
	if !exists {
		return Locale{}, domainerr.ResourceNotFound("page_translation", pageID+":"+locale)
	}
	return row, nil
}

// LayoutSave is the outcome of a room document layout save.
type LayoutSave struct {
	DocumentRevision string
	Changed          bool
	SourceChanged    bool
	Locale           string
}

func (s *Service) UpdatePageDocumentMetadata(ctx context.Context, command UpdatePageDocumentMetadataCommand) (_ LayoutSave, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return LayoutSave{}, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return LayoutSave{}, err
	}
	nextLayout, err := RequireDocumentLayout(command.DocumentLayout)
	if err != nil {
		return LayoutSave{}, domainerr.InvalidField("document_layout", err.Error())
	}
	documentID, err := s.readPageContentDocumentID(ctx, command.PageID)
	if err != nil {
		return LayoutSave{}, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return LayoutSave{}, err
	}
	contributor, err := contentblock.MutationContributor(command.ContributorMemberIDs)
	if err != nil {
		return LayoutSave{}, err
	}

	now := time.Now().UTC()
	var domain contentblock.DomainContext
	advance, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[contentblock.AdvanceResult, Store]{
		Access: lockedCollaboration(pageSourceRoomFence(locale, s.pageCollaborationDocumentFence(
			command.PageID, policyv1.Page.Manage, command.ContributorMemberIDs,
		)), documentID, &domain),
		Change: func(ctx context.Context, store Store) (contentblock.AdvanceResult, error) {
			return store.AdvanceContentRevision(ctx,
				contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
				lockedContentFence(documentID, domain),
				func(ctx context.Context, store Store) (contentblock.MetadataEffect, error) {
					return applyPageDocumentLayoutMetadata(ctx, store, command.PageID, nextLayout, now)
				},
			)
		},
		Audit: func(advance contentblock.AdvanceResult) mutation.Audit {
			if !advance.Changed {
				return mutation.Unchanged()
			}
			return collabLayoutAudit(contributor, command.PageID)
		},
		Outbox: func(ctx context.Context, store Store, advance contentblock.AdvanceResult) error {
			if !advance.Changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{PageID: command.PageID, Kind: LayoutUpdate, Locale: LocaleUpdateChange{
				DocumentRevision: advance.DocumentRevision.String(), Contributors: command.ContributorMemberIDs,
			}})
		},
	})
	if err != nil {
		return LayoutSave{}, normalizePageContentBlockError(err)
	}
	return LayoutSave{
		DocumentRevision: advance.DocumentRevision.String(), Changed: advance.Changed,
		SourceChanged: advance.TranslationSourceChanged, Locale: locale,
	}, nil
}

// pageLayoutAudit records a changed Page document layout for the request's
// member.
func pageLayoutAudit(pageID string) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditPageUpdated, pageLayoutRecord(pageID))
}

// collabLayoutAudit attributes a changed Page document layout to the room's
// contributor, because editor-collab saves it as itself.
func collabLayoutAudit(memberID, pageID string) mutation.Audit {
	return mutation.MemberAudit(memberID, sharedtelemetry.AuditPageUpdated, pageLayoutRecord(pageID))
}

func pageLayoutRecord(pageID string) func(sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
	return func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewPageConfigurationAuditRecord(metadata, pageID, []string{"document_layout"})
	}
}

// pageCheckpoint is the outcome of a version checkpoint; created is nil when
// the source document equals the previous version.
type pageCheckpoint struct {
	created  *Version
	revision string
}

// Checkpoint is the outcome of a version checkpoint: the version it created,
// if any, at the document revision it saw.
type Checkpoint struct {
	VersionID *string
	Revision  string
	Locale    string
}

func (s *Service) CreatePageVersionCheckpoint(ctx context.Context, command CreatePageVersionCheckpointCommand) (_ Checkpoint, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", command.PageID))

	if _, err := parsePageContentUUID("page_id", command.PageID); err != nil {
		return Checkpoint{}, err
	}
	locale, err := normalizePageDocumentLocale(command.Locale)
	if err != nil {
		return Checkpoint{}, err
	}
	documentID, err := s.readPageContentDocumentID(ctx, command.PageID)
	if err != nil {
		return Checkpoint{}, err
	}
	expectedRevision, err := parsePageContentUUID("expected_revision", command.ExpectedRevision)
	if err != nil {
		return Checkpoint{}, err
	}

	var domain contentblock.DomainContext
	checkpoint, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[pageCheckpoint, Store]{
		Access: lockedCollaboration(pageSourceRoomFence(locale, s.pageCollaborationDocumentFence(
			command.PageID, policyv1.Page.Edit, command.ContributorMemberIDs,
		)), documentID, &domain),
		Change: func(ctx context.Context, store Store) (pageCheckpoint, error) {
			return checkpointPageVersion(ctx, store, command, documentID, expectedRevision, domain.SourceLocale)
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
		return Checkpoint{}, normalizePageContentBlockError(err)
	}
	result := Checkpoint{Revision: checkpoint.revision, Locale: locale}
	if checkpoint.created != nil {
		result.VersionID = &checkpoint.created.ID
	}
	return result, nil
}

// checkpointPageVersion stores the source document at expectedRevision as
// the next version unless it equals the latest one.
func checkpointPageVersion(
	ctx context.Context,
	store Store,
	command CreatePageVersionCheckpointCommand,
	documentID, expectedRevision uuid.UUID,
	sourceLocale string,
) (pageCheckpoint, error) {
	snapshot, err := store.LockedContentSnapshot(ctx, documentID, sourceLocale)
	if err != nil {
		return pageCheckpoint{}, err
	}
	if snapshot.Document.Revision != expectedRevision {
		return pageCheckpoint{}, &contentblock.StaleRevisionError{CurrentRevision: snapshot.Document.Revision}
	}
	checkpoint := pageCheckpoint{revision: snapshot.Document.Revision.String()}
	metadata, err := loadRequiredPageSourceLocaleMetadata(ctx, store, command.PageID)
	if err != nil {
		return pageCheckpoint{}, err
	}
	document, err := contentblock.MaterializeSnapshotPageLocale(snapshot, sourceLocale)
	if err != nil {
		return pageCheckpoint{}, err
	}
	encoded, err := EncodeVersionContentSnapshot(sourceLocale, metadata.Title, metadata.Summary, document)
	if err != nil {
		return pageCheckpoint{}, err
	}
	nextVersion, unchanged, err := nextPageVersion(ctx, store, command.PageID, encoded)
	if err != nil || unchanged {
		return checkpoint, err
	}
	checkpoint.created = &Version{
		PageID:               command.PageID,
		Version:              nextVersion,
		Title:                cloneOptionalString(metadata.Title),
		Summary:              cloneOptionalString(metadata.Summary),
		ContentSnapshot:      encoded,
		ContributorMemberIDs: NormalizeContributorMemberIDs(command.ContributorMemberIDs),
		CreatedAt:            time.Now().UTC(),
	}
	if err := store.InsertVersion(ctx, checkpoint.created); err != nil {
		return pageCheckpoint{}, err
	}
	return checkpoint, nil
}

// nextPageVersion locks the latest version of the Page and returns the
// number after it, or unchanged when the latest version already stores
// encoded.
func nextPageVersion(ctx context.Context, store Store, pageID string, encoded []byte) (int32, bool, error) {
	previous, exists, err := store.LockLatestVersion(ctx, pageID)
	if err != nil {
		return 0, false, err
	}
	if !exists {
		return 1, false, nil
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
func applyPageDocumentLayoutMetadata(ctx context.Context, store Store, pageID string, nextLayout DocumentLayout, now time.Time) (contentblock.MetadataEffect, error) {
	current, err := store.DocumentLayout(ctx, pageID)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if current == nextLayout {
		return contentblock.MetadataEffect{}, nil
	}
	if err := store.SetDocumentLayout(ctx, pageID, nextLayout, now); err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true}, nil
}
