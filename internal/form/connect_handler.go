package form

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

// ConnectHandler serves the Form management RPCs.
type ConnectHandler struct {
	managev1connect.UnimplementedFormServiceHandler
	forms *Service
}

// NewConnectHandler returns the Connect adapter of forms.
func NewConnectHandler(forms *Service) *ConnectHandler {
	return &ConnectHandler{forms: forms}
}

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

func (h *ConnectHandler) GetForm(ctx context.Context, req *connect.Request[managev1.GetFormRequest]) (*connect.Response[managev1.Form], error) {
	return respond(h.forms.GetForm(ctx, GetFormQuery{IDOrSlug: req.Msg.Id}))
}

func (h *ConnectHandler) ListFormsAdmin(ctx context.Context, req *connect.Request[managev1.ListFormsAdminRequest]) (*connect.Response[managev1.ListFormsAdminResponse], error) {
	pagination := req.Msg.GetPagination()
	return respond(h.forms.ListFormsAdmin(ctx, ListFormsAdminQuery{
		Page: Page{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()}, Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	}))
}

func (h *ConnectHandler) CreateForm(ctx context.Context, req *connect.Request[managev1.CreateFormRequest]) (*connect.Response[managev1.Form], error) {
	return respond(h.forms.CreateForm(ctx, CreateFormCommand{
		Title: req.Msg.Title, Slug: req.Msg.Slug, Schema: req.Msg.Schema, IsPublic: req.Msg.IsPublic,
		RequireAuth: req.Msg.RequireAuth, AllowDuplicateSubmission: req.Msg.AllowDuplicateSubmission,
		Password: req.Msg.Password, MaxSubmissions: req.Msg.MaxSubmissions,
		OpensAt: optionalTime(req.Msg.OpensAt), ClosesAt: optionalTime(req.Msg.ClosesAt),
		AllowedRoles: req.Msg.AllowedRoles, AcceptLanguage: req.Header().Get("Accept-Language"),
	}))
}

func (h *ConnectHandler) UpdateForm(ctx context.Context, req *connect.Request[managev1.UpdateFormRequest]) (*connect.Response[managev1.Form], error) {
	command := UpdateFormCommand{
		ID: req.Msg.Id, Title: req.Msg.Title, Slug: req.Msg.Slug, Schema: req.Msg.Schema, IsPublic: req.Msg.IsPublic,
		RequireAuth: req.Msg.RequireAuth, AllowDuplicateSubmission: req.Msg.AllowDuplicateSubmission,
		Password: req.Msg.Password, MaxSubmissions: req.Msg.MaxSubmissions,
		OpensAt: optionalTime(req.Msg.OpensAt), ClosesAt: optionalTime(req.Msg.ClosesAt),
		AllowedRoles: req.Msg.AllowedRoles, ReplaceAllowedRoles: req.Msg.ReplaceAllowedRoles,
		ClearSlug: req.Msg.ClearSlug, ClearOpensAt: req.Msg.ClearOpensAt, ClearClosesAt: req.Msg.ClearClosesAt,
		ClearMaxSubmissions: req.Msg.ClearMaxSubmissions,
	}
	if req.Msg.Status != nil {
		// A value the API does not map is the empty status, which the use
		// case rejects.
		status, _ := formStatusFromProto(*req.Msg.Status)
		command.Status = &status
	}
	return respond(h.forms.UpdateForm(ctx, command))
}

func (h *ConnectHandler) DeleteForm(ctx context.Context, req *connect.Request[managev1.DeleteFormRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, h.forms.DeleteForm(ctx, DeleteFormCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) SetFormFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetFormFeaturedImageRequest]) (*connect.Response[managev1.SetFormFeaturedImageResponse], error) {
	return respond(h.forms.SetFormFeaturedImage(ctx, SetFormFeaturedImageCommand{FormID: req.Msg.FormId, FileID: req.Msg.FileId}))
}

func (h *ConnectHandler) DeleteFormFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeleteFormFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	return respond(h.forms.DeleteFormFeaturedImage(ctx, DeleteFormFeaturedImageCommand{FormID: req.Msg.FormId}))
}

