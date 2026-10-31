package referencecatalog

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ClientLogoSlot names the theme slot of a Client logo.
type ClientLogoSlot string

const (
	ClientLogoLight ClientLogoSlot = "light"
	ClientLogoDark  ClientLogoSlot = "dark"
)

// clientLogoSlot is the column, binding key and audit slot of a logo slot.
type clientLogoSlot struct {
	column     string
	bindingKey string
	audit      sharedtelemetry.AuditAssetSlot
}

func clientLogoSlotFor(slot ClientLogoSlot) (clientLogoSlot, error) {
	switch slot {
	case ClientLogoLight:
		return clientLogoSlot{column: "logo_light_file_id", bindingKey: "logo_light", audit: sharedtelemetry.AuditAssetSlotLight}, nil
	case ClientLogoDark:
		return clientLogoSlot{column: "logo_dark_file_id", bindingKey: "logo_dark", audit: sharedtelemetry.AuditAssetSlotDark}, nil
	default:
		return clientLogoSlot{}, domainerr.InvalidField("variant", "unsupported theme asset variant")
	}
}

// SetClientLogoCommand selects FileID as the logo of ClientID in Slot. Slot
// is neither light nor dark for an unsupported variant.
type SetClientLogoCommand struct {
	ClientID string
	Slot     ClientLogoSlot
	FileID   string
}

// DeleteClientLogoCommand clears the logo of ClientID in Slot.
type DeleteClientLogoCommand struct {
	ClientID string
	Slot     ClientLogoSlot
}

// lockClientLogo takes the Client row for a logo change and returns the file
// selected in column.
func lockClientLogo(ctx context.Context, tx *gorm.DB, clientID, column string) (*string, error) {
	var current struct {
		SelectedFileID *string `gorm:"column:selected_file_id"`
	}
	if err := tx.WithContext(ctx).Table("client").
		Select(column+" AS selected_file_id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", clientID).
		Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("client", clientID)
		}
		return nil, err
	}
	return current.SelectedFileID, nil
}

// writeClientLogo stores fileID (nil clears it) in the locked Client's column.
func writeClientLogo(tx *gorm.DB, clientID, column string, fileID any) error {
	result := tx.Table("client").Where("id = ?", clientID).Updates(structured.Fields{column: fileID})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return domainerr.ResourceNotFound("client", clientID)
	}
	return nil
}

// SetClientLogo selects a ready logo for one slot and returns its asset.
func (s *Service) SetClientLogo(ctx context.Context, command SetClientLogoCommand) (_ *commonv1.AssetRef, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", command.ClientID))

	can, err := policyv1.Client.Edit(command.ClientID)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	slot, err := clientLogoSlotFor(command.Slot)
	if err != nil {
		return nil, err
	}

	var selected *string
	logoAsset, err := mutation.Run(ctx, s.mutations, mutation.Spec[*commonv1.AssetRef]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			selected, err = lockClientLogo(ctx, tx, command.ClientID, slot.column)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (*commonv1.AssetRef, error) {
			if selected != nil && *selected == command.FileID {
				return nil, nil
			}
			if err := s.assets.LockForAttachment(ctx, tx, []string{command.FileID}); err != nil {
				return nil, err
			}
			if err := writeClientLogo(tx, command.ClientID, slot.column, command.FileID); err != nil {
				return nil, err
			}
			return s.assets.BindReady(ctx, tx, AssetBinding{
				SourceFileID: command.FileID,
				Owner:        AssetOwner{Type: "client", ID: command.ClientID},
				Key:          slot.bindingKey,
				Kind:         "logo",
			})
		},
		Audit: func(bound *commonv1.AssetRef) mutation.Audit {
			if selected != nil && *selected == command.FileID {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientLogoUpdatedAuditRecord(metadata, command.ClientID, slot.audit, sharedtelemetry.AuditCollectionOperationAdded, command.FileID)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	if selected != nil && *selected == command.FileID {
		// The unchanged selection answers its current asset.
		assets := s.getClientLogoAssets(ctx, command.ClientID)
		if assets == nil {
			return nil, nil
		}
		if command.Slot == ClientLogoDark {
			return assets.Dark, nil
		}
		return assets.Light, nil
	}
	return logoAsset, nil
}

// DeleteClientLogo clears the logo of one slot.
func (s *Service) DeleteClientLogo(ctx context.Context, command DeleteClientLogoCommand) (returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("client_id", command.ClientID))

	can, err := policyv1.Client.Edit(command.ClientID)
	if err != nil {
		return err
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	slot, err := clientLogoSlotFor(command.Slot)
	if err != nil {
		return err
	}

	var selected *string
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Lock: func(ctx context.Context, tx *gorm.DB) (err error) {
			selected, err = lockClientLogo(ctx, tx, command.ClientID, slot.column)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (struct{}, error) {
			if selected == nil {
				return struct{}{}, nil
			}
			if err := writeClientLogo(tx, command.ClientID, slot.column, nil); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, s.assets.Release(ctx, tx, AssetRelease{
				Owner:         AssetOwner{Type: "client", ID: command.ClientID},
				BindingPrefix: slot.bindingKey,
			})
		},
		Audit: func(struct{}) mutation.Audit {
			if selected == nil {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewClientLogoUpdatedAuditRecord(metadata, command.ClientID, slot.audit, sharedtelemetry.AuditCollectionOperationRemoved, *selected)
			})
		},
	})
	return err
}
