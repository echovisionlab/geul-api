package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// InternalCampaignService owns the typed Block persistence boundary for one
// Campaign document. The collaboration runtime transports operations; it does
// not own durable document bytes.
type InternalCampaignService struct {
	db            *gorm.DB
	auditWriter   domainaudit.Appender
	spiceDB       CollaborationPermissionChecker
	checkpoints   persistencecheckpoint.ContributorFence
	contentBlocks *contentblock.Store
}

type InternalCampaignServiceOption func(*InternalCampaignService)

func WithInternalCampaignContentBlockStore(store *contentblock.Store) InternalCampaignServiceOption {
	return func(service *InternalCampaignService) { service.contentBlocks = store }
}

func WithInternalCampaignSpiceDB(spiceDB CollaborationPermissionChecker) InternalCampaignServiceOption {
	return func(service *InternalCampaignService) { service.spiceDB = spiceDB }
}

func WithInternalCampaignCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalCampaignServiceOption {
	return func(service *InternalCampaignService) { service.checkpoints = checkpoints }
}

func NewInternalCampaignService(db *gorm.DB, options ...InternalCampaignServiceOption) *InternalCampaignService {
	service := &InternalCampaignService{db: db}
	for _, option := range options {
		option(service)
	}
	return service
}

func NewAuditedInternalCampaignService(db *gorm.DB, auditWriter domainaudit.Appender, options ...InternalCampaignServiceOption) *InternalCampaignService {
	if auditWriter == nil {
		panic("internal campaign audit writer is required")
	}
	service := NewInternalCampaignService(db, options...)
	service.auditWriter = auditWriter
	return service
}

func (s *InternalCampaignService) ApplyBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyCampaignBlockBatchRequest],
) (*connect.Response[intrav1.ApplyCampaignBlockBatchResponse], error) {
	if err := s.requireContentBlockRuntime(); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	campaignID := strings.TrimSpace(req.Msg.GetCampaignId())
	if campaignID == "" {
		return nil, errs.Required("campaign_id")
	}
	locale, err := normalizeCampaignDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, s.db, campaignContentEntity, campaignID)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		req.Msg.Batch,
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, errs.InvalidArgument("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		req.Msg.Batch.Profile,
		locale,
		&storage,
		req.Msg.GetAffectedLocaleValues(),
	); err != nil {
		return nil, errs.InvalidArgument("affected_locale_values", err.Error())
	}
	expectedDocumentRevision, err := uuid.Parse(strings.TrimSpace(req.Msg.Batch.GetExpectedRevision()))
	if err != nil || expectedDocumentRevision == uuid.Nil || expectedDocumentRevision.String() != strings.TrimSpace(req.Msg.Batch.GetExpectedRevision()) {
		return nil, errs.InvalidArgument("batch.expected_revision", "must be a canonical UUID")
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, req.Msg.Batch.Profile, storage)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return nil, errs.InvalidArgument("locale", "Campaign Block mutation must match the authenticated room locale")
		}
	}
	domain, err := loadCampaignEmailSourceContext(ctx, s.db, campaignContentEntity, campaignID)
	if err != nil {
		return nil, err
	}

	var result contentblock.Result
	now := time.Now().UTC()
	if locale != domain.SourceLocale {
		contributors := req.Msg.GetBatch().GetContributorMemberIds()
		if len(contributors) != 1 {
			return nil, errs.InvalidArgument(
				"contributor_member_ids", "target collaboration mutation requires exactly one origin Member",
			)
		}
		originMemberID := contributors[0]
		var targetRevision string
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := requireCampaignContributors(ctx, tx, contributors); err != nil {
				return err
			}
			if err := requireCampaignCollaborationContributors(ctx, tx, s.checkpoints, campaignID, contributors); err != nil {
				return err
			}
			output, applyErr := applyCampaignTargetMutation(
				ctx, tx, s.contentBlocks,
				campaignTargetMutationInput{
					CampaignID: campaignID, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: expectedDocumentRevision,
					ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
					AllowCreate:            false, Now: now,
					Fence: campaignEmailContentFence(campaignContentEntity, campaignID),
				},
			)
			if applyErr != nil {
				return applyErr
			}
			result, targetRevision = output.Result, output.TargetRevision
			if result.Changed {
				return appendCampaignLocaleContentAudit(
					ctx, tx, s.auditWriter, originMemberID, campaignID, locale,
					campaignLocaleContentOperation(false, false, false, true),
				)
			}
			return nil
		}); err != nil {
			return nil, asCampaignEmailConnectError(err)
		}
		return connect.NewResponse(&intrav1.ApplyCampaignBlockBatchResponse{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: false, ChangedLocales: result.ChangedLocales,
			Locale: locale, TargetRevision: &targetRevision,
		}), nil
	}
	if req.Msg.ExpectedTargetRevision != nil {
		return nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireCampaignContributors(ctx, tx, req.Msg.GetBatch().GetContributorMemberIds()); err != nil {
			return err
		}
		if err := requireCampaignCollaborationContributors(ctx, tx, s.checkpoints, campaignID, req.Msg.GetBatch().GetContributorMemberIds()); err != nil {
			return err
		}
		var applyErr error
		result, applyErr = s.contentBlocks.ApplyBatch(
			ctx,
			tx,
			batch,
			campaignEmailContentFence(
				campaignContentEntity,
				campaignID,
			),
		)
		if applyErr != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, applyErr)
		}
		if !result.Changed {
			return nil
		}
		domain, applyErr := lockCampaignEmailTranslationSource(ctx, tx, campaignContentEntity, campaignID)
		if applyErr != nil {
			return applyErr
		}
		snapshot, applyErr := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, domain.SourceLocale,
		)
		if applyErr != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, applyErr)
		}
		projectionLocales := result.ChangedLocales
		if result.TranslationSourceChanged || len(projectionLocales) == 0 {
			// The source graph/revision advances independently of existing target
			// rows. Shared-presentation-only edits also report no changed locale,
			// but still require the source materialization to follow the document.
			// Existing target rows remain unchanged in both cases.
			projectionLocales = []string{domain.SourceLocale}
		}
		return projectCampaignEmailMaterializedContent(
			ctx, tx, campaignContentEntity, campaignID, snapshot, projectionLocales, now,
		)
	}); err != nil {
		return nil, asCampaignEmailConnectError(err)
	}
	return connect.NewResponse(&intrav1.ApplyCampaignBlockBatchResponse{
		DocumentRevision: result.DocumentRevision.String(),
		Changed:          result.Changed, SourceChanged: result.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales, Locale: locale,
	}), nil
}

