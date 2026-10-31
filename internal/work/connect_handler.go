package work

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/structured"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	"google.golang.org/protobuf/types/known/structpb"
)

// ConnectHandler serves the Work management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedWorkServiceHandler
	works *Service
}

// NewConnectHandler returns the Connect adapter of works.
func NewConnectHandler(works *Service) *ConnectHandler {
	return &ConnectHandler{works: works}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func deleted(err error) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, err)
}

func page(request interface {
	GetLimit() int32
	GetOffset() int32
}) Page {
	return Page{Limit: request.GetLimit(), Offset: request.GetOffset()}
}

func workTypeName(workType managev1.WorkType) string {
	if workType == managev1.WorkType_WORK_TYPE_UNSPECIFIED {
		return ""
	}
	return workType.String()
}

// fieldsOf is the stored form of a present Struct; an absent one is nil.
func fieldsOf(value *structpb.Struct) structured.Fields {
	if value == nil {
		return nil
	}
	return value.AsMap()
}

func clientIDsOf(value *managev1.WorkClientsUpdate) *[]string {
	if value == nil {
		return nil
	}
	ids := value.GetClientIds()
	return &ids
}

func (h *ConnectHandler) ListWorksAdmin(ctx context.Context, req *connect.Request[managev1.ListWorksAdminRequest]) (*connect.Response[managev1.ListWorksAdminResponse], error) {
	return respond(h.works.ListWorksAdmin(ctx, ListWorksAdminQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts}))
}

