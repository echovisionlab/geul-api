package mq

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"

	apiv1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	"google.golang.org/protobuf/proto"
)

type deliverySettlementContextKey struct{}

type deliverySettlement struct {
	db          *sql.DB
	client      eventpkg.PGMQ
	queue       string
	transportID int64
	messageID   string
	scope       *transcodeDeliveryIdentity

	mu      sync.Mutex
	active  bool
	settled bool
}

func newDeliverySettlement(
	db *sql.DB,
	client eventpkg.PGMQ,
	queue string,
	message eventpkg.Message,
	body []byte,
) *deliverySettlement {
	return &deliverySettlement{
		db:          db,
		client:      client,
		queue:       queue,
		transportID: message.TransportID,
		messageID:   message.Envelope.MessageID,
		scope:       transcodeDeliveryIdentityFromMessage(queue, message.Envelope.MessageType, body),
		active:      true,
	}
}

func withDeliverySettlement(ctx context.Context, settlement *deliverySettlement) context.Context {
	return context.WithValue(ctx, deliverySettlementContextKey{}, settlement)
}

func deliverySettlementFromContext(ctx context.Context) (*deliverySettlement, bool) {
	settlement, ok := ctx.Value(deliverySettlementContextKey{}).(*deliverySettlement)
	return settlement, ok && settlement != nil
}

func (d *deliverySettlement) enqueueResultAndComplete(
	ctx context.Context,
	publisherDB *sql.DB,
	complete *apiv1.TranscodeCompleteEvent,
	enqueue func(context.Context, eventpkg.DBTX) error,
) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.active {
		return errors.New("transcode delivery settlement is no longer active")
	}
	if d.settled {
		return errors.New("transcode delivery is already settled")
	}
	if publisherDB == nil || publisherDB != d.db {
		return errors.New("transcode result publisher must use the current delivery database")
	}
	if !d.scope.matches(complete, d.messageID) {
		return errors.New("transcode result does not match current audio/video delivery identity")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transcode result settlement: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := enqueue(ctx, tx); err != nil {
		return fmt.Errorf("enqueue transcode result: %w", err)
	}
	if err := d.client.Complete(ctx, tx, d.queue, d.transportID); err != nil {
		return fmt.Errorf("complete transcode input: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transcode result settlement: %w", err)
	}
	committed = true
	d.settled = true
	return nil
}

func (d *deliverySettlement) envelopeMatchesCommand() bool {
	return d.scope == nil || (d.scope.eventID != "" && d.scope.eventID == d.messageID)
}

type transcodeDeliveryIdentity struct {
	eventID    string
	eventType  apiv1.TranscodeEventType
	entityType apiv1.TranscodeEntityType
	entityID   string
	fileID     string
}

func transcodeDeliveryIdentityFromMessage(
	queue, messageType string,
	body []byte,
) *transcodeDeliveryIdentity {
	switch {
	case queue == eventpkg.QueueTranscoderAudio && messageType == "api.manage.v1.TranscodeAudioEvent":
		var command apiv1.TranscodeAudioEvent
		if err := proto.Unmarshal(body, &command); err != nil {
			return nil
		}
		return &transcodeDeliveryIdentity{
			eventID: command.GetEventId(), eventType: apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_AUDIO,
			entityType: command.GetEntityType(), entityID: command.GetEntityId(), fileID: command.GetFileId(),
		}
	case queue == eventpkg.QueueTranscoderVideo && messageType == "api.manage.v1.TranscodeVideoEvent":
		var command apiv1.TranscodeVideoEvent
		if err := proto.Unmarshal(body, &command); err != nil {
			return nil
		}
		return &transcodeDeliveryIdentity{
			eventID: command.GetEventId(), eventType: apiv1.TranscodeEventType_TRANSCODE_EVENT_TYPE_VIDEO,
			entityType: command.GetEntityType(), entityID: command.GetEntityId(), fileID: command.GetFileId(),
		}
	default:
		return nil
	}
}

func (i *transcodeDeliveryIdentity) matches(complete *apiv1.TranscodeCompleteEvent, envelopeMessageID string) bool {
	return i != nil && complete != nil && i.eventID != "" && i.eventID == envelopeMessageID &&
		complete.GetEventId() == i.eventID && complete.GetEventType() == i.eventType &&
		complete.GetEntityType() == i.entityType && complete.GetEntityId() == i.entityID &&
		complete.GetFileId() == i.fileID
}

// finish prevents delayed code from using this delivery's settlement capability
// after its handler has returned. It reports whether result and input committed.
func (d *deliverySettlement) finish() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active = false
	return d.settled
}
