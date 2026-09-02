package legal

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/persistencecheckpoint"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
)

type InternalPrivacyService struct {
	legal internalLegalDocumentService
}

type InternalPrivacyServiceOption func(*InternalPrivacyService)

func WithInternalPrivacyContentBlocks(
	store *contentblock.Store,
	checker CollaborationPermissionChecker,
	checkpoints ...persistencecheckpoint.ContributorFence,
) InternalPrivacyServiceOption {
	return func(service *InternalPrivacyService) {
		service.legal.contentBlocks = store
		service.legal.spiceDB = checker
		if len(checkpoints) > 0 {
			service.legal.checkpoints = checkpoints[0]
		}
	}
}

func NewInternalPrivacyService(db *gorm.DB, dependencies Dependencies) *InternalPrivacyService {
	return &InternalPrivacyService{legal: internalLegalDocumentService{
		db: db, kind: "privacy", legalOG: dependencies.OG,
	}}
}

func NewAuditedInternalPrivacyService(
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	dependencies Dependencies,
	options ...InternalPrivacyServiceOption,
) *InternalPrivacyService {
	if auditWriter == nil {
		panic("internal privacy audit writer is required")
	}
	service := NewInternalPrivacyService(db, dependencies)
	service.legal.auditWriter = auditWriter
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalPrivacyService) LoadPrivacyBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadPrivacyBlockDocumentRequest],
) (*connect.Response[intrav1.LoadPrivacyBlockDocumentResponse], error) {
	result, err := s.legal.loadDocument(ctx, req.Msg.PrivacyId, req.Msg.Locale, req.Msg.Principal)
	if err != nil {
		return nil, err
	}
	sourceMetadata := &intrav1.PrivacyLocaleMetadata{
		Locale: result.SourceMetadata.Locale, Title: result.SourceMetadata.Title,
	}
	var localeMetadata *intrav1.PrivacyLocaleMetadata
	if result.LocaleMetadata != nil {
		localeMetadata = &intrav1.PrivacyLocaleMetadata{
			Locale: result.LocaleMetadata.Locale, Title: result.LocaleMetadata.Title,
		}
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(result.Snapshot, result.Locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.LoadPrivacyBlockDocumentResponse{
		Document: result.Document, DocumentRevision: result.Snapshot.Document.Revision.String(),
		SourceMetadata: sourceMetadata,
		Locale:         result.Locale, LocaleExists: result.LocaleExists,
		LocaleMetadata: localeMetadata, TargetRevision: result.TargetRevision,
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalPrivacyService) ApplyPrivacyBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyPrivacyBlockBatchRequest],
) (*connect.Response[intrav1.ApplyPrivacyBlockBatchResponse], error) {
	result, err := s.legal.applyDocumentMutation(
		ctx, req.Msg.PrivacyId, req.Msg.Locale, req.Msg.ExpectedTargetRevision,
		req.Msg.Batch, req.Msg.AffectedLocaleValues,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&intrav1.ApplyPrivacyBlockBatchResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}

func (s *InternalPrivacyService) UpdatePrivacyLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdatePrivacyLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdatePrivacyLocaleMetadataResponse], error) {
	if req.Msg.Title == nil {
		return nil, errs.InvalidArgument("title", "must be provided")
	}
	result, _, err := s.legal.updateDocumentMetadata(ctx, legalDocumentMetadataInput{
		EntityID: req.Msg.PrivacyId, Locale: req.Msg.Locale,
		ExpectedRevision: req.Msg.ExpectedRevision, Title: req.Msg.Title,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Contributors:           req.Msg.ContributorMemberIds,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&intrav1.UpdatePrivacyLocaleMetadataResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}