func (h *ConnectHandler) CreateWork(ctx context.Context, req *connect.Request[managev1.CreateWorkRequest]) (*connect.Response[managev1.Work], error) {
	msg := req.Msg
	return respond(h.works.CreateWork(ctx, CreateWorkCommand{
		Title: msg.Title, Slug: msg.Slug, Type: workTypeName(msg.Type), Summary: msg.Summary,
		Metadata: fieldsOf(msg.Metadata), Featured: msg.Featured, Year: msg.Year, Month: msg.Month,
		UntilYear: msg.UntilYear, UntilMonth: msg.UntilMonth, IsPresent: msg.IsPresent,
		SourceLocale: msg.SourceLocale, Document: msg.Document, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) DeleteWork(ctx context.Context, req *connect.Request[managev1.DeleteWorkRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.works.DeleteWork(ctx, DeleteWorkCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) PublishWork(ctx context.Context, req *connect.Request[managev1.PublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error) {
	return respond(h.works.PublishWork(ctx, PublishWorkCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) UnpublishWork(ctx context.Context, req *connect.Request[managev1.UnpublishWorkRequest]) (*connect.Response[managev1.WorkLifecycleMutationResponse], error) {
	return respond(h.works.UnpublishWork(ctx, UnpublishWorkCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) GetWork(ctx context.Context, req *connect.Request[managev1.GetWorkRequest]) (*connect.Response[managev1.Work], error) {
	return respond(h.works.GetWork(ctx, GetWorkQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) UpdateWork(ctx context.Context, req *connect.Request[managev1.UpdateWorkRequest]) (*connect.Response[managev1.UpdateWorkResponse], error) {
	msg := req.Msg
	command := UpdateWorkCommand{
		ID: msg.Id, Slug: msg.Slug, Metadata: fieldsOf(msg.Metadata), ObservedMetadata: fieldsOf(msg.ObservedMetadata),
		Featured: msg.Featured, Clients: clientIDsOf(msg.Clients), ObservedClients: clientIDsOf(msg.ObservedClients),
		Year: msg.Year, Month: msg.Month, UntilYear: msg.UntilYear, UntilMonth: msg.UntilMonth,
		IsPresent: msg.IsPresent, MapPlaceID: msg.MapPlaceId,
	}
	if msg.Type != nil {
		workType := msg.Type.String()
		command.Type = &workType
	}
	return respond(h.works.UpdateWork(ctx, command))
}

func (h *ConnectHandler) SetWorkFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetWorkFeaturedImageRequest]) (*connect.Response[managev1.SetWorkFeaturedImageResponse], error) {
	return respond(h.works.SetWorkFeaturedImage(ctx, SetWorkFeaturedImageCommand{WorkID: req.Msg.WorkId, FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeleteWorkFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeleteWorkFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	return respond(h.works.DeleteWorkFeaturedImage(ctx, DeleteWorkFeaturedImageCommand{WorkID: req.Msg.WorkId}))
}

func (h *ConnectHandler) GetWorkCredits(ctx context.Context, req *connect.Request[managev1.GetWorkCreditsRequest]) (*connect.Response[managev1.GetWorkCreditsResponse], error) {
	return respond(h.works.GetWorkCredits(ctx, GetWorkCreditsQuery{WorkID: req.Msg.WorkId}))
}

func (h *ConnectHandler) AddWorkCredit(ctx context.Context, req *connect.Request[managev1.AddWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error) {
	msg := req.Msg
	return respond(h.works.AddWorkCredit(ctx, AddWorkCreditCommand{
		WorkID: msg.WorkId, GroupID: msg.GroupId, ArtistID: msg.ArtistId, MemberID: msg.MemberId, Name: msg.Name, CreditRole: msg.CreditRole,
	}))
}

func (h *ConnectHandler) UpdateWorkCredit(ctx context.Context, req *connect.Request[managev1.UpdateWorkCreditRequest]) (*connect.Response[managev1.WorkCredit], error) {
	return respond(h.works.UpdateWorkCredit(ctx, UpdateWorkCreditCommand{CreditID: req.Msg.CreditId, GroupID: req.Msg.GroupId, CreditRole: req.Msg.CreditRole}))
}

func (h *ConnectHandler) DeleteWorkCredit(ctx context.Context, req *connect.Request[managev1.DeleteWorkCreditRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.works.DeleteWorkCredit(ctx, DeleteWorkCreditCommand{CreditID: req.Msg.CreditId}))
}

func (h *ConnectHandler) MoveWorkCreditItem(ctx context.Context, req *connect.Request[managev1.MoveWorkCreditItemRequest]) (*connect.Response[managev1.MoveWorkCreditItemResponse], error) {
	msg := req.Msg
	return respond(h.works.MoveWorkCreditItem(ctx, MoveWorkCreditItemCommand{
		WorkID: msg.WorkId, Kind: msg.Kind, ItemID: msg.ItemId, TargetGroupID: msg.TargetGroupId, After: msg.After, Before: msg.Before,
	}))
}

func (h *ConnectHandler) CreateWorkCreditGroup(ctx context.Context, req *connect.Request[managev1.CreateWorkCreditGroupRequest]) (*connect.Response[managev1.WorkCreditGroup], error) {
	return respond(h.works.CreateWorkCreditGroup(ctx, CreateWorkCreditGroupCommand{WorkID: req.Msg.WorkId, Name: req.Msg.Name}))
}

func (h *ConnectHandler) UpdateWorkCreditGroup(ctx context.Context, req *connect.Request[managev1.UpdateWorkCreditGroupRequest]) (*connect.Response[managev1.WorkCreditGroup], error) {
	return respond(h.works.UpdateWorkCreditGroup(ctx, UpdateWorkCreditGroupCommand{GroupID: req.Msg.GroupId, Name: req.Msg.Name}))
}

func (h *ConnectHandler) DeleteWorkCreditGroup(ctx context.Context, req *connect.Request[managev1.DeleteWorkCreditGroupRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return deleted(h.works.DeleteWorkCreditGroup(ctx, DeleteWorkCreditGroupCommand{GroupID: req.Msg.GroupId}))
}

func (h *ConnectHandler) ListMyCreditedWorks(ctx context.Context, req *connect.Request[managev1.ListMyCreditedWorksRequest]) (*connect.Response[managev1.ListMyCreditedWorksResponse], error) {
	return respond(h.works.ListMyCreditedWorks(ctx, ListMyCreditedWorksQuery{Page: page(req.Msg.GetPagination()), Filters: req.Msg.Filters, Sorts: req.Msg.Sorts}))
}

func (h *ConnectHandler) ListWorkVersions(ctx context.Context, req *connect.Request[managev1.ListWorkVersionsRequest]) (*connect.Response[managev1.ListWorkVersionsResponse], error) {
	return respond(h.works.ListWorkVersions(ctx, ListWorkVersionsQuery{WorkID: req.Msg.WorkId, Page: page(req.Msg.GetPagination())}))
}

func (h *ConnectHandler) RestoreWorkVersion(ctx context.Context, req *connect.Request[managev1.RestoreWorkVersionRequest]) (*connect.Response[managev1.Work], error) {
	return respond(h.works.RestoreWorkVersion(ctx, RestoreWorkVersionCommand{WorkID: req.Msg.WorkId, VersionID: req.Msg.VersionId}))
}

func (h *ConnectHandler) CheckWorkSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckWorkSlugAvailableRequest]) (*connect.Response[managev1.CheckWorkSlugAvailableResponse], error) {
	return respond(h.works.CheckWorkSlugAvailable(ctx, CheckWorkSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeWorkID: req.Msg.ExcludeWorkId}))
}

// InternalConnectHandler serves the Work collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalWorkServiceHandler
	works *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of works.
func NewInternalConnectHandler(works *Service) *InternalConnectHandler {
	return &InternalConnectHandler{works: works}
}

func (h *InternalConnectHandler) LoadWorkBlockDocument(ctx context.Context, req *connect.Request[intrav1.LoadWorkBlockDocumentRequest]) (*connect.Response[intrav1.LoadWorkBlockDocumentResponse], error) {
	return respond(h.works.LoadWorkBlockDocument(ctx, LoadWorkBlockDocumentQuery{
		WorkID: req.Msg.WorkId, Locale: req.Msg.Locale, SessionID: req.Msg.GetPrincipal().GetSessionId(),
	}))
}

func (h *InternalConnectHandler) ApplyWorkBlockBatch(ctx context.Context, req *connect.Request[intrav1.ApplyWorkBlockBatchRequest]) (*connect.Response[intrav1.ApplyWorkBlockBatchResponse], error) {
	msg := req.Msg
	return respond(h.works.ApplyWorkBlockBatch(ctx, ApplyWorkBlockBatchCommand{
		WorkID: msg.WorkId, Locale: msg.Locale, Batch: msg.Batch,
		AffectedLocaleValues: msg.AffectedLocaleValues, ExpectedTargetRevision: msg.ExpectedTargetRevision,
	}))
}

func (h *InternalConnectHandler) UpdateWorkLocaleMetadata(ctx context.Context, req *connect.Request[intrav1.UpdateWorkLocaleMetadataRequest]) (*connect.Response[intrav1.UpdateWorkLocaleMetadataResponse], error) {
	msg := req.Msg
	command := UpdateWorkLocaleMetadataCommand{
		WorkID: msg.WorkId, Locale: msg.Locale, Title: msg.Title,
		ExpectedRevision: msg.ExpectedRevision, ExpectedTargetRevision: msg.ExpectedTargetRevision,
		ContributorMemberIDs: msg.ContributorMemberIds,
	}
	switch summary := msg.SummaryUpdate.(type) {
	case *intrav1.UpdateWorkLocaleMetadataRequest_Summary:
		command.SetSummary, command.Summary = true, &summary.Summary
	case *intrav1.UpdateWorkLocaleMetadataRequest_ClearSummary:
		command.SetSummary = true
	}
	return respond(h.works.UpdateWorkLocaleMetadata(ctx, command))
}

func (h *InternalConnectHandler) CreateWorkVersionCheckpoint(ctx context.Context, req *connect.Request[intrav1.CreateWorkVersionCheckpointRequest]) (*connect.Response[intrav1.CreateWorkVersionCheckpointResponse], error) {
	msg := req.Msg
	return respond(h.works.CreateWorkVersionCheckpoint(ctx, CreateWorkVersionCheckpointCommand{
		WorkID: msg.WorkId, Locale: msg.Locale, ExpectedRevision: msg.ExpectedRevision, ContributorMemberIDs: msg.ContributorMemberIds,
	}))
}
