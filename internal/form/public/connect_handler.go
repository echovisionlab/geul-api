package public

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/requestip"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/open/v1/openv1connect"
)

// ConnectHandler serves the public Form RPCs.
type ConnectHandler struct {
	openv1connect.UnimplementedFormServiceHandler
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

func (h *ConnectHandler) CheckAccess(ctx context.Context, req *connect.Request[openv1.CheckFormAccessRequest]) (*connect.Response[openv1.CheckFormAccessResponse], error) {
	access, err := h.forms.CheckFormAccess(ctx, CheckFormAccessQuery{
		Slug:           req.Msg.Slug,
		Dashboard:      normalizeFormAccessTarget(req.Msg.Target) == openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD,
		Embed:          normalizeFormAccessContext(req.Msg.Context) == openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED,
		ShareToken:     req.Msg.ShareToken,
		SharePassword:  req.Msg.SharePassword,
		Password:       req.Msg.Password,
		AcceptLanguage: req.Header().Get("Accept-Language"),
		ClientAddress:  requestClientAddress(req),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	response := &openv1.CheckFormAccessResponse{Accessible: access.Accessible, Reason: accessReasonMessage(access.Reason)}
	if access.Form != nil {
		response.Form = formMessage(*access.Form)
	}
	return connect.NewResponse(response), nil
}

func (h *ConnectHandler) Submit(ctx context.Context, req *connect.Request[openv1.SubmitFormRequest]) (*connect.Response[openv1.SubmitFormResponse], error) {
	submissionID, err := h.forms.SubmitForm(ctx, submitFormCommand(req))
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.SubmitFormResponse{SubmissionId: submissionID}), nil
}

// submitFormCommand reads the answers and the client the request came from.
func submitFormCommand(req *connect.Request[openv1.SubmitFormRequest]) SubmitFormCommand {
	return SubmitFormCommand{
		FormID: req.Msg.FormId, Data: req.Msg.Data, Password: req.Msg.Password,
		ClientAddress:  requestClientAddress(req),
		UserAgent:      req.Header().Get("User-Agent"),
		AcceptLanguage: req.Header().Get("Accept-Language"),
	}
}

func (h *ConnectHandler) VerifyPassword(ctx context.Context, req *connect.Request[openv1.VerifyFormPasswordRequest]) (*connect.Response[openv1.VerifyFormPasswordResponse], error) {
	valid, err := h.forms.VerifyFormPassword(ctx, VerifyFormPasswordQuery{
		Slug: req.Msg.Slug, Password: req.Msg.Password, ShareToken: req.Msg.ShareToken, SharePassword: req.Msg.SharePassword,
		ClientAddress: requestClientAddress(req),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&openv1.VerifyFormPasswordResponse{Valid: valid}), nil
}

// requestClientAddress is the trusted client the request came from.
func requestClientAddress(req connect.AnyRequest) string {
	return requestip.TrustedClientIP(req.Header().Get("X-Forwarded-For"), req.Header().Get("X-Real-IP"), req.Peer().Addr)
}

func (h *ConnectHandler) GetDashboard(ctx context.Context, req *connect.Request[openv1.GetFormDashboardRequest]) (*connect.Response[openv1.GetFormDashboardResponse], error) {
	dashboard, err := h.forms.GetFormDashboard(ctx, GetFormDashboardQuery{
		Slug: req.Msg.Slug, ShareToken: req.Msg.ShareToken, SharePassword: req.Msg.SharePassword,
		AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return respond(&openv1.GetFormDashboardResponse{Dashboard: dashboardMessage(dashboard)}, nil)
}

func normalizeFormAccessContext(context openv1.FormAccessContext) openv1.FormAccessContext {
	switch context {
	case openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED:
		return openv1.FormAccessContext_FORM_ACCESS_CONTEXT_EMBED
	case openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL:
		return openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL
	default:
		return openv1.FormAccessContext_FORM_ACCESS_CONTEXT_URL
	}
}

func normalizeFormAccessTarget(target openv1.FormAccessTarget) openv1.FormAccessTarget {
	switch target {
	case openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD:
		return openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD
	case openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM:
		return openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM
	default:
		return openv1.FormAccessTarget_FORM_ACCESS_TARGET_FORM
	}
}

// accessReasonMessages maps each access reason to the API enum.
var accessReasonMessages = map[AccessReason]openv1.FormAccessReason{
	ReasonUnspecified:           openv1.FormAccessReason_FORM_ACCESS_REASON_UNSPECIFIED,
	ReasonAllowed:               openv1.FormAccessReason_FORM_ACCESS_REASON_ALLOWED,
	ReasonFormNotFound:          openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND,
	ReasonFormNotPublished:      openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_PUBLISHED,
	ReasonNotPublic:             openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_PUBLIC,
	ReasonAuthRequired:          openv1.FormAccessReason_FORM_ACCESS_REASON_AUTH_REQUIRED,
	ReasonRoleNotAllowed:        openv1.FormAccessReason_FORM_ACCESS_REASON_ROLE_NOT_ALLOWED,
	ReasonPasswordRequired:      openv1.FormAccessReason_FORM_ACCESS_REASON_PASSWORD_REQUIRED,
	ReasonNotYetOpen:            openv1.FormAccessReason_FORM_ACCESS_REASON_NOT_YET_OPEN,
	ReasonClosed:                openv1.FormAccessReason_FORM_ACCESS_REASON_CLOSED,
	ReasonMaxSubmissionsReached: openv1.FormAccessReason_FORM_ACCESS_REASON_MAX_SUBMISSIONS_REACHED,
	ReasonAlreadySubmitted:      openv1.FormAccessReason_FORM_ACCESS_REASON_ALREADY_SUBMITTED,
}

func accessReasonMessage(reason AccessReason) openv1.FormAccessReason {
	return accessReasonMessages[reason]
}

// formStatusToOpen maps a stored form status to the public API enum. A value
// the API does not know is UNSPECIFIED.
func formStatusToOpen(status form.Status) openv1.FormStatus {
	switch status {
	case form.StatusDraft:
		return openv1.FormStatus_FORM_STATUS_DRAFT
	case form.StatusPublished:
		return openv1.FormStatus_FORM_STATUS_PUBLISHED
	default:
		return openv1.FormStatus_FORM_STATUS_UNSPECIFIED
	}
}

func formMessage(view View) *openv1.Form {
	if view.Metadata {
		return formMetadataMessage(view)
	}
	f := view.Form
	protoForm := &openv1.Form{
		Id:                       f.ID,
		Title:                    view.Document.Title,
		Schema:                   view.Document.Schema,
		Status:                   formStatusToOpen(f.Status),
		IsPublic:                 f.IsPublic,
		RequireAuth:              f.RequireAuth != nil && *f.RequireAuth,
		AllowedRoles:             slices.Clone(f.AllowedRoles),
		AllowDuplicateSubmission: f.AllowDuplicateSubmission != nil && *f.AllowDuplicateSubmission,
		HasPassword:              f.HasPassword(),
		CreatedAt:                timestamppb.New(f.CreatedAt),
	}

	if f.Slug != nil {
		protoForm.Slug = f.Slug
	}
	if f.MaxSubmissions != nil {
		protoForm.MaxSubmissions = f.MaxSubmissions
	}
	if f.OpensAt != nil {
		protoForm.OpensAt = timestamppb.New(*f.OpensAt)
	}
	if f.ClosesAt != nil {
		protoForm.ClosesAt = timestamppb.New(*f.ClosesAt)
	}
	if f.UpdatedAt != nil {
		protoForm.UpdatedAt = timestamppb.New(*f.UpdatedAt)
	}
	protoForm.FeaturedImageAsset = form.AssetMessage(view.FeaturedImage)
	protoForm.OgAsset = form.AssetMessage(view.OgAsset)
	if view.Localization != nil {
		protoForm.LocalizationInfo = localizationInfoMessage(*view.Localization)
	}

	return protoForm
}

func formMetadataMessage(view View) *openv1.Form {
	f := view.Form
	protoForm := &openv1.Form{
		Id:          f.ID,
		Title:       view.Document.Title,
		Status:      formStatusToOpen(f.Status),
		IsPublic:    f.IsPublic,
		HasPassword: f.HasPassword(),
		CreatedAt:   timestamppb.New(f.CreatedAt),
	}

	if f.Slug != nil {
		protoForm.Slug = f.Slug
	}
	if f.UpdatedAt != nil {
		protoForm.UpdatedAt = timestamppb.New(*f.UpdatedAt)
	}
	protoForm.OgAsset = form.AssetMessage(view.OgAsset)
	protoForm.FeaturedImageAsset = form.AssetMessage(view.FeaturedImage)
	if view.Localization != nil {
		protoForm.LocalizationInfo = localizationInfoMessage(*view.Localization)
	}

	return protoForm
}

func localizationInfoMessage(selection LocalizationSelection) *openv1.LocalizationInfo {
	return &openv1.LocalizationInfo{
		RequestedLocale: selection.RequestedLocale, DisplayedLocale: selection.DisplayedLocale,
		SourceLocale: selection.SourceLocale, IsFallback: selection.IsFallback, IsOriginal: selection.IsOriginal,
		FallbackReason: selection.FallbackReason, AvailableLocales: selection.AvailableLocales,
	}
}

func dashboardMessage(dashboard Dashboard) *openv1.FormDashboard {
	fieldStats := make([]*openv1.FormDashboardFieldStat, len(dashboard.FieldStats))
	for i, field := range dashboard.FieldStats {
		values := make([]*openv1.FormDashboardFieldValue, len(field.Values))
		for j, value := range field.Values {
			values[j] = &openv1.FormDashboardFieldValue{Value: value.Value, Count: value.Count}
		}
		fieldStats[i] = &openv1.FormDashboardFieldStat{FieldId: field.FieldID, FieldLabel: field.FieldLabel, Values: values}
	}
	return &openv1.FormDashboard{
		FormId:               dashboard.FormID,
		FormTitle:            dashboard.FormTitle,
		FormSlug:             dashboard.FormSlug,
		TotalSubmissions:     dashboard.TotalSubmissions,
		SubmissionsToday:     dashboard.SubmissionsToday,
		SubmissionsThisWeek:  dashboard.SubmissionsThisWeek,
		SubmissionsThisMonth: dashboard.SubmissionsThisMonth,
		FieldStats:           fieldStats,
	}
}
