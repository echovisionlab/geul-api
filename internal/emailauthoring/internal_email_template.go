package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

// InternalEmailTemplateService owns the typed Block persistence boundary for
// one Email Template document. Email Layout remains a separate HTML authoring
// domain and sealed delivery snapshots remain immutable render facts.
type InternalEmailTemplateService struct {
	db            *gorm.DB
	auditWriter   domainaudit.Appender
	spiceDB       *auth.SpiceDBClient
	checkpoints   persistencecheckpoint.ContributorFence
	contentBlocks *contentblock.Store
	references    CampaignDeliveryReferences
}

type InternalEmailTemplateServiceOption func(*InternalEmailTemplateService)

func WithInternalEmailTemplateContentBlockStore(store *contentblock.Store) InternalEmailTemplateServiceOption {
	return func(service *InternalEmailTemplateService) { service.contentBlocks = store }
}

func WithInternalEmailTemplateCheckpoints(checkpoints persistencecheckpoint.ContributorFence) InternalEmailTemplateServiceOption {
	return func(service *InternalEmailTemplateService) { service.checkpoints = checkpoints }
}

func WithInternalEmailTemplateCampaignDeliveryReferences(references CampaignDeliveryReferences) InternalEmailTemplateServiceOption {
	return func(service *InternalEmailTemplateService) { service.references = references }
}

func NewInternalEmailTemplateService(db *gorm.DB, spiceDB *auth.SpiceDBClient, options ...InternalEmailTemplateServiceOption) *InternalEmailTemplateService {
	service := &InternalEmailTemplateService{db: db, spiceDB: spiceDB}
	for _, option := range options {
		option(service)
	}
	return service
}

func NewAuditedInternalEmailTemplateService(db *gorm.DB, auditWriter domainaudit.Appender, spiceDB *auth.SpiceDBClient, options ...InternalEmailTemplateServiceOption) *InternalEmailTemplateService {
	if auditWriter == nil {
		panic("internal email template audit writer is required")
	}
	service := NewInternalEmailTemplateService(db, spiceDB, options...)
	service.auditWriter = auditWriter
	return service
}

