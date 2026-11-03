package form

import (
	"context"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// ConnectHandler serves the Form management RPCs.
type ConnectHandler struct {
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

// respondWith translates a use-case result with message.
func respondWith[R, T any](result R, err error, message func(R) *T) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message(result)), nil
}

func (h *ConnectHandler) GetForm(ctx context.Context, req *connect.Request[managev1.GetFormRequest]) (*connect.Response[managev1.Form], error) {
	view, err := h.forms.GetForm(ctx, GetFormQuery{IDOrSlug: req.Msg.Id})
	return respondWith(view, err, formMessage)
}

func (h *ConnectHandler) ListFormsAdmin(ctx context.Context, req *connect.Request[managev1.ListFormsAdminRequest]) (*connect.Response[managev1.ListFormsAdminResponse], error) {
	pagination := req.Msg.GetPagination()
	list, err := h.forms.ListFormsAdmin(ctx, ListFormsAdminQuery{
		Page: Page{Limit: pagination.GetLimit(), Offset: pagination.GetOffset()}, Filters: req.Msg.Filters, Sorts: req.Msg.Sorts,
	})
	return respondWith(list, err, formListMessage)
}

func (h *ConnectHandler) CreateForm(ctx context.Context, req *connect.Request[managev1.CreateFormRequest]) (*connect.Response[managev1.Form], error) {
	view, err := h.forms.CreateForm(ctx, CreateFormCommand{
		Title: req.Msg.Title, Slug: req.Msg.Slug, Schema: req.Msg.Schema, IsPublic: req.Msg.IsPublic,
		RequireAuth: req.Msg.RequireAuth, AllowDuplicateSubmission: req.Msg.AllowDuplicateSubmission,
		Password: req.Msg.Password, MaxSubmissions: req.Msg.MaxSubmissions,
		OpensAt: optionalTime(req.Msg.OpensAt), ClosesAt: optionalTime(req.Msg.ClosesAt),
		AllowedRoles: req.Msg.AllowedRoles, AcceptLanguage: req.Header().Get("Accept-Language"),
	})
	return respondWith(view, err, formMessage)
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
	view, err := h.forms.UpdateForm(ctx, command)
	return respondWith(view, err, formMessage)
}

func (h *ConnectHandler) DeleteForm(ctx context.Context, req *connect.Request[managev1.DeleteFormRequest]) (*connect.Response[managev1.DeleteResponse], error) {
	return respond(&managev1.DeleteResponse{Success: true}, h.forms.DeleteForm(ctx, DeleteFormCommand{ID: req.Msg.Id}))
}

func (h *ConnectHandler) SetFormFeaturedImage(ctx context.Context, req *connect.Request[managev1.SetFormFeaturedImageRequest]) (*connect.Response[managev1.SetFormFeaturedImageResponse], error) {
	change, err := h.forms.SetFormFeaturedImage(ctx, SetFormFeaturedImageCommand{FormID: req.Msg.FormId, FileID: req.Msg.FileId})
	return respondWith(change, err, func(change FeaturedImageChange) *managev1.SetFormFeaturedImageResponse {
		return &managev1.SetFormFeaturedImageResponse{ImageAsset: AssetMessage(change.ImageAsset), OgGenerationRunId: change.OgGenerationRunID}
	})
}

func (h *ConnectHandler) DeleteFormFeaturedImage(ctx context.Context, req *connect.Request[managev1.DeleteFormFeaturedImageRequest]) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	runID, err := h.forms.DeleteFormFeaturedImage(ctx, DeleteFormFeaturedImageCommand{FormID: req.Msg.FormId})
	return respondWith(runID, err, func(runID *string) *managev1.OgAssetDeleteResponse {
		return &managev1.OgAssetDeleteResponse{Success: true, OgGenerationRunId: runID}
	})
}

func (h *ConnectHandler) CheckFormSlugAvailable(ctx context.Context, req *connect.Request[managev1.CheckFormSlugAvailableRequest]) (*connect.Response[managev1.CheckFormSlugAvailableResponse], error) {
	available, err := h.forms.CheckFormSlugAvailable(ctx, CheckFormSlugAvailableQuery{Slug: req.Msg.Slug, ExcludeID: req.Msg.GetExcludeId()})
	return respondWith(available, err, func(available bool) *managev1.CheckFormSlugAvailableResponse {
		return &managev1.CheckFormSlugAvailableResponse{Available: available}
	})
}

