package emailauthoring

import (
	"context"
	"fmt"
	"net/mail"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/email"
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
	var template Template
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		template, err = findTemplate(ctx, store, command.ID)
		return err
	}); err != nil {
		return err
	}
	if err := s.units.Read(ctx, func(store Store) error {
		return store.CheckTestSendReadiness(ctx, command.Email)
	}); err != nil {
		if message := email.TestSendReadinessMessage(err); message != "" {
			return domainerr.Precondition(message)
		}
		return err
	}
	testEmail, err := newEmailTemplateTestEmail(s.locales, template.ID, command.Email, command.Locale, emailTestActorID(ctx))
	if err != nil {
		return err
	}
	if err := s.testEmails.QueueTestEmail(ctx, testEmail); err != nil {
		return fmt.Errorf("failed to queue test email: %w", err)
	}
	return nil
}

// newEmailTemplateTestEmail is the test send of templateID to recipient,
// with the preview samples and the recipient as every email alias.
func newEmailTemplateTestEmail(
	locales LocaleNormalizer,
	templateID string,
	recipient string,
	locale *string,
	actorID string,
) (TestEmail, error) {
	recipient = strings.TrimSpace(recipient)
	sampleData := emailTemplateSampleData()
	for _, key := range emailTemplateEmailAliasVariables {
		sampleData[key] = recipient
	}
	normalizedLocale, err := resolveEmailTemplateSendTestLocale(locales, locale)
	if err != nil {
		return TestEmail{}, err
	}
	return TestEmail{
		Recipient: recipient, TemplateID: templateID, Data: sampleData,
		MessageID: "template-test:" + uuid.NewString(), ActorMemberID: strings.TrimSpace(actorID),
		Locale: normalizedLocale,
	}, nil
}

func resolveEmailTemplateSendTestLocale(locales LocaleNormalizer, locale *string) (*string, error) {
	if locale == nil {
		return nil, nil
	}
	normalizedLocale := locales.NormalizeSupportedLocale(strings.TrimSpace(*locale))
	if normalizedLocale == nil {
		return nil, domainerr.InvalidField("locale", "unsupported locale")
	}
	return normalizedLocale, nil
}

func emailTestActorID(ctx context.Context) string {
	if user := auth.GetUser(ctx); user != nil && strings.TrimSpace(user.MemberID.String()) != "" {
		return user.MemberID.String()
	}
	return "system"
}
