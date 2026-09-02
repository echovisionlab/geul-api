package emailauthoring

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/email"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// templateKeyRegex validates the format of template keys
func validateEmail(email string) error {
	email = strings.TrimSpace(email)
	if email == "" {
		return fmt.Errorf("email is required")
	}
	_, err := mail.ParseAddress(email)
	if err != nil {
		return fmt.Errorf("invalid email address: %s", email)
	}
	return nil
}

// SendTestEmail sends a test email (admin)
func (s *EmailTemplateService) SendTestEmail(
	ctx context.Context,
	req *connect.Request[managev1.SendTestEmailRequest],
) (*connect.Response[managev1.SuccessResponse], error) {
	can, canErr := policyv1.EmailTemplate.Manage(req.Msg.Id)
	if _, err := s.requireEmailTemplateCan(ctx, can, canErr); err != nil {
		return nil, err
	}

	// Validate email address
	if err := validateEmail(req.Msg.Email); err != nil {
		return nil, errs.InvalidArgument("email", err.Error())
	}

	// Verify template exists
	var template model.EmailTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ?", req.Msg.Id).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("email template", req.Msg.Id)
		}
		return nil, errs.Internal(err)
	}

	if s.publisher == nil {
		return nil, errs.Internal(fmt.Errorf("email publisher is not configured"))
	}

	sendJob, err := buildEmailTemplateSendTestJob(s.runtime, template.ID, req.Msg.Email, req.Msg.Locale, emailTestActorID(ctx))
	if err != nil {
		return nil, err
	}
	if err := s.publisher.PublishSendEmail(ctx, sendJob); err != nil {
		return nil, errs.Internal(fmt.Errorf("failed to queue test email: %w", err))
	}

	return connect.NewResponse(&managev1.SuccessResponse{Success: true}), nil
}

func buildEmailTemplateSendTestJob(
	runtime EmailTemplateRuntime,
	templateID string,
	recipient string,
	locale *string,
	actorID string,
) (*managev1.SendEmailEvent, error) {
	recipient = strings.TrimSpace(recipient)
	messageID := "template-test:" + uuid.NewString()
	sendJob := &managev1.SendEmailEvent{
		Recipient:    recipient,
		TemplateType: email.DirectTemplateType(templateID),
		TemplateData: map[string]string{
			"recipient_email": recipient,
		},
		ReferenceId:      &templateID,
		MessageId:        &messageID,
		RecipientContext: &managev1.SendEmailEvent_TestEmail{TestEmail: &managev1.TestEmailRecipient{ActorMemberId: strings.TrimSpace(actorID)}},
	}
	normalizedLocale, err := resolveEmailTemplateSendTestLocale(runtime, locale)
	if err != nil {
		return nil, err
	}
	sendJob.Locale = normalizedLocale
	return sendJob, nil
}

func resolveEmailTemplateSendTestLocale(runtime EmailTemplateRuntime, locale *string) (*string, error) {
	if locale == nil {
		return nil, nil
	}
	normalizedLocale := runtime.NormalizeSupportedLocale(strings.TrimSpace(*locale))
	if normalizedLocale == nil {
		return nil, errs.InvalidArgument("locale", "unsupported locale")
	}
	return normalizedLocale, nil
}

// PreviewEmailTemplate renders a template with sample data (admin)
