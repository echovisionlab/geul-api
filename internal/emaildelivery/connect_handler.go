package emaildelivery

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1/intrav1connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
)

func respond[T any](message *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(message), nil
}

// MailAdapterConnectHandler serves the Mail Adapter Connect RPCs. Each
// method translates the request into a use-case command or query, and the
// result into the response; errs.Wrap maps use-case errors to Connect codes.
type MailAdapterConnectHandler struct {
	managev1connect.UnimplementedMailAdapterServiceHandler
	delivery *Service
}

// NewMailAdapterConnectHandler returns the Mail Adapter Connect adapter of
// delivery.
func NewMailAdapterConnectHandler(delivery *Service) *MailAdapterConnectHandler {
	return &MailAdapterConnectHandler{delivery: delivery}
}

// mailAdapterTypeToProto maps a stored mail adapter type to the API enum. A
// value the API does not know is UNSPECIFIED.
func mailAdapterTypeToProto(adapterType AdapterType) managev1.MailAdapterType {
	switch adapterType {
	case AdapterTypeLogging:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING
	case AdapterTypeSES:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES
	case AdapterTypeSMTP:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP
	default:
		return managev1.MailAdapterType_MAIL_ADAPTER_TYPE_UNSPECIFIED
	}
}

// mailAdapterTypeFromProto maps an API mail adapter type to the stored value.
// It reports false for UNSPECIFIED and unknown values, which a client may
// not set.
func mailAdapterTypeFromProto(adapterType managev1.MailAdapterType) (AdapterType, bool) {
	switch adapterType {
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_LOGGING:
		return AdapterTypeLogging, true
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SES:
		return AdapterTypeSES, true
	case managev1.MailAdapterType_MAIL_ADAPTER_TYPE_SMTP:
		return AdapterTypeSMTP, true
	default:
		return "", false
	}
}

// requestedMailAdapterType maps the API type of a request. A type the domain
// does not store is the empty type, which the use case rejects.
func requestedMailAdapterType(adapterType managev1.MailAdapterType) AdapterType {
	stored, _ := mailAdapterTypeFromProto(adapterType)
	return stored
}

// mailAdapterMessage never exposes the SES secret access key or SMTP password.
func mailAdapterMessage(ctx context.Context, adapter MailAdapter) *managev1.MailAdapter {
	message := &managev1.MailAdapter{
		Id:        adapter.ID,
		Name:      adapter.Name,
		Type:      mailAdapterTypeToProto(adapter.Type),
		IsActive:  adapter.IsActive,
		Priority:  int32(adapter.Priority),
		CreatedAt: adapter.CreatedAt.Format(time.RFC3339),
	}
	if adapter.UpdatedAt != nil {
		message.UpdatedAt = adapter.UpdatedAt.Format(time.RFC3339)
	}
	ses, smtp := adapter.ProviderConfig(ctx)
	if ses != nil {
		config := &managev1.SESConfig{Region: ses.Region, AccessKeyId: ses.AccessKeyID, FromEmail: ses.FromEmail}
		if ses.FromName != "" {
			config.FromName = &ses.FromName
		}
		message.Config = &managev1.MailAdapter_SesConfig{SesConfig: config}
	}
	if smtp != nil {
		config := &managev1.SMTPConfig{
			Host: smtp.Host, Port: int32(smtp.Port), Secure: smtp.Secure, User: smtp.User, FromEmail: smtp.FromEmail,
		}
		if smtp.FromName != "" {
			config.FromName = &smtp.FromName
		}
		message.Config = &managev1.MailAdapter_SmtpConfig{SmtpConfig: config}
	}
	return message
}

func testSendMessage(result TestSendResult, err error) (*managev1.TestMailAdapterResponse, error) {
	if err != nil {
		return nil, err
	}
	return &managev1.TestMailAdapterResponse{Success: result.Success, Error: result.Error, MessageId: result.MessageID}, nil
}

