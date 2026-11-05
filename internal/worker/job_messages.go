package worker

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) handleEmailSendMessage(ctx context.Context, msg mq.Message) error {
	var event managev1.SendEmailEvent
	if err := proto.Unmarshal(msg.Body, &event); err != nil {
		return terminalQueueContractError(
			"invalid_email_send_payload",
			fmt.Errorf("invalid email send event: %w", err),
		)
	}
	if err := requireStableDeliveryMessageID(msg, event.GetMessageId()); err != nil {
		return err
	}
	return h.HandleEmailSend(ctx, &event)
}