func (h *ConnectHandler) CheckFormSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckFormSlugAvailableRequest]) (*connect.Response[managev1.CheckFormSlugAvailableResponse], error) {
	return respond(h.forms.CheckFormSlugAvailable(ctx, CheckFormSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeID: req.Msg.GetExcludeId()}))
}

func (h *ConnectHandler) ListFormSubmissions(ctx context.Context, req *connect.Request[managev1.ListFormSubmissionsRequest]) (*connect.Response[managev1.ListFormSubmissionsResponse], error) {
	query, err := listFormSubmissionsQuery(req.Msg)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(h.forms.ListFormSubmissions(ctx, query))
}

// listFormSubmissionsQuery translates the request; a creation bound outside
// the timestamp range is invalid.
func listFormSubmissionsQuery(msg *managev1.ListFormSubmissionsRequest) (ListFormSubmissionsQuery, error) {
	pagination := msg.GetPagination()
	query := ListFormSubmissionsQuery{
		FormID: msg.FormId, Search: msg.GetSearch(), CountryCode: msg.GetCountryCode(),
		Page: Page{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()}, Sorts: msg.Sorts,
	}
	var err error
	if query.CreatedAtFrom, err = validTime("created_at_from", msg.CreatedAtFrom); err != nil {
		return ListFormSubmissionsQuery{}, err
	}
	if query.CreatedAtBefore, err = validTime("created_at_before", msg.CreatedAtBefore); err != nil {
		return ListFormSubmissionsQuery{}, err
	}
	return query, nil
}

func (h *ConnectHandler) GetFormSubmissionStats(ctx context.Context, req *connect.Request[managev1.GetFormSubmissionStatsRequest]) (*connect.Response[managev1.GetFormSubmissionStatsResponse], error) {
	return respond(h.forms.GetFormSubmissionStats(ctx, GetFormSubmissionStatsQuery{FormID: req.Msg.FormId}))
}

func (h *ConnectHandler) GetFormSubmissionWithSchema(ctx context.Context, req *connect.Request[managev1.GetFormSubmissionRequest]) (*connect.Response[managev1.FormSubmissionWithSchema], error) {
	return respond(h.forms.GetFormSubmissionWithSchema(ctx, GetFormSubmissionQuery{ID: req.Msg.Id}))
}

func (h *ConnectHandler) DeleteFormSubmission(ctx context.Context, req *connect.Request[managev1.DeleteFormSubmissionRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, h.forms.DeleteFormSubmission(ctx, DeleteFormSubmissionCommand{ID: req.Msg.Id}))
}

func optionalTime(value *timestamppb.Timestamp) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.AsTime()
	return &converted
}

func validTime(field string, value *timestamppb.Timestamp) (*time.Time, error) {
	if value == nil {
		return nil, nil
	}
	if err := value.CheckValid(); err != nil {
		return nil, domainerr.InvalidField(field, "must be a valid timestamp").WithCause(err)
	}
	return optionalTime(value), nil
}

// InternalConnectHandler serves the Form collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
	intrav1connect.UnimplementedInternalFormServiceHandler
	forms *Service
}

// NewInternalConnectHandler returns the internal Connect adapter of forms.
func NewInternalConnectHandler(forms *Service) *InternalConnectHandler {
	return &InternalConnectHandler{forms: forms}
}

func (h *InternalConnectHandler) SaveDocument(ctx context.Context, req *connect.Request[intrav1.SaveFormDocumentRequest]) (*connect.Response[intrav1.SaveFormDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	return respond(h.forms.SaveFormDocument(ctx, SaveFormDocumentCommand{
		FormID: req.Msg.FormId, Locale: req.Msg.Locale, ExpectedDocumentRevision: req.Msg.ExpectedDocumentRevision,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, ContributorMemberIDs: req.Msg.ContributorMemberIds,
		Meta: req.Msg.Meta, PresentLocaleValues: req.Msg.PresentLocaleValues,
	}))
}

func (h *InternalConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadFormDocumentRequest]) (*connect.Response[intrav1.LoadFormDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	return respond(h.forms.LoadFormDocument(ctx, LoadFormDocumentQuery{FormID: req.Msg.FormId, Locale: req.Msg.Locale}))
}