func (s *InternalEmailTemplateService) ApplyBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyEmailTemplateBlockBatchRequest],
) (*connect.Response[intrav1.ApplyEmailTemplateBlockBatchResponse], error) {
	if err := s.requireContentBlockRuntime(); err != nil {
		return nil, err
	}
	if req == nil || req.Msg == nil || req.Msg.Batch == nil {
		return nil, errs.Required("batch")
	}
	templateID := strings.TrimSpace(req.Msg.GetEmailTemplateId())
	if templateID == "" {
		return nil, errs.Required("email_template_id")
	}
	locale, err := normalizeEmailTemplateDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, s.db, emailTemplateContentEntity, templateID)
	if err != nil {
		return nil, err
	}
	storage, err := contentv1.FlattenRichTextMutationBatchStorage(
		req.Msg.GetBatch(),
		contentv1.ContentValidationMode_CONTENT_VALIDATION_MODE_WRITE,
	)
	if err != nil {
		return nil, errs.InvalidArgument("batch", err.Error())
	}
	if err := contentblock.RestoreRichTextAffectedLocaleValues(
		req.Msg.GetBatch().GetProfile(),
		locale,
		&storage,
		req.Msg.GetAffectedLocaleValues(),
	); err != nil {
		return nil, errs.InvalidArgument("affected_locale_values", err.Error())
	}
	batch, err := contentblock.BatchFromRichTextStorage(documentID, req.Msg.GetBatch().GetProfile(), storage)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	for _, group := range batch.LocaleGroups {
		if group.Locale != locale {
			return nil, errs.InvalidArgument("locale", "Email Template Block mutation must match the authenticated room locale")
		}
	}
	domain, err := loadCampaignEmailSourceContext(ctx, s.db, emailTemplateContentEntity, templateID)
	if err != nil {
		return nil, err
	}

	var result contentblock.Result
	now := time.Now().UTC()
	if locale != domain.SourceLocale {
		contributors := req.Msg.GetBatch().GetContributorMemberIds()
		var targetRevision string
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			originMemberID, err := requireEmailAuthoringMutationContributor(ctx, tx, contributors)
			if err != nil {
				return err
			}
			if err := requireEmailTemplateCollaborationContributors(ctx, tx, s.checkpoints, templateID, contributors); err != nil {
				return err
			}
			output, applyErr := applyEmailTemplateTargetMutation(
				ctx, tx, s.contentBlocks,
				emailTemplateTargetMutationInput{
					TemplateID: templateID, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: batch.ExpectedRevision,
					ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
					AllowCreate:            false, Now: now,
					Fence: campaignEmailContentFence(s.references, emailTemplateContentEntity, templateID),
				},
			)
			if applyErr != nil {
				return applyErr
			}
			result, targetRevision = output.Result, output.TargetRevision
			if result.Changed {
				return appendEmailTemplateLocaleContentAudit(
					ctx, tx, s.auditWriter, originMemberID, templateID, locale,
					emailAuthoringLocaleContentOperation(false, false, false, true),
				)
			}
			return nil
		}); err != nil {
			return nil, asCampaignEmailConnectError(err)
		}
		return connect.NewResponse(&intrav1.ApplyEmailTemplateBlockBatchResponse{
			DocumentRevision: result.DocumentRevision.String(), Changed: result.Changed,
			SourceChanged: false, ChangedLocales: result.ChangedLocales,
			Locale: locale, TargetRevision: &targetRevision,
		}), nil
	}
	if req.Msg.ExpectedTargetRevision != nil {
		return nil, errs.InvalidArgument("expected_target_revision", "must be omitted for the source locale")
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		originMemberID, err := requireEmailAuthoringMutationContributor(
			ctx, tx, req.Msg.GetBatch().GetContributorMemberIds(),
		)
		if err != nil {
			return err
		}
		if err := requireEmailTemplateCollaborationContributors(ctx, tx, s.checkpoints, templateID, req.Msg.GetBatch().GetContributorMemberIds()); err != nil {
			return err
		}
		var applyErr error
		result, applyErr = s.contentBlocks.ApplyBatch(
			ctx,
			tx,
			batch,
			campaignEmailContentFence(
				s.references,
				emailTemplateContentEntity,
				templateID,
			),
		)
		if applyErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, applyErr)
		}
		if !result.Changed {
			return nil
		}
		domain, applyErr := lockCampaignEmailTranslationSource(ctx, tx, emailTemplateContentEntity, templateID)
		if applyErr != nil {
			return applyErr
		}
		snapshot, applyErr := s.contentBlocks.LoadSnapshotInTransaction(
			ctx, tx, documentID, domain.SourceLocale,
		)
		if applyErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, applyErr)
		}
		projectionLocales := result.ChangedLocales
		if result.TranslationSourceChanged {
			// A source graph/value change also changes the source fallback of
			// every existing target projection. Re-materialize those rows from
			// the current snapshot so delivery never serves a stale fallback.
			projectionLocales = nil
		}
		if applyErr := projectEmailTemplateMaterializedContent(
			ctx, tx, templateID, snapshot, projectionLocales, now,
		); applyErr != nil {
			return applyErr
		}
		if result.TranslationSourceChanged {
			document, applyErr := contentblock.SnapshotToLocalizedRichTextDocument(
				snapshot, domain.SourceLocale,
			)
			if applyErr != nil {
				return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, applyErr)
			}
			projection, applyErr := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
			if applyErr != nil {
				return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, applyErr)
			}
			if applyErr := syncCustomEmailTemplateVariables(ctx, tx, templateID, projection.HTML); applyErr != nil {
				return applyErr
			}
		}
		return appendEmailTemplateLocaleContentAudit(
			ctx, tx, s.auditWriter, originMemberID, templateID, locale,
			emailAuthoringLocaleContentOperation(true, false, false, true),
		)
	}); err != nil {
		return nil, asCampaignEmailConnectError(err)
	}
	return connect.NewResponse(&intrav1.ApplyEmailTemplateBlockBatchResponse{
		DocumentRevision: result.DocumentRevision.String(),
		Changed:          result.Changed, SourceChanged: result.TranslationSourceChanged,
		ChangedLocales: result.ChangedLocales, Locale: locale,
	}), nil
}

