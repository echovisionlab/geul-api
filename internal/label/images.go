package label

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

// labelLogoSlot is the variant and audit slot of a logo.
type labelLogoSlot struct {
	variant LogoVariant
	audit   sharedtelemetry.AuditAssetSlot
}

func (slot labelLogoSlot) fileID(label *Label) *string {
	if slot.variant == LogoVariantDark {
		return label.LogoDarkFileID
	}
	return label.LogoLightFileID
}

func labelLogoSlotFor(variant LogoVariant) (labelLogoSlot, error) {
	switch variant {
	case LogoVariantLight:
		return labelLogoSlot{variant: variant, audit: sharedtelemetry.AuditAssetSlotLight}, nil
	case LogoVariantDark:
		return labelLogoSlot{variant: variant, audit: sharedtelemetry.AuditAssetSlotDark}, nil
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

func normalizedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameNormalizedOptionalString(left *string, right *string) bool {
	return sameOptionalString(normalizedOptionalString(left), normalizedOptionalString(right))
}

// refreshLabelOgForEffectiveLogoChange regenerates the OG image when the
// effective logo changed: a Label left without a logo loses its OG image,
// otherwise a new generation run starts.
func refreshLabelOgForEffectiveLogoChange(ctx context.Context, store Store, labelID string, previousEffectiveLogoFileID *string, reason string) (*string, error) {
	current, err := store.EffectiveLogoFileID(ctx, labelID)
	if err != nil {
		return nil, err
	}
	currentEffectiveLogoFileID := normalizedOptionalString(current)
	if sameNormalizedOptionalString(previousEffectiveLogoFileID, currentEffectiveLogoFileID) {
		return nil, nil
	}
	if currentEffectiveLogoFileID == nil {
		if err := store.ClearOgAsset(ctx, labelID); err != nil {
			return nil, err
		}
		if err := store.CancelOG(ctx, labelID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	runID, err := store.RequestOG(ctx, labelID, reason)
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
	var label Label
	change, err := s.changeLabelLogo(ctx, command.LabelID, &label, slot, sharedtelemetry.AuditCollectionOperationAdded,
		func(ctx context.Context, store Store) (logoChange, error) {
			previousEffectiveLogoFileID := effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID)
			if sameOptionalString(slot.fileID(&label), &command.FileID) {
				return logoChange{}, nil
			}
			if err := store.LockAttachableFiles(ctx, []string{command.FileID}); err != nil {
				return logoChange{}, err
			}
			fileID := command.FileID
			if err := store.SetLabelLogo(ctx, command.LabelID, slot.variant, &fileID, time.Now()); err != nil {
				return logoChange{}, err
			}
			if err := store.BindLogo(ctx, command.FileID, command.LabelID, slot.variant); err != nil {
				return logoChange{}, err
			}
			ogRunID, err := refreshLabelOgForEffectiveLogoChange(ctx, store, command.LabelID, previousEffectiveLogoFileID, "label_logo_updated")
			return logoChange{changed: true, ogRunID: ogRunID, fileID: command.FileID, signalID: label.ID}, err
		})
	if err != nil {
		return LogoUpdate{}, err
	}
	logos := s.getLabelImageAssets(ctx, label.ID)
	if _, err := s.overlayLabelSourceLocaleDocument(ctx, &label); err != nil {
		return LogoUpdate{}, err
	}
	return LogoUpdate{Light: logos.Light, Dark: logos.Dark, OgGenerationRunID: change.ogRunID}, nil
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
	var label Label
	change, err := s.changeLabelLogo(ctx, command.LabelID, &label, slot, sharedtelemetry.AuditCollectionOperationRemoved,
		func(ctx context.Context, store Store) (logoChange, error) {
			previousEffectiveLogoFileID := effectiveLabelLogoFileID(label.LogoLightFileID, label.LogoDarkFileID)
			oldFileID := slot.fileID(&label)
			if oldFileID == nil {
				return logoChange{}, nil
			}
			if err := store.SetLabelLogo(ctx, command.LabelID, slot.variant, nil, time.Now()); err != nil {
				return logoChange{}, err
			}
			if err := store.ReleaseLogoBinding(ctx, command.LabelID, slot.variant); err != nil {
				return logoChange{}, err
			}
			ogRunID, err := refreshLabelOgForEffectiveLogoChange(ctx, store, command.LabelID, previousEffectiveLogoFileID, "label_logo_removed")
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
	label *Label,
	slot labelLogoSlot,
	operation sharedtelemetry.AuditCollectionOperation,
	change func(context.Context, Store) (logoChange, error),
) (logoChange, error) {
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[logoChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			var err error
			*label, err = store.LockLabel(ctx, labelID)
			return err
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
		Outbox: func(ctx context.Context, store Store, change logoChange) error {
			if !change.changed {
				return nil
			}
			return store.PublishContentUpdate(ctx, ContentUpdate{LabelID: change.signalID, Kind: MediaUpdate})
		},
	})
	if errors.Is(err, ErrLabelNotFound) {
		return logoChange{}, domainerr.ResourceNotFound("label", labelID)
	}
	return result, err
}
