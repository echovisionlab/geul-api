package label

import (
	"context"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func effectiveLabelLogoFileID(lightFileID *string, darkFileID *string) *string {
	if light := normalizedOptionalString(lightFileID); light != nil {
		return light
	}
	return normalizedOptionalString(darkFileID)
}

func sameNormalizedOptionalString(left *string, right *string) bool {
	left = normalizedOptionalString(left)
	right = normalizedOptionalString(right)
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func refreshLabelOgForEffectiveLogoChangeWithDB(
	ctx context.Context,
	tx *gorm.DB,
	runtime Runtime,
	labelID string,
	previousEffectiveLogoFileID *string,
	reason string,
) (*string, error) {
	var row struct {
		FileID *string `gorm:"column:file_id"`
	}
	if err := tx.WithContext(ctx).
		Raw(`SELECT COALESCE(logo_light_file_id, logo_dark_file_id) AS file_id FROM label WHERE id = ? LIMIT 1`, labelID).
		Scan(&row).Error; err != nil {
		return nil, err
	}
	currentEffectiveLogoFileID := normalizedOptionalString(row.FileID)
	if sameNormalizedOptionalString(previousEffectiveLogoFileID, currentEffectiveLogoFileID) {
		return nil, nil
	}
	if currentEffectiveLogoFileID == nil {
		if err := tx.WithContext(ctx).Table("label").
			Where("id = ?", labelID).Update("og_asset_id", nil).Error; err != nil {
			return nil, err
		}
		if err := runtime.CancelAndReleaseOGWithDB(ctx, tx, labelID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	runID, err := runtime.RequestCurrentWithDB(ctx, tx, labelID, reason)
	if err != nil || strings.TrimSpace(runID) == "" {
		return nil, err
	}
	return &runID, nil
}

func labelLogoColumnForVariant(variant managev1.ThemeAssetVariant) (string, error) {
	switch variant {
	case managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_UNSPECIFIED,
		managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT:
		return "logo_light_file_id", nil
	case managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK:
		return "logo_dark_file_id", nil
	default:
		return "", errs.InvalidArgument("variant", "unsupported theme asset variant")
	}
}

// SetLabelImage selects the file referenced by a label logo slot.
func (s *LabelService) SetLabelImage(
	ctx context.Context,
	req *connect.Request[managev1.SetLabelImageRequest],
) (*connect.Response[managev1.SetLabelImageResponse], error) {
	logoColumn, err := labelLogoColumnForVariant(req.Msg.Variant)
	if err != nil {
		return nil, err
	}

	var label model.Label
	var ogRunID *string
	changed := false

	// Use transaction to ensure atomicity
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Get the label to update.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&label, "id = ?", req.Msg.LabelId).Error; err != nil {
			return err
		}
		if err := requireLockedLabelPermission(ctx, tx, s.spiceDB, req.Msg.LabelId, policyv1.Label.Manage); err != nil {
			return err
		}
		previousEffectiveLogoFileID := effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID)
		currentSlotFileID := label.LogoLightFileID
		if logoColumn == "logo_dark_file_id" {
			currentSlotFileID = label.LogoDarkFileID
		}
		if sameOptionalString(currentSlotFileID, &req.Msg.FileId) {
			return nil
		}
		if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}

		// 2. Update label logo
		result := tx.Model(&label).Updates(structured.Fields{
			logoColumn:   req.Msg.FileId,
			"updated_at": time.Now(),
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		changed = true

		bindingKey := "logo:light"
		if logoColumn == "logo_dark_file_id" {
			bindingKey = "logo:dark"
		}
		_, err = s.runtime.BindReadyAssetForSourceFile(
			ctx, tx, req.Msg.FileId, "label", req.Msg.LabelId, bindingKey, "logo",
		)
		if err != nil {
			return err
		}
		ogRunID, err = refreshLabelOgForEffectiveLogoChangeWithDB(
			ctx, tx, s.runtime, req.Msg.LabelId,
			previousEffectiveLogoFileID, "label_logo_updated",
		)
		if err != nil {
			return err
		}
		return s.appendLabelLogoAudit(ctx, tx, label.ID, labelAuditLogoSlot(logoColumn), sharedtelemetry.AuditCollectionOperationAdded, req.Msg.FileId)
	})

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("label", req.Msg.LabelId)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	imageAssets := s.getLabelImageAssets(ctx, label.ID)

	if err := s.overlayLabelSourceLocaleDocument(ctx, &label); err != nil {
		return nil, err
	}
	if changed {
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelMediaMutationContentUpdatedEvent(label.ID))
	}

	return connect.NewResponse(&managev1.SetLabelImageResponse{
		ImageLightAsset:   imageAssets.Light,
		ImageDarkAsset:    imageAssets.Dark,
		OgGenerationRunId: ogRunID,
	}), nil
}

// DeleteLabelImage clears a label logo slot without deleting its file.
func (s *LabelService) DeleteLabelImage(
	ctx context.Context,
	req *connect.Request[managev1.DeleteLabelImageRequest],
) (*connect.Response[managev1.OgAssetDeleteResponse], error) {
	logoColumn, err := labelLogoColumnForVariant(req.Msg.Variant)
	if err != nil {
		return nil, err
	}

	var ogRunID *string
	changed := false

	// Use transaction to ensure atomicity
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Get the label to update.
		var label model.Label
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&label, "id = ?", req.Msg.LabelId).Error; err != nil {
			return err
		}
		if err := requireLockedLabelPermission(ctx, tx, s.spiceDB, req.Msg.LabelId, policyv1.Label.Manage); err != nil {
			return err
		}
		previousEffectiveLogoFileID := effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID)
		oldFileID := label.LogoLightFileID
		if logoColumn == "logo_dark_file_id" {
			oldFileID = label.LogoDarkFileID
		}
		if oldFileID == nil {
			return nil
		}
		changed = true

		// 2. Clear the selected logo slot.
		if err := tx.Model(&label).Updates(structured.Fields{
			logoColumn:   nil,
			"updated_at": time.Now(),
		}).Error; err != nil {
			return err
		}

		bindingKey := "logo:light"
		if logoColumn == "logo_dark_file_id" {
			bindingKey = "logo:dark"
		}
		if err := s.runtime.ReleasePublicAssetBindings(
			ctx, tx, "label", req.Msg.LabelId, bindingKey,
		); err != nil {
			return err
		}
		ogRunID, err = refreshLabelOgForEffectiveLogoChangeWithDB(
			ctx, tx, s.runtime, req.Msg.LabelId,
			previousEffectiveLogoFileID, "label_logo_removed",
		)
		if err != nil {
			return err
		}
		return s.appendLabelLogoAudit(ctx, tx, label.ID, labelAuditLogoSlot(logoColumn), sharedtelemetry.AuditCollectionOperationRemoved, *oldFileID)
	})

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("label", req.Msg.LabelId)
		}
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if changed {
		publishLabelContentUpdated(ctx, s.asyncPublisher, buildLabelMediaMutationContentUpdatedEvent(req.Msg.LabelId))
	}

	return connect.NewResponse(&managev1.OgAssetDeleteResponse{
		Success: true, OgGenerationRunId: ogRunID,
	}), nil
}

func labelAuditLogoSlot(column string) sharedtelemetry.AuditAssetSlot {
	if column == "logo_dark_file_id" {
		return sharedtelemetry.AuditAssetSlotDark
	}
	return sharedtelemetry.AuditAssetSlotLight
}

// ==================== Owner and Manager Assignment ====================
