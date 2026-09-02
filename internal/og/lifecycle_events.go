package og

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type ogAssetRefSnapshot struct {
	AssetID   string `json:"asset_id"`
	URL       string `json:"url"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogOutputSnapshot struct {
	AssetID   string `json:"asset_id"`
	ObjectKey string `json:"object_key"`
	Extension string `json:"extension"`
	MimeType  string `json:"mime_type"`
}

type ogEntitySnapshot struct {
	EntityType    string              `json:"entity_type"`
	EntityID      string              `json:"entity_id"`
	Title         string              `json:"title"`
	Locale        *string             `json:"locale,omitempty"`
	FeaturedImage *ogAssetRefSnapshot `json:"featured_image,omitempty"`
	Output        ogOutputSnapshot    `json:"output"`
}

type ogRenderConfigSnapshot struct {
	SiteTitle     string              `json:"site_title"`
	PrimaryColor  string              `json:"primary_color"`
	LogoAsset     *ogAssetRefSnapshot `json:"logo_asset,omitempty"`
	OGImageConfig json.RawMessage     `json:"og_image_config,omitempty"`
}

func supersedeActiveOgGeneration(
	tx *gorm.DB,
	targetID string,
	generationID string,
	replacementID string,
	now time.Time,
) error {
	result := tx.Model(&model.OgGeneration{}).
		Where("id = ? AND target_id = ? AND status IN ?", generationID, targetID, []string{
			model.OgGenerationStatusQueued,
		}).
		Updates(structured.Fields{
			"status":           model.OgGenerationStatusSuperseded,
			"superseded_at":    now,
			"superseded_by_id": replacementID,
			"completed_at":     now,
			"updated_at":       now,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		result = tx.Model(&model.OgGeneration{}).
			Where("id = ? AND target_id = ? AND status = ?", generationID, targetID, model.OgGenerationStatusProcessing).
			Updates(structured.Fields{
				"status":           model.OgGenerationStatusSuperseded,
				"superseded_at":    now,
				"superseded_by_id": replacementID,
				"completed_at":     now,
				"updated_at":       now,
			})
		if result.Error != nil {
			return result.Error
		}
	}
	if result.RowsAffected == 0 {
		return nil
	}
	var generation model.OgGeneration
	if err := tx.First(&generation, "id = ?", generationID).Error; err != nil {
		return err
	}
	var target model.OgGenerationTarget
	if err := tx.First(&target, "id = ?", targetID).Error; err != nil {
		return err
	}
	notifyOgLifecycle(tx, &generation, &target, model.OgGenerationStatusSuperseded, &replacementID, nil, now)
	return refreshOgRunStatus(tx, generation.RunID, now)
}

func snapshotAssetRef(asset *commonv1.AssetRef) *ogAssetRefSnapshot {
	if asset == nil {
		return nil
	}
	return &ogAssetRefSnapshot{
		AssetID:   asset.GetAssetId(),
		URL:       asset.GetUrl(),
		Extension: asset.GetExtension(),
		MimeType:  asset.GetMimeType(),
	}
}

func notifyOgLifecycle(
	tx *gorm.DB,
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	status string,
	replacementID *string,
	asset *commonv1.AssetRef,
	now time.Time,
) {
	payload, err := newOgLifecycleEventPayload(generation, target, status, replacementID, asset, now)
	if err != nil {
		return
	}
	if tx.Dialector.Name() != "postgres" {
		return
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	if len(encoded) >= postgresNotifyPayloadLimit {
		payload, err = newOgLifecycleEventPayload(generation, target, status, replacementID, nil, now)
		if err != nil {
			return
		}
		encoded = base64.StdEncoding.EncodeToString(payload)
	}
	if len(encoded) >= postgresNotifyPayloadLimit {
		return
	}
	publishPostgresNotificationBestEffort(tx, ogLifecycleNotifyChannel, encoded)
}

func publishPostgresNotificationBestEffort(tx *gorm.DB, channel string, payload string) {
	_ = tx.Transaction(func(notifyTx *gorm.DB) error {
		return notifyTx.Exec("SELECT pg_notify(?, ?)", channel, payload).Error
	})
}

func newOgLifecycleEventPayload(
	generation *model.OgGeneration,
	target *model.OgGenerationTarget,
	status string,
	replacementID *string,
	asset *commonv1.AssetRef,
	now time.Time,
) ([]byte, error) {
	protoTarget, err := TargetToProto(target)
	if err != nil {
		return nil, err
	}
	payload, err := proto.Marshal(&managev1.OgGenerationLifecycleEvent{
		GenerationId:            generation.ID,
		RunId:                   generation.RunID,
		Target:                  protoTarget,
		Status:                  StatusToProto(status),
		Asset:                   asset,
		ErrorCode:               optionalString(generation.LastErrorCode),
		OccurredAt:              timestamppb.New(now),
		ReplacementGenerationId: optionalString(replacementID),
	})
	return payload, err
}

func TargetToProto(target *model.OgGenerationTarget) (*managev1.OgGenerationTarget, error) {
	if target == nil {
		return nil, fmt.Errorf("OG generation target is required")
	}
	entityType := EntityTypeForName(target.EntityType)
	if entityType == managev1.OgEntityType_OG_ENTITY_TYPE_UNSPECIFIED {
		return nil, fmt.Errorf("unsupported OG generation target entity type %q", target.EntityType)
	}
	result := &managev1.OgGenerationTarget{EntityType: entityType, EntityId: target.EntityID}
	switch target.TargetKind {
	case "entity":
		result.Scope = &managev1.OgGenerationTarget_Entity{Entity: &managev1.OgEntityTarget{}}
	case "locale":
		if target.Locale == nil || strings.TrimSpace(*target.Locale) == "" {
			return nil, fmt.Errorf("locale OG target is missing locale")
		}
		result.Scope = &managev1.OgGenerationTarget_Locale{
			Locale: &managev1.OgLocaleTarget{Locale: strings.TrimSpace(*target.Locale)},
		}
	default:
		return nil, fmt.Errorf("unsupported OG target kind %q", target.TargetKind)
	}
	return result, nil
}

func StatusToProto(status string) managev1.OgGenerationStatus {
	switch status {
	case model.OgGenerationStatusQueued:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_QUEUED
	case model.OgGenerationStatusProcessing:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_PROCESSING
	case model.OgGenerationStatusReady:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_READY
	case model.OgGenerationStatusFailed:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_FAILED
	case model.OgGenerationStatusSuperseded:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_SUPERSEDED
	case model.OgGenerationStatusCancelled:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_CANCELLED
	default:
		return managev1.OgGenerationStatus_OG_GENERATION_STATUS_UNSPECIFIED
	}
}
