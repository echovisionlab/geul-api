package ogpostgres

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	ogLifecycleNotifyChannel   = "og_lifecycle"
	postgresNotifyPayloadLimit = 8_000
	notifySavepoint            = "og_lifecycle_notify"
)

func (s *store) NotifyLifecycle(ctx context.Context, event og.LifecycleEvent) {
	generation := event.Generation
	payload, err := lifecycleEventPayload(event, event.Asset)
	if err != nil {
		slog.ErrorContext(ctx, "Failed to encode OG lifecycle notification", failurecause.Attr(err), "generation_id", generation.ID, "run_id", generation.RunID, "status", string(event.Status))
		return
	}
	if s.tx.Dialector.Name() != "postgres" {
		return
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	if len(encoded) >= postgresNotifyPayloadLimit {
		payload, err = lifecycleEventPayload(event, nil)
		if err != nil {
			slog.ErrorContext(ctx, "Failed to encode compact OG lifecycle notification", failurecause.Attr(err), "generation_id", generation.ID, "run_id", generation.RunID, "status", string(event.Status))
			return
		}
		encoded = base64.StdEncoding.EncodeToString(payload)
	}
	if len(encoded) >= postgresNotifyPayloadLimit {
		slog.ErrorContext(ctx, "OG lifecycle notification exceeds PostgreSQL limit", "generation_id", generation.ID, "run_id", generation.RunID, "status", string(event.Status), "encoded_bytes", len(encoded))
		return
	}
	publishPostgresNotificationBestEffort(ctx, s.tx, ogLifecycleNotifyChannel, encoded)
}

// publishPostgresNotificationBestEffort notifies inside a savepoint, so a
// failed notification leaves the generation's transaction usable.
func publishPostgresNotificationBestEffort(ctx context.Context, tx *gorm.DB, channel string, payload string) {
	db := tx.WithContext(ctx)
	err := db.SavePoint(notifySavepoint).Error
	if err == nil {
		if err = db.Exec("SELECT pg_notify(?, ?)", channel, payload).Error; err != nil {
			err = errors.Join(err, db.RollbackTo(notifySavepoint).Error)
		}
	}
	if err != nil {
		slog.ErrorContext(ctx, "Failed to publish OG notification", failurecause.Attr(err, payload), "channel", channel)
	}
}

func lifecycleEventPayload(event og.LifecycleEvent, asset *og.AssetRef) ([]byte, error) {
	target, err := og.TargetToProto(event.Target)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(&managev1.OgGenerationLifecycleEvent{
		GenerationId:            event.Generation.ID,
		RunId:                   event.Generation.RunID,
		Target:                  target,
		Status:                  og.StatusToProto(event.Status),
		Asset:                   og.AssetRefToProto(asset),
		ErrorCode:               optionalString(event.Generation.LastErrorCode),
		OccurredAt:              timestamppb.New(event.OccurredAt),
		ReplacementGenerationId: optionalString(event.ReplacementID),
	})
}

func optionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