func (s *InternalCampaignService) UpdateCampaignLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateCampaignLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateCampaignLocaleMetadataResponse], error) {
	if err := s.requireContentBlockRuntime(); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil {
		return nil, errs.Required("request")
	}
	campaignID := strings.TrimSpace(req.Msg.GetCampaignId())
	if campaignID == "" {
		return nil, errs.Required("campaign_id")
	}
	locale, err := normalizeCampaignDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	if req.Msg.Subject == nil {
		return nil, errs.Required("subject")
	}
	expectedRevision, err := uuid.Parse(strings.TrimSpace(req.Msg.GetExpectedRevision()))
	if err != nil {
		return nil, errs.InvalidArgument("expected_revision", "must be a UUID")
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, s.db, campaignContentEntity, campaignID)
	if err != nil {
		return nil, err
	}
	domain, err := loadCampaignEmailSourceContext(ctx, s.db, campaignContentEntity, campaignID)
	if err != nil {
		return nil, err
	}

	var result contentblock.AdvanceResult
	var sourceLocale string
	now := time.Now().UTC()
	if locale != domain.SourceLocale {
		contributors := req.Msg.GetContributorMemberIds()
		if len(contributors) != 1 {
			return nil, errs.InvalidArgument(
				"contributor_member_ids", "target collaboration mutation requires exactly one origin Member",
			)
		}
		originMemberID := contributors[0]
		batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision}
		var output campaignTargetMutationResult
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := requireCampaignContributors(ctx, tx, contributors); err != nil {
				return err
			}
			if err := requireCampaignCollaborationContributors(ctx, tx, s.checkpoints, campaignID, contributors); err != nil {
				return err
			}
			var applyErr error
			output, applyErr = applyCampaignTargetMutation(
				ctx, tx, s.contentBlocks,
				campaignTargetMutationInput{
					CampaignID: campaignID, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: expectedRevision,
					ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
					AllowCreate:            false, SetSubject: true, Subject: req.Msg.Subject, Now: now,
					Fence: campaignEmailContentFence(campaignContentEntity, campaignID),
				},
			)
			if applyErr != nil {
				return applyErr
			}
			if output.Result.Changed {
				return appendCampaignLocaleContentAudit(
					ctx, tx, s.auditWriter, originMemberID, campaignID, locale,
					campaignLocaleContentOperation(false, false, false, true),
				)
			}
			return nil
		}); err != nil {
			return nil, asCampaignEmailConnectError(err)
		}
		return connect.NewResponse(&intrav1.UpdateCampaignLocaleMetadataResponse{
			DocumentRevision: output.Result.DocumentRevision.String(), Changed: output.Result.Changed,
			SourceChanged: false, ChangedLocales: output.Result.ChangedLocales,
			Locale:         locale,
			TargetRevision: &output.TargetRevision,
		}), nil
	}
	if req.Msg.ExpectedTargetRevision != nil {
		return nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := requireCampaignContributors(ctx, tx, req.Msg.GetContributorMemberIds()); err != nil {
			return err
		}
		if err := requireCampaignCollaborationContributors(ctx, tx, s.checkpoints, campaignID, req.Msg.GetContributorMemberIds()); err != nil {
			return err
		}
		var advanceErr error
		result, advanceErr = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			campaignEmailContentFence(
				campaignContentEntity,
				campaignID,
			),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				domain, err := lockCampaignEmailTranslationSource(ctx, tx, campaignContentEntity, campaignID)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				sourceLocale = domain.SourceLocale
				return updateCampaignEmailLocaleSubject(ctx, tx, campaignContentEntity, campaignID, sourceLocale, req.Msg.GetSubject(), now)
			},
		)
		if advanceErr != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, advanceErr)
		}
		domain, advanceErr := lockCampaignEmailTranslationSource(ctx, tx, campaignContentEntity, campaignID)
		if advanceErr != nil {
			return advanceErr
		}
		if !result.TranslationSourceChanged {
			return nil
		}
		snapshot, advanceErr := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, domain.SourceLocale,
		)
		if advanceErr != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, advanceErr)
		}
		return projectCampaignEmailMaterializedContent(
			ctx, tx, campaignContentEntity, campaignID, snapshot, []string{domain.SourceLocale}, now,
		)
	}); err != nil {
		return nil, asCampaignEmailConnectError(err)
	}
	return connect.NewResponse(&intrav1.UpdateCampaignLocaleMetadataResponse{
		DocumentRevision: result.DocumentRevision.String(),
		Changed:          result.Changed, SourceChanged: result.TranslationSourceChanged,
		ChangedLocales: []string{sourceLocale}, Locale: locale,
	}), nil
}