// activeOnly reports whether filters ask for active adapters, the only
// filter the listing applies.
func activeOnly(filters []*commonv1.FilterSpec) bool {
	for _, filter := range filters {
		if filter != nil && filter.GetField() == "is_active" && filter.GetValue() == "true" {
			return true
		}
	}
	return false
}

func sesConfigInput(config *managev1.SESConfig) *SESConfigInput {
	if config == nil {
		return nil
	}
	return &SESConfigInput{
		Region: config.Region, AccessKeyID: config.AccessKeyId, SecretAccessKey: config.SecretAccessKey,
		FromEmail: config.FromEmail, FromName: config.FromName,
	}
}

func smtpConfigInput(config *managev1.SMTPConfig) *SMTPConfigInput {
	if config == nil {
		return nil
	}
	return &SMTPConfigInput{
		Host: config.Host, Port: config.Port, Secure: config.Secure, User: config.User, Password: config.Password,
		FromEmail: config.FromEmail, FromName: config.FromName,
	}
}

func (h *MailAdapterConnectHandler) Create(ctx context.Context, req *connect.Request[managev1.CreateMailAdapterRequest]) (*connect.Response[managev1.CreateMailAdapterResponse], error) {
	adapter, err := h.delivery.CreateMailAdapter(ctx, CreateMailAdapterCommand{
		Name: req.Msg.Name, Type: requestedMailAdapterType(req.Msg.Type),
		SES: sesConfigInput(req.Msg.GetSesConfig()), SMTP: smtpConfigInput(req.Msg.GetSmtpConfig()),
		IsActive: req.Msg.IsActive, Priority: req.Msg.GetPriority(),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.CreateMailAdapterResponse{Adapter: mailAdapterMessage(ctx, adapter)}), nil
}

func (h *MailAdapterConnectHandler) Update(ctx context.Context, req *connect.Request[managev1.UpdateMailAdapterRequest]) (*connect.Response[managev1.UpdateMailAdapterResponse], error) {
	command := UpdateMailAdapterCommand{
		ID: req.Msg.Id, Name: req.Msg.Name,
		SES: sesConfigInput(req.Msg.GetSesConfig()), SMTP: smtpConfigInput(req.Msg.GetSmtpConfig()),
		IsActive: req.Msg.IsActive, Priority: req.Msg.Priority,
	}
	if req.Msg.Type != nil {
		adapterType := requestedMailAdapterType(*req.Msg.Type)
		command.Type = &adapterType
	}
	adapter, err := h.delivery.UpdateMailAdapter(ctx, command)
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.UpdateMailAdapterResponse{Adapter: mailAdapterMessage(ctx, adapter)}), nil
}

func (h *MailAdapterConnectHandler) Delete(ctx context.Context, req *connect.Request[managev1.DeleteMailAdapterRequest]) (*connect.Response[managev1.DeleteMailAdapterResponse], error) {
	err := h.delivery.DeleteMailAdapter(ctx, DeleteMailAdapterCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteMailAdapterResponse{}, err)
}

func (h *MailAdapterConnectHandler) ListMailAdaptersAdmin(ctx context.Context, req *connect.Request[managev1.ListMailAdaptersAdminRequest]) (*connect.Response[managev1.ListMailAdaptersAdminResponse], error) {
	page, err := h.delivery.ListMailAdapters(ctx, ListMailAdaptersQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), ActiveOnly: activeOnly(req.Msg.Filters),
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	adapters := make([]*managev1.MailAdapter, len(page.Adapters))
	for i, adapter := range page.Adapters {
		adapters[i] = mailAdapterMessage(ctx, adapter)
	}
	return connect.NewResponse(&managev1.ListMailAdaptersAdminResponse{Adapters: adapters, Pagination: page.Page.BuildResponse(page.Total)}), nil
}

func (h *MailAdapterConnectHandler) TestConfig(ctx context.Context, req *connect.Request[managev1.TestMailAdapterConfigRequest]) (*connect.Response[managev1.TestMailAdapterResponse], error) {
	return respond(testSendMessage(h.delivery.TestMailAdapterConfig(ctx, TestMailAdapterConfigCommand{
		Type: requestedMailAdapterType(req.Msg.Type), TestEmail: req.Msg.TestEmail,
		SES: sesConfigInput(req.Msg.GetSesConfig()), SMTP: smtpConfigInput(req.Msg.GetSmtpConfig()),
	})))
}

