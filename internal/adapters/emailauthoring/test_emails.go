package emailauthoring

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/email"
	emailauthoringdomain "github.com/echovisionlab/geul-api/internal/emailauthoring"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// EmailPublisher queues an email for the email worker.
type EmailPublisher interface {
	PublishSendEmail(context.Context, *managev1.SendEmailEvent) error
}

// TestEmails queues an administrator's template test send as a direct
// template email to a test recipient.
type TestEmails struct {
	publisher EmailPublisher
}

var _ emailauthoringdomain.TestEmailQueue = TestEmails{}

// NewTestEmails returns the test send queue over publisher.
func NewTestEmails(publisher EmailPublisher) TestEmails {
	if publisher == nil {
		panic("Email Template test send publisher is required")
	}
	return TestEmails{publisher: publisher}
}

func (q TestEmails) QueueTestEmail(ctx context.Context, test emailauthoringdomain.TestEmail) error {
	templateID, messageID := test.TemplateID, test.MessageID
	return q.publisher.PublishSendEmail(ctx, &managev1.SendEmailEvent{
		Recipient:        test.Recipient,
		TemplateType:     email.DirectTemplateType(templateID),
		TemplateData:     test.Data,
		ReferenceId:      &templateID,
		MessageId:        &messageID,
		RecipientContext: &managev1.SendEmailEvent_TestEmail{TestEmail: &managev1.TestEmailRecipient{ActorMemberId: test.ActorMemberID}},
		Locale:           test.Locale,
	})
}