func (s *InternalEmailTemplateService) UpdateEmailTemplateLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateEmailTemplateLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateEmailTemplateLocaleMetadataResponse], error) {
	if err := s.requireContentBlockRuntime(); err != nil {
		return nil, err
	}
	templateID := strings.TrimSpace(req.Msg.GetEmailTemplateId())
	if templateID == "" {
		return nil, errs.Required("email_template_id")
	}
	locale, err := normalizeEmailTemplateDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	if req.Msg.Subject == nil {
		return nil, errs.Required("subject")
	}
	expectedRevisionValue := strings.TrimSpace(req.Msg.GetExpectedRevision())
	expectedRevision, err := uuid.Parse(expectedRevisionValue)
	if err != nil || expectedRevision == uuid.Nil || expectedRevision.String() != expectedRevisionValue {
		return nil, errs.InvalidArgument("expected_revision", "must be a canonical UUID")
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, s.db, emailTemplateContentEntity, templateID)
	if err != nil {
		return nil, err
	}
	domain, err := loadCampaignEmailSourceContext(ctx, s.db, emailTemplateContentEntity, templateID)
	if err != nil {
		return nil, err
	}

	var result contentblock.AdvanceResult
	now := time.Now().UTC()
	if locale != domain.SourceLocale {
		contributors := req.Msg.GetContributorMemberIds()
		batch := contentblock.Batch{DocumentID: documentID, ExpectedRevision: expectedRevision}
		var output emailTemplateTargetMutationResult
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			originMemberID, err := requireEmailAuthoringMutationContributor(ctx, tx, contributors)
			if err != nil {
				return err
			}
			if err := requireEmailTemplateCollaborationContributors(ctx, tx, s.checkpoints, templateID, contributors); err != nil {
				return err
			}
			var applyErr error
			output, applyErr = applyEmailTemplateTargetMutation(
				ctx, tx, s.contentBlocks,
				emailTemplateTargetMutationInput{
					TemplateID: templateID, DocumentID: documentID, Locale: locale,
					Batch: batch, ExpectedDocumentRevision: expectedRevision,
					ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
					AllowCreate:            false, SetSubject: true, Subject: req.Msg.Subject, Now: now,
					Fence: campaignEmailContentFence(s.references, emailTemplateContentEntity, templateID),
				},
			)
			if applyErr != nil {
				return applyErr
			}
			if output.Result.Changed {
				return appendEmailTemplateLocaleContentAudit(
					ctx, tx, s.auditWriter, originMemberID, templateID, locale,
					emailAuthoringLocaleContentOperation(false, false, false, true),
				)
			}
			return nil
		}); err != nil {
			return nil, asCampaignEmailConnectError(err)
		}
		return connect.NewResponse(&intrav1.UpdateEmailTemplateLocaleMetadataResponse{
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
		originMemberID, err := requireEmailAuthoringMutationContributor(
			ctx, tx, req.Msg.GetContributorMemberIds(),
		)
		if err != nil {
			return err
		}
		if err := requireEmailTemplateCollaborationContributors(ctx, tx, s.checkpoints, templateID, req.Msg.GetContributorMemberIds()); err != nil {
			return err
		}
		var advanceErr error
		result, advanceErr = s.contentBlocks.AdvanceRevision(
			ctx,
			tx,
			contentblock.AdvanceInput{DocumentID: documentID, ExpectedRevision: expectedRevision},
			campaignEmailContentFence(
				s.references,
				emailTemplateContentEntity,
				templateID,
			),
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				domain, err := lockCampaignEmailTranslationSource(ctx, tx, emailTemplateContentEntity, templateID)
				if err != nil {
					return contentblock.MetadataEffect{}, err
				}
				return updateCampaignEmailLocaleSubject(ctx, tx, emailTemplateContentEntity, templateID, domain.SourceLocale, req.Msg.GetSubject(), now)
			},
		)
		if advanceErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, advanceErr)
		}
		if result.Changed {
			return appendEmailTemplateLocaleContentAudit(
				ctx, tx, s.auditWriter, originMemberID, templateID, locale,
				emailAuthoringLocaleContentOperation(true, false, false, true),
			)
		}
		return nil
	}); err != nil {
		return nil, asCampaignEmailConnectError(err)
	}

	changedLocales := []string(nil)
	if result.Changed {
		changedLocales = []string{locale}
	}
	return connect.NewResponse(&intrav1.UpdateEmailTemplateLocaleMetadataResponse{
		DocumentRevision: result.DocumentRevision.String(),
		Changed:          result.Changed, SourceChanged: result.TranslationSourceChanged,
		ChangedLocales: changedLocales, Locale: locale,
	}), nil
}

func (s *InternalEmailTemplateService) LoadDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadEmailTemplateDocumentRequest],
) (*connect.Response[intrav1.LoadEmailTemplateDocumentResponse], error) {
	if err := s.requireContentBlockRuntime(); err != nil {
		return nil, err
	}
	if s.spiceDB == nil {
		return nil, errs.Internal(errors.New("email template collaboration authorization is not configured"))
	}
	templateID := strings.TrimSpace(req.Msg.GetEmailTemplateId())
	if templateID == "" {
		return nil, errs.Required("email_template_id")
	}
	locale, err := normalizeEmailTemplateDocumentLocale(req.Msg.GetLocale())
	if err != nil {
		return nil, err
	}
	if err := requireCampaignEmailDocumentLoad(
		ctx,
		s.db,
		s.spiceDB,
		s.references,
		req.Msg.GetPrincipal(),
		intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_EMAIL_TEMPLATE,
		templateID,
	); err != nil {
		return nil, err
	}
	var state emailTemplateExactLocaleState
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		documentID, loadErr := loadCampaignEmailContentDocumentRoot(
			ctx, tx, emailTemplateContentEntity, templateID, true,
		)
		if loadErr != nil {
			return loadErr
		}
		state, loadErr = loadEmailTemplateExactLocaleState(
			ctx, tx, s.contentBlocks, templateID, documentID, locale, false,
		)
		return loadErr
	}); err != nil {
		return nil, err
	}
	document, err := materializeEmailTemplateExactLocaleDocument(state, locale)
	if err != nil {
		return nil, err
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(state.Snapshot, locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	var targetRevision *string
	if locale != state.SourceLocale && state.TargetMetadata != nil {
		targetRevision = &state.TargetRevision
	}

	return connect.NewResponse(&intrav1.LoadEmailTemplateDocumentResponse{
		Document: document, DocumentRevision: state.Snapshot.Document.Revision.String(),
		SourceMetadata: emailTemplateSourceMetadataProjection(state), Locale: locale,
		LocaleExists:   state.TargetMetadata != nil,
		LocaleMetadata: emailTemplateLocaleMetadataProjection(state, locale), TargetRevision: targetRevision,
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalEmailTemplateService) requireContentBlockRuntime() error {
	if s == nil || s.db == nil || s.contentBlocks == nil {
		return errs.Internal(errors.New("email template content Block runtime is not configured"))
	}
	return nil
}
