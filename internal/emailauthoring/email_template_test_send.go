package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
	emailpostgres "github.com/echovisionlab/geul-api/internal/email/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// validateEmail validates an email address using net/mail.
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

// SendTestEmailCommand sends an Email Template to one address with sample
// data.
type SendTestEmailCommand struct {
	ID     string
	Email  string
	Locale *string
}

// SendTestEmail queues a test send of the template to an address that is
// ready to receive one.
func (s *Service) SendTestEmail(ctx context.Context, command SendTestEmailCommand) error {
	can, canErr := policyv1.EmailTemplate.Manage(command.ID)
	if err := s.requireAdminCan(ctx, can, canErr); err != nil {
		return err
	}
	if err := validateEmail(command.Email); err != nil {
		return domainerr.InvalidField("email", err.Error())
	}
	var template model.EmailTemplate
	if err := s.db.WithContext(ctx).First(&template, "id = ?", command.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("email template", command.ID)
		}
		return err
	}
	if err := email.CheckTestSendReadiness(ctx, emailpostgres.Bind(s.db), command.Email); err != nil {
		if message := email.TestSendReadinessMessage(err); message != "" {
			return domainerr.Precondition(message)
		}
		return err
	}
	sendJob, err := buildEmailTemplateSendTestJob(s.runtime, template.ID, command.Email, command.Locale, emailTestActorID(ctx))
	if err != nil {
		return err
	}
	if err := s.publisher.PublishSendEmail(ctx, sendJob); err != nil {
		return fmt.Errorf("failed to queue test email: %w", err)
	}
	return nil
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
	sampleData := emailTemplateSampleData()
	for _, key := range emailTemplateEmailAliasVariables {
		sampleData[key] = recipient
	}
	sendJob := &managev1.SendEmailEvent{
		Recipient:        recipient,
		TemplateType:     email.DirectTemplateType(templateID),
		TemplateData:     sampleData,
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
		return nil, domainerr.InvalidField("locale", "unsupported locale")
	}
	return normalizedLocale, nil
}
