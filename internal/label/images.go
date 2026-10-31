package label

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentsignal"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// LogoVariant names a Label logo slot.
type LogoVariant uint8

const (
	// LogoVariantUnsupported is a variant the Label has no slot for.
	LogoVariantUnsupported LogoVariant = iota
	LogoVariantLight
	LogoVariantDark
)

// labelLogoSlot is the column, public asset binding and audit slot of a
// logo variant.
type labelLogoSlot struct {
	column     string
	bindingKey string
	audit      sharedtelemetry.AuditAssetSlot
}

func (slot labelLogoSlot) fileID(label *model.Label) *string {
	if slot.column == "logo_dark_file_id" {
		return label.LogoDarkFileID
	}
	return label.LogoLightFileID
}

func labelLogoSlotFor(variant LogoVariant) (labelLogoSlot, error) {
	switch variant {
	case LogoVariantLight:
		return labelLogoSlot{column: "logo_light_file_id", bindingKey: "logo_light", audit: sharedtelemetry.AuditAssetSlotLight}, nil
	case LogoVariantDark:
		return labelLogoSlot{column: "logo_dark_file_id", bindingKey: "logo_dark", audit: sharedtelemetry.AuditAssetSlotDark}, nil
	default:
		return labelLogoSlot{}, domainerr.InvalidField("variant", "unsupported theme asset variant")
	}
}

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

// SetLabelImageCommand selects FileID for a Label logo slot.
type SetLabelImageCommand struct {
	LabelID string
	FileID  string
	Variant LogoVariant
}

// LogoUpdate is the Label's ready logos after a logo change and the OG
// generation run it started.
type LogoUpdate struct {
	Light             *commonv1.AssetRef
	Dark              *commonv1.AssetRef
	OgGenerationRunID *string
}

// logoChange is a committed logo slot change. signalID is the Label ID its
// content.updated signal names.
type logoChange struct {
	changed  bool
	ogRunID  *string
	fileID   string
	signalID string
}

// SetLabelImage selects the file referenced by a label logo slot.
func (s *Service) SetLabelImage(ctx context.Context, command SetLabelImageCommand) (_ LogoUpdate, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	slot, err := labelLogoSlotFor(command.Variant)
	if err != nil {
		return LogoUpdate{}, err
	}
	var label model.Label
	change, err := s.changeLabelLogo(ctx, command.LabelID, &label, slot, sharedtelemetry.AuditCollectionOperationAdded,
		func(ctx context.Context, tx *gorm.DB) (logoChange, error) {
			previousEffectiveLogoFileID := effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID)
			if sameOptionalString(slot.fileID(&label), &command.FileID) {
				return logoChange{}, nil
			}
			if err := s.runtime.LockAttachableFilesForUpdate(ctx, tx, []string{command.FileID}); err != nil {
				return logoChange{}, err
			}
			result := tx.Model(&label).Updates(structured.Fields{slot.column: command.FileID, "updated_at": time.Now()})
			if result.Error != nil {
				return logoChange{}, result.Error
			}
			if result.RowsAffected == 0 {
				return logoChange{}, gorm.ErrRecordNotFound
			}
			if _, err := s.runtime.BindReadyAssetForSourceFile(ctx, tx, command.FileID, "label", command.LabelID, slot.bindingKey, "logo"); err != nil {
				return logoChange{}, err
			}
			ogRunID, err := refreshLabelOgForEffectiveLogoChangeWithDB(ctx, tx, s.runtime, command.LabelID, previousEffectiveLogoFileID, "label_logo_updated")
			return logoChange{changed: true, ogRunID: ogRunID, fileID: command.FileID, signalID: label.ID}, err
		})
	if err != nil {
		return LogoUpdate{}, err
	}
	imageAssets := s.getLabelImageAssets(ctx, label.ID)
	if err := s.overlayLabelSourceLocaleDocument(ctx, &label); err != nil {
		return LogoUpdate{}, err
	}
	return LogoUpdate{Light: imageAssets.Light, Dark: imageAssets.Dark, OgGenerationRunID: change.ogRunID}, nil
}

// DeleteLabelImageCommand clears a Label logo slot without deleting its file.
type DeleteLabelImageCommand struct {
	LabelID string
	Variant LogoVariant
}

// DeleteLabelImage clears a label logo slot and returns the OG generation
// run the change started.
func (s *Service) DeleteLabelImage(ctx context.Context, command DeleteLabelImageCommand) (_ *string, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("label_id", command.LabelID))

	slot, err := labelLogoSlotFor(command.Variant)
	if err != nil {
		return nil, err
	}
	var label model.Label
	change, err := s.changeLabelLogo(ctx, command.LabelID, &label, slot, sharedtelemetry.AuditCollectionOperationRemoved,
		func(ctx context.Context, tx *gorm.DB) (logoChange, error) {
			previousEffectiveLogoFileID := effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID)
			oldFileID := slot.fileID(&label)
			if oldFileID == nil {
				return logoChange{}, nil
			}
			if err := tx.Model(&label).Updates(structured.Fields{slot.column: nil, "updated_at": time.Now()}).Error; err != nil {
				return logoChange{}, err
			}
			if err := s.runtime.ReleasePublicAssetBindings(ctx, tx, "label", command.LabelID, slot.bindingKey); err != nil {
				return logoChange{}, err
			}
			ogRunID, err := refreshLabelOgForEffectiveLogoChangeWithDB(ctx, tx, s.runtime, command.LabelID, previousEffectiveLogoFileID, "label_logo_removed")
			return logoChange{changed: true, ogRunID: ogRunID, fileID: *oldFileID, signalID: command.LabelID}, err
		})
	if err != nil {
		return nil, err
	}
	return change.ogRunID, nil
}

// changeLabelLogo locks the Label row into label before the fresh Manage
// check and runs change; a changed slot is audited and signals a media
// update.
func (s *Service) changeLabelLogo(
	ctx context.Context,
	labelID string,
	label *model.Label,
	slot labelLogoSlot,
	operation sharedtelemetry.AuditCollectionOperation,
	change func(context.Context, *gorm.DB) (logoChange, error),
) (logoChange, error) {
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[logoChange]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(label, "id = ?", labelID).Error
		},
		Access: labelEntity.LockedAccess(s.spiceDB, labelID, policyv1.Label.Manage),
		Change: change,
		Audit: func(change logoChange) mutation.Audit {
			if !change.changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditLabelUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewLabelLogoAuditRecord(m, label.ID, slot.audit, operation, change.fileID)
			})
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, change logoChange) error {
			if !change.changed {
				return nil
			}
			return contentsignal.PublishInTransaction(ctx, tx, s.publisher, buildLabelMediaMutationContentUpdatedEvent(change.signalID))
		},
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return logoChange{}, domainerr.ResourceNotFound("label", labelID)
	}
	return result, err
}