func (s *InternalCampaignService) LoadDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadCampaignDocumentRequest],
) (*connect.Response[intrav1.LoadCampaignDocumentResponse], error) {
	if err := s.requireContentBlockRuntime(); err != nil {
		return nil, err
	}
	if s.spiceDB == nil {
		return nil, errs.Internal(errors.New("campaign collaboration authorization is not configured"))
	}
	campaignID := strings.TrimSpace(req.Msg.GetCampaignId())
	if campaignID == "" {
		return nil, errs.Required("campaign_id")
	}
	locale, err := normalizeCampaignDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	if err := requireCampaignEmailDocumentLoad(
		ctx,
		s.db,
		s.spiceDB,
		req.Msg.GetPrincipal(),
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN,
		campaignID,
	); err != nil {
		return nil, err
	}
	var state campaignExactLocaleState
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, loadErr := loadCampaignEmailContentDocumentRoot(
			ctx, tx, campaignContentEntity, campaignID, true,
		)
		if loadErr != nil {
			return loadErr
		}
		state, loadErr = loadCampaignExactLocaleState(
			ctx, tx, s.contentBlocks, campaignID, documentID, locale, false,
		)
		return loadErr
	}); err != nil {
		return nil, err
	}
	document, err := materializeCampaignExactLocaleDocument(state, locale)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.Snapshot, locale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	var targetRevision *string
	if locale != state.SourceLocale && state.TargetMetadata != nil {
		targetRevision = &state.TargetRevision
	}

	return connect.NewResponse(&intrav1.LoadCampaignDocumentResponse{
		Document: document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: campaignSourceMetadataProjection(state), Locale: locale,
		LocaleExists:   state.TargetMetadata != nil,
		LocaleMetadata: campaignLocaleMetadataProjection(state, locale), TargetRevision: targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalCampaignService) requireContentBlockRuntime() error {
	if s == nil || s.db == nil || s.contentBlocks == nil {
		return errs.Internal(errors.New("campaign content Block runtime is not configured"))
	}
	return nil
}
