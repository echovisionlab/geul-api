package emaildelivery

import (
	"context"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
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

// requestedMailAdapterType maps the API type of a request. A type the domain
// does not store is the empty type, which the use case rejects.
func requestedMailAdapterType(adapterType managev1.MailAdapterType) model.MailAdapterType {
	stored, _ := mailAdapterTypeFromProto(adapterType)
	return stored
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
	return respond(&managev1.CreateMailAdapterResponse{Adapter: adapter}, err)
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
	return respond(&managev1.UpdateMailAdapterResponse{Adapter: adapter}, err)
}

func (h *MailAdapterConnectHandler) Delete(ctx context.Context, req *connect.Request[managev1.DeleteMailAdapterRequest]) (*connect.Response[managev1.DeleteMailAdapterResponse], error) {
	err := h.delivery.DeleteMailAdapter(ctx, DeleteMailAdapterCommand{ID: req.Msg.Id})
	return respond(&managev1.DeleteMailAdapterResponse{}, err)
}

func (h *MailAdapterConnectHandler) ListMailAdaptersAdmin(ctx context.Context, req *connect.Request[managev1.ListMailAdaptersAdminRequest]) (*connect.Response[managev1.ListMailAdaptersAdminResponse], error) {
	page, err := h.delivery.ListMailAdapters(ctx, ListMailAdaptersQuery{
		Limit: req.Msg.GetPagination().GetLimit(), Offset: req.Msg.GetPagination().GetOffset(), Filters: req.Msg.Filters,
	})
	return respond(&managev1.ListMailAdaptersAdminResponse{Adapters: page.Adapters, Pagination: page.Pagination}, err)
}

func (h *MailAdapterConnectHandler) TestConfig(ctx context.Context, req *connect.Request[managev1.TestMailAdapterConfigRequest]) (*connect.Response[managev1.TestMailAdapterResponse], error) {
	return respond(h.delivery.TestMailAdapterConfig(ctx, TestMailAdapterConfigCommand{
		Type: requestedMailAdapterType(req.Msg.Type), TestEmail: req.Msg.TestEmail,
		SES: sesConfigInput(req.Msg.GetSesConfig()), SMTP: smtpConfigInput(req.Msg.GetSmtpConfig()),
	}))
}

func (h *MailAdapterConnectHandler) Test(ctx context.Context, req *connect.Request[managev1.TestMailAdapterRequest]) (*connect.Response[managev1.TestMailAdapterResponse], error) {
	return respond(h.delivery.TestMailAdapter(ctx, TestMailAdapterCommand{ID: req.Msg.Id, TestEmail: req.Msg.TestEmail}))
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
	return respond(&managev1.GetEmailSuppressionResponse{Suppression: suppression}, err)
}

func (h *EmailSuppressionConnectHandler) ReleaseEmailSuppression(ctx context.Context, req *connect.Request[managev1.ReleaseEmailSuppressionRequest]) (*connect.Response[managev1.ReleaseEmailSuppressionResponse], error) {
	err := h.delivery.ReleaseEmailSuppression(ctx, ReleaseEmailSuppressionCommand{Email: req.Msg.Email})
	return respond(&managev1.ReleaseEmailSuppressionResponse{Success: true}, err)
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
