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

type InternalTermsService struct {
	legal internalLegalDocumentService
}

type InternalTermsServiceOption func(*InternalTermsService)

func WithInternalTermsContentBlocks(
	store *contentblock.Store,
	checker CollaborationPermissionChecker,
	checkpoints ...persistencecheckpoint.ContributorFence,
) InternalTermsServiceOption {
	return func(service *InternalTermsService) {
		service.legal.contentBlocks = store
		service.legal.spiceDB = checker
		if len(checkpoints) > 0 {
			service.legal.checkpoints = checkpoints[0]
		}
	}
}

func NewInternalTermsService(db *gorm.DB, dependencies Dependencies) *InternalTermsService {
	return &InternalTermsService{legal: internalLegalDocumentService{
		db: db, kind: "terms", legalOG: dependencies.OG,
	}}
}

func NewAuditedInternalTermsService(
	db *gorm.DB,
	auditWriter domainaudit.Appender,
	dependencies Dependencies,
	options ...InternalTermsServiceOption,
) *InternalTermsService {
	if auditWriter == nil {
		panic("internal terms audit writer is required")
	}
	service := NewInternalTermsService(db, dependencies)
	service.legal.auditWriter = auditWriter
	for _, option := range options {
		if option != nil {
			option(service)
		}
	}
	return service
}

func (s *InternalTermsService) LoadTermsBlockDocument(
	ctx context.Context,
	req *connect.Request[intrav1.LoadTermsBlockDocumentRequest],
) (*connect.Response[intrav1.LoadTermsBlockDocumentResponse], error) {
	result, err := s.legal.loadDocument(ctx, req.Msg.TermsId, req.Msg.Locale, req.Msg.Principal)
	if err != nil {
		return nil, err
	}
	sourceMetadata := &intrav1.TermsLocaleMetadata{
		Locale: result.SourceMetadata.Locale, Title: result.SourceMetadata.Title,
	}
	var localeMetadata *intrav1.TermsLocaleMetadata
	if result.LocaleMetadata != nil {
		localeMetadata = &intrav1.TermsLocaleMetadata{
			Locale: result.LocaleMetadata.Locale, Title: result.LocaleMetadata.Title,
		}
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(result.Snapshot, result.Locale)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&intrav1.LoadTermsBlockDocumentResponse{
		Document: result.Document, DocumentRevision: result.Snapshot.Document.Revision.String(),
		SourceMetadata: sourceMetadata,
		Locale:         result.Locale, LocaleExists: result.LocaleExists,
		LocaleMetadata: localeMetadata, TargetRevision: result.TargetRevision,
		PresentLocaleValues: presentLocaleValues,
	}), nil
}

func (s *InternalTermsService) ApplyTermsBlockBatch(
	ctx context.Context,
	req *connect.Request[intrav1.ApplyTermsBlockBatchRequest],
) (*connect.Response[intrav1.ApplyTermsBlockBatchResponse], error) {
	result, err := s.legal.applyDocumentMutation(
		ctx, req.Msg.TermsId, req.Msg.Locale, req.Msg.ExpectedTargetRevision,
		req.Msg.Batch, req.Msg.AffectedLocaleValues,
	)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&intrav1.ApplyTermsBlockBatchResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}

func (s *InternalTermsService) UpdateTermsLocaleMetadata(
	ctx context.Context,
	req *connect.Request[intrav1.UpdateTermsLocaleMetadataRequest],
) (*connect.Response[intrav1.UpdateTermsLocaleMetadataResponse], error) {
	if req.Msg.Title == nil {
		return nil, errs.InvalidArgument("title", "must be provided")
	}
	result, _, err := s.legal.updateDocumentMetadata(ctx, legalDocumentMetadataInput{
		EntityID: req.Msg.TermsId, Locale: req.Msg.Locale,
		ExpectedRevision: req.Msg.ExpectedRevision, Title: req.Msg.Title,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Contributors:           req.Msg.ContributorMemberIds,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&intrav1.UpdateTermsLocaleMetadataResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}
