package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	filemediaapplication "github.com/echovisionlab/geul-api/internal/filemedia/application"
	"github.com/echovisionlab/geul-api/internal/mq"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (h *Handlers) handleCampaignEmailMessage(ctx context.Context, msg mq.Message) error {
	job, err := decodeCampaignEmailMessage(msg)
	if err != nil {
		return terminalQueueContractError("invalid_campaign_email_payload", err)
	}
	if err := requireStableDeliveryMessageID(msg, job.GetDeliveryRunId()); err != nil {
		return err
	}
	return h.handleSendBulkEmailBatch(ctx, job)
}

func decodeCampaignEmailMessage(msg mq.Message) (*managev1.SendBulkEmailBatchEvent, error) {
	var job managev1.SendBulkEmailBatchEvent
	if err := proto.Unmarshal(msg.Body, &job); err != nil {
		return nil, fmt.Errorf("invalid campaign email job: %w", err)
	}
	if strings.TrimSpace(job.GetDeliveryRunId()) == "" {
		return nil, fmt.Errorf("invalid campaign email job: delivery_run_id is required")
	}
	return &job, nil
}

var (
	errInvalidFileDeletePayload = errors.New("invalid file delete payload")
)

func parseFileDeleteEvent(msg mq.Message) (*managev1.FileDeleteEvent, error) {
	var protoEvent managev1.FileDeleteEvent
	if err := proto.Unmarshal(msg.Body, &protoEvent); err == nil {
		if err := filemediaapplication.ValidateDeleteEvent(&protoEvent); err == nil {
			return &protoEvent, nil
		}
	}

	bodyText := strings.TrimSpace(string(msg.Body))
	if bodyText == "" || (!strings.HasPrefix(bodyText, "{") && !strings.HasPrefix(bodyText, "[")) {
		return nil, fmt.Errorf("%w: unsupported encoding", errInvalidFileDeletePayload)
	}

	// Handles protobuf JSON payloads.
	var jsonEvent managev1.FileDeleteEvent
	if err := protojson.Unmarshal(msg.Body, &jsonEvent); err == nil {
		if err := filemediaapplication.ValidateDeleteEvent(&jsonEvent); err == nil {
			return &jsonEvent, nil
		}
	}

	return nil, fmt.Errorf("%w: missing required fields", errInvalidFileDeletePayload)
}

// handleFileDeleteMessage handles messages from file.delete queue
func (h *Handlers) handleFileDeleteMessage(ctx context.Context, msg mq.Message) error {
	event, err := parseFileDeleteEvent(msg)
	if err != nil {
		return terminalQueueContractError(
			"invalid_file_delete_payload",
			fmt.Errorf("invalid file delete event: %w", err),
		)
	}
	if err := requireStableDeliveryMessageID(msg, event.GetFileId()); err != nil {
		return err
	}
	if err := h.HandleFileDelete(ctx, event); err != nil {
		if errors.Is(err, filemediaapplication.ErrInvalidFileDeleteTarget) {
			return terminalQueueContractError("invalid_file_delete_target", err)
		}
		return err
	}
	return nil
}
