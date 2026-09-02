package worker

import (
	"context"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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

func (h *Handlers) handleAIMetadataGenerateMessage(ctx context.Context, msg mq.Message) error {
	var event managev1.MetadataGenerationQueueEvent
	if err := proto.Unmarshal(msg.Body, &event); err != nil {
		return terminalQueueContractError(
			"invalid_metadata_ai_payload",
			fmt.Errorf("invalid metadata AI queue payload: %w", err),
		)
	}
	if event.JobId == "" {
		return terminalQueueContractError(
			"invalid_metadata_ai_payload",
			fmt.Errorf("invalid metadata AI queue payload: missing job_id"),
		)
	}
	if err := requireStableDeliveryMessageID(msg, event.GetJobId()); err != nil {
		return err
	}
	if h.metadataAI == nil {
		return fmt.Errorf("metadata AI job processor is not configured")
	}
	return h.metadataAI.ProcessJob(ctx, event.JobId)
}

func translationGenerateQueueConfig() mq.QueueConfig {
	return mq.QueueConfig{
		Name:         eventpkg.QueueTranslationGenerate,
		MessageType:  "api.manage.v1.TranslationGenerateEvent",
		Workers:      1,
		MaxRetries:   translationMaxRetries,
		RetryDelay:   translationRetryDelay,
		RetryBackoff: translationRetryBackoff,
	}
}

func (h *Handlers) handleTranslationGenerateMessage(ctx context.Context, msg mq.Message) error {
	var event managev1.TranslationGenerateEvent
	if err := proto.Unmarshal(msg.Body, &event); err != nil {
		return mq.NewTerminalDeliveryError(
			"translation_invalid_payload",
			fmt.Errorf("invalid translation generate payload: %w", err),
		)
	}
	if event.JobId == "" {
		return mq.NewTerminalDeliveryError(
			"translation_missing_job_id",
			fmt.Errorf("invalid translation generate payload: missing job_id"),
		)
	}
	if err := requireStableDeliveryMessageID(msg, event.GetJobId()); err != nil {
		return err
	}
	if h.translationJobs == nil {
		return mq.NewTerminalDeliveryError(
			"translation_processor_unconfigured",
			fmt.Errorf("translation job processor is not configured"),
		)
	}
	return h.translationJobs.ProcessDelivery(ctx, event.JobId)
}
