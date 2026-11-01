package referencecatalog

import (
	"context"
	"errors"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
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

// clientLogoSlot is the binding key and audit slot of a logo slot.
type clientLogoSlot struct {
	bindingKey string
	audit      sharedtelemetry.AuditAssetSlot
}

func clientLogoSlotFor(slot ClientLogoSlot) (clientLogoSlot, error) {
	switch slot {
	case ClientLogoLight:
		return clientLogoSlot{bindingKey: "logo_light", audit: sharedtelemetry.AuditAssetSlotLight}, nil
	case ClientLogoDark:
		return clientLogoSlot{bindingKey: "logo_dark", audit: sharedtelemetry.AuditAssetSlotDark}, nil
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
// selected in slot.
func lockClientLogo(ctx context.Context, store Store, clientID string, slot ClientLogoSlot) (*string, error) {
	selected, err := store.LockClientLogo(ctx, clientID, slot)
	if errors.Is(err, ErrClientNotFound) {
		return nil, domainerr.ResourceNotFound("client", clientID)
	}
	return selected, err
}

// writeClientLogo stores fileID (nil clears it) in the locked Client's slot.
func writeClientLogo(ctx context.Context, store Store, clientID string, slot ClientLogoSlot, fileID *string) error {
	err := store.SetClientLogo(ctx, clientID, slot, fileID)
	if errors.Is(err, ErrClientNotFound) {
		return domainerr.ResourceNotFound("client", clientID)
	}
	return err
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
	logoAsset, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[*commonv1.AssetRef, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			selected, err = lockClientLogo(ctx, store, command.ClientID, command.Slot)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (*commonv1.AssetRef, error) {
			if selected != nil && *selected == command.FileID {
				return nil, nil
			}
			if err := store.LockFilesForAttachment(ctx, []string{command.FileID}); err != nil {
				return nil, err
			}
			if err := writeClientLogo(ctx, store, command.ClientID, command.Slot, &command.FileID); err != nil {
				return nil, err
			}
			return store.BindReadyAsset(ctx, AssetBinding{
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
		light, dark := s.clientLogos(ctx, command.ClientID)
		if command.Slot == ClientLogoDark {
			return dark, nil
		}
		return light, nil
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
	_, err = mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock: func(ctx context.Context, store Store) (err error) {
			selected, err = lockClientLogo(ctx, store, command.ClientID, command.Slot)
			return err
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (struct{}, error) {
			if selected == nil {
				return struct{}{}, nil
			}
			if err := writeClientLogo(ctx, store, command.ClientID, command.Slot, nil); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, store.ReleaseAssets(ctx, AssetRelease{
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
