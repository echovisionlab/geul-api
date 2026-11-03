package legal

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func optionalTimestamp(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

// paginationMessage renders where a page of versions lies among them.
func paginationMessage(page PolicyPage) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{Total: page.Total, Limit: page.Limit, Offset: page.Offset, HasMore: page.HasMore}
}

func optionalTime(value *timestamppb.Timestamp) *time.Time {
	if value == nil {
		return nil
	}
	parsed := value.AsTime()
	return &parsed
}

// TermsConnectHandler serves the Terms management RPCs through the shared
// legal policy use cases.
type TermsConnectHandler struct {
	policies *Service
}

// NewTermsConnectHandler returns the Terms Connect adapter of policies.
func NewTermsConnectHandler(policies *Service) *TermsConnectHandler {
	return &TermsConnectHandler{policies: policies}
}

// TermsMessage renders a Terms version.
func TermsMessage(policy Policy) *managev1.Terms {
	return &managev1.Terms{
		Id: policy.ID, Version: policy.Version, Title: policy.Title,
		Document: policy.Document, Revision: policy.Revision,
		SourceLocale: policy.SourceLocale, SnapshotDigest: policy.SnapshotDigest,
		Status:    TermsStatusToProto(policy.Status),
		CreatedAt: timestamppb.New(policy.CreatedAt), UpdatedAt: timestamppb.New(policy.UpdatedAt),
		EffectiveFrom: optionalTimestamp(policy.EffectiveFrom), EffectiveUntil: optionalTimestamp(policy.EffectiveUntil),
	}
}

// TermsLifecycleMessage renders a Terms lifecycle result.
func TermsLifecycleMessage(lifecycle PolicyLifecycle) *managev1.TermsLifecycleMutationResponse {
	return &managev1.TermsLifecycleMutationResponse{
		Id: lifecycle.ID, Changed: lifecycle.Changed, DocumentRevision: lifecycle.DocumentRevision,
		Status: TermsStatusToProto(lifecycle.Status), UpdatedAt: timestamppb.New(lifecycle.UpdatedAt),
		EffectiveFrom: optionalTimestamp(lifecycle.EffectiveFrom), EffectiveUntil: optionalTimestamp(lifecycle.EffectiveUntil),
	}
}

func termsResponse(policy Policy, err error) (*connect.Response[managev1.Terms], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(TermsMessage(policy)), nil
}

func termsLifecycleResponse(lifecycle PolicyLifecycle, err error) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(TermsLifecycleMessage(lifecycle)), nil
}

func (h *TermsConnectHandler) GetTermsVersion(ctx context.Context, req *connect.Request[managev1.GetTermsVersionRequest]) (*connect.Response[managev1.Terms], error) {
	return termsResponse(h.policies.GetPolicyVersion(ctx, Terms, GetPolicyVersionQuery{ID: req.Msg.Id}))
}

func (h *TermsConnectHandler) ListTermsVersions(ctx context.Context, req *connect.Request[managev1.ListTermsVersionsRequest]) (*connect.Response[managev1.ListTermsVersionsResponse], error) {
	query := ListPolicyVersionsQuery{Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset()}
	if req.Msg.Status != nil && *req.Msg.Status != managev1.TermsStatus_TERMS_STATUS_UNSPECIFIED {
		// A status the API does not define matches no version.
		status, _ := TermsStatusFromProto(*req.Msg.Status)
		query.Status = &status
	}
	page, err := h.policies.ListPolicyVersions(ctx, Terms, query)
	versions := make([]*managev1.Terms, len(page.Versions))
	for i, version := range page.Versions {
		versions[i] = TermsMessage(version)
	}
	return respond(&managev1.ListTermsVersionsResponse{Versions: versions, Pagination: paginationMessage(page)}, err)
}