func (h *ConnectHandler) ListFormSubmissions(ctx context.Context, req *connect.Request[managev1.ListFormSubmissionsRequest]) (*connect.Response[managev1.ListFormSubmissionsResponse], error) {
	query, err := listFormSubmissionsQuery(req.Msg)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	page, err := h.forms.ListFormSubmissions(ctx, query)
	return respondWith(page, err, func(page SubmissionPage) *managev1.ListFormSubmissionsResponse {
		submissions := make([]*managev1.FormSubmission, len(page.Submissions))
		for i, submission := range page.Submissions {
			submissions[i] = submissionMessage(submission)
		}
		return &managev1.ListFormSubmissionsResponse{Submissions: submissions, Pagination: paginationMessage(page.Pagination)}
	})
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
	stats, err := h.forms.GetFormSubmissionStats(ctx, GetFormSubmissionStatsQuery{FormID: req.Msg.FormId})
	return respondWith(stats, err, func(stats SubmissionStats) *managev1.GetFormSubmissionStatsResponse {
		return &managev1.GetFormSubmissionStatsResponse{Stats: submissionStatsMessage(stats)}
	})
}

func (h *ConnectHandler) GetFormSubmissionWithSchema(ctx context.Context, req *connect.Request[managev1.GetFormSubmissionRequest]) (*connect.Response[managev1.FormSubmissionWithSchema], error) {
	result, err := h.forms.GetFormSubmissionWithSchema(ctx, GetFormSubmissionQuery{ID: req.Msg.Id})
	return respondWith(result, err, func(result SubmissionWithSchema) *managev1.FormSubmissionWithSchema {
		return &managev1.FormSubmissionWithSchema{
			Submission: submissionMessage(result.Submission), FormSchema: result.FormSchema, FormTitle: result.FormTitle,
		}
	})
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

// StatusMessage maps a stored form status to the API enum. A value the API
// does not know is UNSPECIFIED.
func StatusMessage(status Status) managev1.FormStatus {
	switch status {
	case StatusDraft:
		return managev1.FormStatus_FORM_STATUS_DRAFT
	case StatusPublished:
		return managev1.FormStatus_FORM_STATUS_PUBLISHED
	default:
		return managev1.FormStatus_FORM_STATUS_UNSPECIFIED
	}
}

// formStatusFromProto maps an API form status to the stored value. It
// reports false for UNSPECIFIED and unknown values, which a client may not set.
func formStatusFromProto(status managev1.FormStatus) (Status, bool) {
	switch status {
	case managev1.FormStatus_FORM_STATUS_DRAFT:
		return StatusDraft, true
	case managev1.FormStatus_FORM_STATUS_PUBLISHED:
		return StatusPublished, true
	default:
		return "", false
	}
}

// AssetMessage renders a public asset; nil stays nil.
func AssetMessage(asset *Asset) *commonv1.AssetRef {
	if asset == nil {
		return nil
	}
	return &commonv1.AssetRef{
		AssetId: asset.ID, Url: asset.URL, Extension: asset.Extension, MimeType: asset.MIMEType,
		FileSize: asset.FileSize, Sha256: asset.SHA256, Disposition: commonv1.AssetDisposition(asset.Disposition),
		DownloadFilename: asset.DownloadFilename,
	}
}

func formMessage(view View) *managev1.Form {
	f := view.Form
	form := &managev1.Form{
		Id:           f.ID,
		SourceLocale: f.SourceLocale,
		Title:        view.Title,
		Schema:       view.Schema,
		Status:       StatusMessage(f.Status),
		IsPublic:     f.IsPublic,
		AllowedRoles: slices.Clone(f.AllowedRoles),
		CreatedAt:    timestamppb.New(f.CreatedAt),
		OgAsset:      AssetMessage(view.OgAsset),
	}

	if f.Slug != nil {
		form.Slug = f.Slug
	}
	if f.RequireAuth != nil {
		form.RequireAuth = f.RequireAuth
	}
	if f.AllowDuplicateSubmission != nil {
		form.AllowDuplicateSubmission = f.AllowDuplicateSubmission
	}
	if f.MaxSubmissions != nil {
		form.MaxSubmissions = f.MaxSubmissions
	}
	if f.OpensAt != nil {
		form.OpensAt = timestamppb.New(*f.OpensAt)
	}
	if f.ClosesAt != nil {
		form.ClosesAt = timestamppb.New(*f.ClosesAt)
	}
	if f.UpdatedAt != nil {
		form.UpdatedAt = timestamppb.New(*f.UpdatedAt)
	}
	if view.FeaturedImage != nil {
		form.FeaturedImageAsset = AssetMessage(view.FeaturedImage)
	}
	// HasPassword indicates if form is password protected (don't expose the hash)
	form.HasPassword = f.HasPassword()

	return form
}

// formListMessage renders a page of Forms as the admin list answers it.
func formListMessage(list FormList) *managev1.ListFormsAdminResponse {
	summaries := make([]*managev1.FormSummary, len(list.Forms))
	for i, summary := range list.Forms {
		summaries[i] = formSummaryMessage(summary)
	}
	return &managev1.ListFormsAdminResponse{Forms: summaries, Pagination: paginationMessage(list.Pagination)}
}

func formSummaryMessage(listed Summary) *managev1.FormSummary {
	f := listed.Form
	summary := &managev1.FormSummary{
		Id:              f.ID,
		Title:           listed.Title,
		Status:          StatusMessage(f.Status),
		SubmissionCount: int32(listed.SubmissionCount),
		CreatedAt:       timestamppb.New(f.CreatedAt),
		SourceLocale:    f.SourceLocale,
	}

	if f.Slug != nil {
		summary.Slug = f.Slug
	}
	if f.UpdatedAt != nil {
		summary.UpdatedAt = timestamppb.New(*f.UpdatedAt)
	}

	return summary
}

func paginationMessage(pagination Pagination) *commonv1.PaginationResponse {
	return &commonv1.PaginationResponse{
		Total: pagination.Total, Limit: pagination.Limit, Offset: pagination.Offset, HasMore: pagination.HasMore,
	}
}

func submissionMessage(sub Submission) *managev1.FormSubmission {
	submission := &managev1.FormSubmission{
		Id:          sub.ID,
		FormId:      sub.FormID,
		CountryCode: sub.CountryCode,
		Data:        sub.Data,
		CreatedAt:   timestamppb.New(sub.CreatedAt),
	}

	if sub.MemberID != nil {
		submission.MemberId = sub.MemberID
	}
	if sub.IPAddress != nil {
		submission.IpAddress = sub.IPAddress
	}
	if sub.UserAgent != nil {
		submission.UserAgent = sub.UserAgent
	}

	return submission
}

func submissionStatsMessage(stats SubmissionStats) *managev1.FormSubmissionStats {
	fieldStats := make([]*managev1.FormSubmissionFieldStat, len(stats.FieldStats))
	for i, field := range stats.FieldStats {
		values := make([]*managev1.FormSubmissionFieldValue, len(field.Values))
		for j, value := range field.Values {
			values[j] = &managev1.FormSubmissionFieldValue{Value: value.Value, Count: value.Count}
		}
		fieldStats[i] = &managev1.FormSubmissionFieldStat{FieldId: field.FieldID, FieldLabel: field.FieldLabel, Values: values}
	}
	return &managev1.FormSubmissionStats{
		TotalSubmissions:     stats.TotalSubmissions,
		SubmissionsToday:     stats.SubmissionsToday,
		SubmissionsThisWeek:  stats.SubmissionsThisWeek,
		SubmissionsThisMonth: stats.SubmissionsThisMonth,
		FieldStats:           fieldStats,
	}
}

// InternalConnectHandler serves the Form collaboration RPCs editor-collab
// calls.
type InternalConnectHandler struct {
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
	command := SaveFormDocumentCommand{
		FormID: req.Msg.FormId, Locale: req.Msg.Locale, ExpectedDocumentRevision: req.Msg.ExpectedDocumentRevision,
		ExpectedTargetRevision: req.Msg.ExpectedTargetRevision, ContributorMemberIDs: req.Msg.ContributorMemberIds,
		PresentLocaleValues: req.Msg.PresentLocaleValues,
	}
	if meta := req.Msg.Meta; meta != nil {
		command.Meta = &DocumentMeta{Title: meta.Title, Schema: meta.Schema}
	}
	saved, err := h.forms.SaveFormDocument(ctx, command)
	return respondWith(saved, err, func(saved DocumentSave) *intrav1.SaveFormDocumentResponse {
		return &intrav1.SaveFormDocumentResponse{
			Success: true, Locale: saved.Locale, DocumentRevision: saved.DocumentRevision, TargetRevision: saved.TargetRevision,
		}
	})
}

func (h *InternalConnectHandler) LoadDocument(ctx context.Context, req *connect.Request[intrav1.LoadFormDocumentRequest]) (*connect.Response[intrav1.LoadFormDocumentResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, errs.InvalidArgument("request", "request is required")
	}
	document, err := h.forms.LoadFormDocument(ctx, LoadFormDocumentQuery{FormID: req.Msg.FormId, Locale: req.Msg.Locale})
	return respondWith(document, err, func(document CollaborationDocument) *intrav1.LoadFormDocumentResponse {
		response := &intrav1.LoadFormDocumentResponse{
			SourceMetadata: formMetaMessage(document.SourceMetadata), SourceLocale: document.SourceLocale, Locale: document.Locale,
			DocumentRevision: document.DocumentRevision, LocaleExists: document.LocaleExists,
			PresentLocaleValues: document.PresentLocaleValues, TargetRevision: document.TargetRevision,
		}
		if document.LocaleMetadata != nil {
			response.LocaleMetadata = formMetaMessage(*document.LocaleMetadata)
		}
		return response
	})
}

func formMetaMessage(meta DocumentMeta) *intrav1.FormMeta {
	return &intrav1.FormMeta{Title: meta.Title, Schema: meta.Schema}
}