func (h *MailAdapterConnectHandler) Test(ctx context.Context, req *connect.Request[managev1.TestMailAdapterRequest]) (*connect.Response[managev1.TestMailAdapterResponse], error) {
	return respond(testSendMessage(h.delivery.TestMailAdapter(ctx, TestMailAdapterCommand{ID: req.Msg.Id, TestEmail: req.Msg.TestEmail})))
}

// EmailSuppressionConnectHandler serves the Email Suppression Connect RPCs.
type EmailSuppressionConnectHandler struct {
	managev1connect.UnimplementedEmailSuppressionServiceHandler
	delivery *Service
}

// NewEmailSuppressionConnectHandler returns the Email Suppression Connect
// adapter of delivery.
func NewEmailSuppressionConnectHandler(delivery *Service) *EmailSuppressionConnectHandler {
	return &EmailSuppressionConnectHandler{delivery: delivery}
}

func (h *EmailSuppressionConnectHandler) GetEmailSuppression(ctx context.Context, req *connect.Request[managev1.GetEmailSuppressionRequest]) (*connect.Response[managev1.GetEmailSuppressionResponse], error) {
	suppression, err := h.delivery.GetEmailSuppression(ctx, GetEmailSuppressionQuery{Email: req.Msg.Email})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	response := &managev1.GetEmailSuppressionResponse{}
	if suppression != nil {
		response.Suppression = emailSuppressionMessage(*suppression)
	}
	return connect.NewResponse(response), nil
}

func (h *EmailSuppressionConnectHandler) ReleaseEmailSuppression(ctx context.Context, req *connect.Request[managev1.ReleaseEmailSuppressionRequest]) (*connect.Response[managev1.ReleaseEmailSuppressionResponse], error) {
	err := h.delivery.ReleaseEmailSuppression(ctx, ReleaseEmailSuppressionCommand{Email: req.Msg.Email})
	return respond(&managev1.ReleaseEmailSuppressionResponse{Success: true}, err)
}

func emailSuppressionMessage(s Suppression) *managev1.EmailSuppression {
	message := &managev1.EmailSuppression{
		Id:           s.ID,
		Email:        s.Email,
		Reason:       s.Reason,
		Source:       s.Source,
		SuppressedAt: timestamppb.New(s.SuppressedAt),
		CreatedAt:    timestamppb.New(s.CreatedAt),
		UpdatedAt:    timestamppb.New(s.UpdatedAt),
	}
	if s.ReferenceID != nil {
		message.ReferenceId = s.ReferenceID
	}
	if s.LastError != nil {
		message.LastError = s.LastError
	}
	if s.ReleasedAt != nil {
		message.ReleasedAt = timestamppb.New(*s.ReleasedAt)
	}
	if s.ReleasedBy != nil {
		message.ReleasedBy = s.ReleasedBy
	}
	return message
}

// CourierConnectHandler serves the internal Email Courier RPC that the
// Kratos HTTP courier calls.
type CourierConnectHandler struct {
	intrav1connect.UnimplementedEmailCourierServiceHandler
	courier *EmailCourierService
}

// NewCourierConnectHandler returns the Email Courier Connect adapter of
// courier.
func NewCourierConnectHandler(courier *EmailCourierService) *CourierConnectHandler {
	return &CourierConnectHandler{courier: courier}
}

func (h *CourierConnectHandler) SendEmail(ctx context.Context, req *connect.Request[intrav1.SendEmailRequest]) (*connect.Response[intrav1.SendEmailResponse], error) {
	queued, err := h.courier.QueueAuthEmail(ctx, QueueAuthEmailCommand{
		Recipient: req.Msg.Recipient, TemplateType: req.Msg.TemplateType, TemplateData: req.Msg.TemplateData,
	})
	return respond(&intrav1.SendEmailResponse{Queued: queued}, err)
}