func (h *TermsConnectHandler) CreateTermsVersion(ctx context.Context, req *connect.Request[managev1.CreateTermsVersionRequest]) (*connect.Response[managev1.Terms], error) {
	return termsResponse(h.policies.CreatePolicyVersion(ctx, Terms, CreatePolicyVersionCommand{
		Title: req.Msg.Title, Document: req.Msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *TermsConnectHandler) ScheduleTerms(ctx context.Context, req *connect.Request[managev1.ScheduleTermsRequest]) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	return termsLifecycleResponse(h.policies.SchedulePolicy(ctx, Terms, SchedulePolicyCommand{
		ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision, EffectiveFrom: optionalTime(req.Msg.EffectiveFrom),
	}))
}

func (h *TermsConnectHandler) CancelTermsSchedule(ctx context.Context, req *connect.Request[managev1.CancelTermsScheduleRequest]) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	return termsLifecycleResponse(h.policies.CancelPolicySchedule(ctx, Terms, CancelPolicyScheduleCommand{ID: req.Msg.Id}))
}

func (h *TermsConnectHandler) ActivateTermsNow(ctx context.Context, req *connect.Request[managev1.ActivateTermsNowRequest]) (*connect.Response[managev1.TermsLifecycleMutationResponse], error) {
	return termsLifecycleResponse(h.policies.ActivatePolicyNow(ctx, Terms, ActivatePolicyNowCommand{ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision}))
}

func (h *TermsConnectHandler) DeleteTerms(ctx context.Context, req *connect.Request[managev1.DeleteTermsRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.policies.DeletePolicy(ctx, Terms, DeletePolicyCommand{ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *TermsConnectHandler) RegenerateTermsDerivedContent(ctx context.Context, req *connect.Request[managev1.RegenerateTermsDerivedContentRequest]) (*connect.Response[managev1.RegenerateTermsDerivedContentResponse], error) {
	digest, err := h.policies.RegeneratePolicyDerivedContent(ctx, Terms, RegeneratePolicyDerivedContentCommand{
		ID: req.Msg.Id, ExpectedSnapshotDigest: req.Msg.ExpectedSnapshotDigest,
	})
	return respond(&managev1.RegenerateTermsDerivedContentResponse{Regenerated: true, SnapshotDigest: digest}, err)
}

// PrivacyConnectHandler serves the Privacy management RPCs through the
// shared legal policy use cases.
type PrivacyConnectHandler struct {
	policies *Service
}

// NewPrivacyConnectHandler returns the Privacy Connect adapter of policies.
func NewPrivacyConnectHandler(policies *Service) *PrivacyConnectHandler {
	return &PrivacyConnectHandler{policies: policies}
}

// PrivacyMessage renders a Privacy version.
func PrivacyMessage(policy Policy) *managev1.Privacy {
	return &managev1.Privacy{
		Id: policy.ID, Version: policy.Version, Title: policy.Title,
		Document: policy.Document, Revision: policy.Revision,
		SourceLocale: policy.SourceLocale, SnapshotDigest: policy.SnapshotDigest,
		Status:    PrivacyStatusToProto(policy.Status),
		CreatedAt: timestamppb.New(policy.CreatedAt), UpdatedAt: timestamppb.New(policy.UpdatedAt),
		EffectiveFrom: optionalTimestamp(policy.EffectiveFrom), EffectiveUntil: optionalTimestamp(policy.EffectiveUntil),
	}
}

// PrivacyLifecycleMessage renders a Privacy lifecycle result.
func PrivacyLifecycleMessage(lifecycle PolicyLifecycle) *managev1.PrivacyLifecycleMutationResponse {
	return &managev1.PrivacyLifecycleMutationResponse{
		Id: lifecycle.ID, Changed: lifecycle.Changed, DocumentRevision: lifecycle.DocumentRevision,
		Status: PrivacyStatusToProto(lifecycle.Status), UpdatedAt: timestamppb.New(lifecycle.UpdatedAt),
		EffectiveFrom: optionalTimestamp(lifecycle.EffectiveFrom), EffectiveUntil: optionalTimestamp(lifecycle.EffectiveUntil),
	}
}

func privacyResponse(policy Policy, err error) (*connect.Response[managev1.Privacy], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(PrivacyMessage(policy)), nil
}

func privacyLifecycleResponse(lifecycle PolicyLifecycle, err error) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(PrivacyLifecycleMessage(lifecycle)), nil
}

func (h *PrivacyConnectHandler) GetPrivacyVersion(ctx context.Context, req *connect.Request[managev1.GetPrivacyVersionRequest]) (*connect.Response[managev1.Privacy], error) {
	return privacyResponse(h.policies.GetPolicyVersion(ctx, Privacy, GetPolicyVersionQuery{ID: req.Msg.Id}))
}

func (h *PrivacyConnectHandler) ListPrivacyVersions(ctx context.Context, req *connect.Request[managev1.ListPrivacyVersionsRequest]) (*connect.Response[managev1.ListPrivacyVersionsResponse], error) {
	query := ListPolicyVersionsQuery{Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset()}
	if req.Msg.Status != nil && *req.Msg.Status != managev1.PrivacyStatus_PRIVACY_STATUS_UNSPECIFIED {
		// A status the API does not define matches no version.
		status, _ := PrivacyStatusFromProto(*req.Msg.Status)
		query.Status = &status
	}
	page, err := h.policies.ListPolicyVersions(ctx, Privacy, query)
	versions := make([]*managev1.Privacy, len(page.Versions))
	for i, version := range page.Versions {
		versions[i] = PrivacyMessage(version)
	}
	return respond(&managev1.ListPrivacyVersionsResponse{Versions: versions, Pagination: paginationMessage(page)}, err)
}

func (h *PrivacyConnectHandler) CreatePrivacyVersion(ctx context.Context, req *connect.Request[managev1.CreatePrivacyVersionRequest]) (*connect.Response[managev1.Privacy], error) {
	return privacyResponse(h.policies.CreatePolicyVersion(ctx, Privacy, CreatePolicyVersionCommand{
		Title: req.Msg.Title, Document: req.Msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *PrivacyConnectHandler) SchedulePrivacy(ctx context.Context, req *connect.Request[managev1.SchedulePrivacyRequest]) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	return privacyLifecycleResponse(h.policies.SchedulePolicy(ctx, Privacy, SchedulePolicyCommand{
		ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision, EffectiveFrom: optionalTime(req.Msg.EffectiveFrom),
	}))
}

func (h *PrivacyConnectHandler) CancelPrivacySchedule(ctx context.Context, req *connect.Request[managev1.CancelPrivacyScheduleRequest]) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	return privacyLifecycleResponse(h.policies.CancelPolicySchedule(ctx, Privacy, CancelPolicyScheduleCommand{ID: req.Msg.Id}))
}

func (h *PrivacyConnectHandler) ActivatePrivacyNow(ctx context.Context, req *connect.Request[managev1.ActivatePrivacyNowRequest]) (*connect.Response[managev1.PrivacyLifecycleMutationResponse], error) {
	return privacyLifecycleResponse(h.policies.ActivatePolicyNow(ctx, Privacy, ActivatePolicyNowCommand{ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision}))
}

func (h *PrivacyConnectHandler) DeletePrivacy(ctx context.Context, req *connect.Request[managev1.DeletePrivacyRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	err := h.policies.DeletePolicy(ctx, Privacy, DeletePolicyCommand{ID: req.Msg.Id, ExpectedRevision: req.Msg.ExpectedRevision})
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func (h *PrivacyConnectHandler) RegeneratePrivacyDerivedContent(ctx context.Context, req *connect.Request[managev1.RegeneratePrivacyDerivedContentRequest]) (*connect.Response[managev1.RegeneratePrivacyDerivedContentResponse], error) {
	digest, err := h.policies.RegeneratePolicyDerivedContent(ctx, Privacy, RegeneratePolicyDerivedContentCommand{
		ID: req.Msg.Id, ExpectedSnapshotDigest: req.Msg.ExpectedSnapshotDigest,
	})
	return respond(&managev1.RegeneratePrivacyDerivedContentResponse{Regenerated: true, SnapshotDigest: digest}, err)
}

// InternalTermsConnectHandler serves the Terms collaboration RPCs through the
// shared legal document use cases.
type InternalTermsConnectHandler struct {
	documents *InternalService
}

// NewInternalTermsConnectHandler returns the Terms collaboration adapter of
// documents.
func NewInternalTermsConnectHandler(documents *InternalService) *InternalTermsConnectHandler {
	return &InternalTermsConnectHandler{documents: documents}
}

func termsLocaleMetadata(metadata *LocaleMetadata) *intrav1.TermsLocaleMetadata {
	if metadata == nil {
		return nil
	}
	return &intrav1.TermsLocaleMetadata{Locale: metadata.Locale, Title: metadata.Title}
}

func (h *InternalTermsConnectHandler) LoadTermsBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadTermsBlockDocumentRequest]) (*connect.Response[intrav1.LoadTermsBlockDocumentResponse], error) {
	result, err := h.documents.LoadBlockDocument(ctx, Terms, LoadBlockDocumentQuery{ID: req.Msg.TermsId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId()})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(result.Snapshot, result.Locale)
	return respond(&intrav1.LoadTermsBlockDocumentResponse{
		Document: result.Document, DocumentRevision: result.Snapshot.Document.Revision.String(),
		SourceMetadata: termsLocaleMetadata(&result.SourceMetadata),
		Locale:         result.Locale, LocaleExists: result.LocaleExists,
		LocaleMetadata: termsLocaleMetadata(result.LocaleMetadata), TargetRevision: result.TargetRevision,
		PresentLocaleValues: presentLocaleValues,
	}, err)
}

func (h *InternalTermsConnectHandler) ApplyTermsBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyTermsBlockBatchRequest]) (*connect.Response[intrav1.ApplyTermsBlockBatchResponse], error) {
	result, err := h.documents.ApplyBlockBatch(ctx, Terms, ApplyBlockBatchCommand{
		ID: req.Msg.TermsId, Locale: req.Msg.Locale, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Batch: req.Msg.Batch, AffectedLocaleValues: req.Msg.AffectedLocaleValues,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.ApplyTermsBlockBatchResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}

func (h *InternalTermsConnectHandler) UpdateTermsLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateTermsLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateTermsLocaleMetadataResponse], error) {
	if req.Msg.Title == nil {
		return nil, errs.InvalidArgument("title", "must be provided")
	}
	result, err := h.documents.UpdateLocaleMetadata(ctx, Terms, UpdateLocaleMetadataCommand{
		EntityID: req.Msg.TermsId, Locale: req.Msg.Locale,
		ExpectedRevision: req.Msg.ExpectedRevision, Title: req.Msg.Title,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Contributors:           req.Msg.ContributorMemberIds,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.UpdateTermsLocaleMetadataResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}

// InternalPrivacyConnectHandler serves the Privacy collaboration RPCs through
// the shared legal document use cases.
type InternalPrivacyConnectHandler struct {
	documents *InternalService
}

// NewInternalPrivacyConnectHandler returns the Privacy collaboration adapter
// of documents.
func NewInternalPrivacyConnectHandler(documents *InternalService) *InternalPrivacyConnectHandler {
	return &InternalPrivacyConnectHandler{documents: documents}
}

func privacyLocaleMetadata(metadata *LocaleMetadata) *intrav1.PrivacyLocaleMetadata {
	if metadata == nil {
		return nil
	}
	return &intrav1.PrivacyLocaleMetadata{Locale: metadata.Locale, Title: metadata.Title}
}

func (h *InternalPrivacyConnectHandler) LoadPrivacyBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadPrivacyBlockDocumentRequest]) (*connect.Response[intrav1.LoadPrivacyBlockDocumentResponse], error) {
	result, err := h.documents.LoadBlockDocument(ctx, Privacy, LoadBlockDocumentQuery{ID: req.Msg.PrivacyId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId()})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	presentLocaleValues, err := contentblock.PresentRichTextLocaleValues(result.Snapshot, result.Locale)
	return respond(&intrav1.LoadPrivacyBlockDocumentResponse{
		Document: result.Document, DocumentRevision: result.Snapshot.Document.Revision.String(),
		SourceMetadata: privacyLocaleMetadata(&result.SourceMetadata),
		Locale:         result.Locale, LocaleExists: result.LocaleExists,
		LocaleMetadata: privacyLocaleMetadata(result.LocaleMetadata), TargetRevision: result.TargetRevision,
		PresentLocaleValues: presentLocaleValues,
	}, err)
}

func (h *InternalPrivacyConnectHandler) ApplyPrivacyBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyPrivacyBlockBatchRequest]) (*connect.Response[intrav1.ApplyPrivacyBlockBatchResponse], error) {
	result, err := h.documents.ApplyBlockBatch(ctx, Privacy, ApplyBlockBatchCommand{
		ID: req.Msg.PrivacyId, Locale: req.Msg.Locale, ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Batch: req.Msg.Batch, AffectedLocaleValues: req.Msg.AffectedLocaleValues,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.ApplyPrivacyBlockBatchResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}

func (h *InternalPrivacyConnectHandler) UpdatePrivacyLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdatePrivacyLocaleMetadataRequest]) (*connect.Response[intrav1.UpdatePrivacyLocaleMetadataResponse], error) {
	if req.Msg.Title == nil {
		return nil, errs.InvalidArgument("title", "must be provided")
	}
	result, err := h.documents.UpdateLocaleMetadata(ctx, Privacy, UpdateLocaleMetadataCommand{
		EntityID: req.Msg.PrivacyId, Locale: req.Msg.Locale,
		ExpectedRevision: req.Msg.ExpectedRevision, Title: req.Msg.Title,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision,
		Contributors:           req.Msg.ContributorMemberIds,
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&intrav1.UpdatePrivacyLocaleMetadataResponse{
		DocumentRevision: result.DocumentRevision, Changed: result.Changed,
		SourceChanged: result.SourceChanged, ChangedLocales: result.ChangedLocales,
		Locale: result.Locale, TargetRevision: result.TargetRevision,
	}), nil
}
