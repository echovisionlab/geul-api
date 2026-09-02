package email

import (
	"context"
	"fmt"
	"strings"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// CommandPublisher is the transport capability required to enqueue one email
// delivery command. Domains retain their own consumer-facing dependency fields.
type CommandPublisher interface {
	PublishSendEmail(context.Context, *managev1.SendEmailEvent) error
}

// PublishCommand validates the command boundary, assigns its stable message ID,
// and delegates durable publication to the supplied transport.
func PublishCommand(
	ctx context.Context,
	publisher CommandPublisher,
	job *managev1.SendEmailEvent,
	messageID string,
) error {
	if publisher == nil {
		return fmt.Errorf("email command publisher is required")
	}
	if job == nil {
		return fmt.Errorf("email command is required")
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return fmt.Errorf("email message id is required")
	}
	job.MessageId = &messageID
	return publisher.PublishSendEmail(ctx, job)
}
