package referencecatalog

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// =============================================================================
// Logo Management (Admin)
// =============================================================================

func clientLogoColumnForVariant(variant managev1.ThemeAssetVariant) (string, error) {
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

// SetClientLogo sets the client logo
func (s *ClientService) SetClientLogo(
	ctx context.Context,
	req *connect.Request[managev1.SetClientLogoRequest],
) (*connect.Response[managev1.SetClientLogoResponse], error) {
	can, err := policyv1.Client.Edit(req.Msg.ClientId)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	logoColumn, err := clientLogoColumnForVariant(req.Msg.Variant)
	if err != nil {
		return nil, err
	}

	var logoAsset *commonv1.AssetRef
	changed := false

	// Use transaction with row-level locking to prevent race conditions
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the row with FOR UPDATE to prevent concurrent modifications
		var current struct {
			SelectedFileID *string `gorm:"column:selected_file_id"`
		}
		if err := tx.Table("client").
			Select(logoColumn+" AS selected_file_id").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", req.Msg.ClientId).
			Take(&current).Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if current.SelectedFileID != nil && *current.SelectedFileID == req.Msg.FileId {
			return nil
		}
		if err := s.assets.LockForAttachment(ctx, tx, []string{req.Msg.FileId}); err != nil {
			return err
		}

		// Update client logo atomically
		result := tx.Table("client").
			Where("id = ?", req.Msg.ClientId).
			Updates(structured.Fields{
				logoColumn: req.Msg.FileId,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		bindingKey := "logo:light"
		if logoColumn == "logo_dark_file_id" {
			bindingKey = "logo:dark"
		}
		logoRef, err := s.assets.BindReady(ctx, tx, AssetBinding{
			SourceFileID: req.Msg.FileId,
			Owner:        AssetOwner{Type: "client", ID: req.Msg.ClientId},
			Key:          bindingKey,
			Kind:         "logo",
		})
		if err != nil {
			return err
		}
		logoAsset = logoRef
		changed = true
		slot := sharedtelemetry.AuditAssetSlotLight
		if logoColumn == "logo_dark_file_id" {
			slot = sharedtelemetry.AuditAssetSlotDark
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewClientLogoUpdatedAuditRecord(metadata, req.Msg.ClientId, slot, sharedtelemetry.AuditCollectionOperationAdded, req.Msg.FileId)
		})
	})

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("client", req.Msg.ClientId)
		}
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	if !changed {
		assets := s.getClientLogoAssets(ctx, req.Msg.ClientId)
		if assets != nil {
			if logoColumn == "logo_dark_file_id" {
				logoAsset = assets.Dark
			} else {
				logoAsset = assets.Light
			}
		}
	}

	return connect.NewResponse(&managev1.SetClientLogoResponse{
		LogoLightAsset: optionalLogoResponseAsset(req.Msg.Variant, managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_LIGHT, logoAsset),
		LogoDarkAsset:  optionalLogoResponseAsset(req.Msg.Variant, managev1.ThemeAssetVariant_THEME_ASSET_VARIANT_DARK, logoAsset),
	}), nil
}

// DeleteClientLogo deletes the client logo
func (s *ClientService) DeleteClientLogo(
	ctx context.Context,
	req *connect.Request[managev1.DeleteClientLogoRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	can, err := policyv1.Client.Edit(req.Msg.ClientId)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}

	logoColumn, err := clientLogoColumnForVariant(req.Msg.Variant)
	if err != nil {
		return nil, err
	}

	var oldFileID *string

	// Use transaction with row-level locking to prevent race conditions
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the row with FOR UPDATE to prevent concurrent modifications
		var current struct {
			SelectedFileID *string `gorm:"column:selected_file_id"`
		}
		if err := tx.Table("client").
			Select(logoColumn+" AS selected_file_id").
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", req.Msg.ClientId).
			Take(&current).Error; err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		oldFileID = current.SelectedFileID
		if oldFileID == nil {
			return nil
		}

		// Clear the selected logo slot atomically.
		result := tx.Table("client").
			Where("id = ?", req.Msg.ClientId).
			Updates(structured.Fields{
				logoColumn: nil,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		bindingKey := "logo:light"
		if logoColumn == "logo_dark_file_id" {
			bindingKey = "logo:dark"
		}
		if err := s.assets.Release(ctx, tx, AssetRelease{
			Owner:         AssetOwner{Type: "client", ID: req.Msg.ClientId},
			BindingPrefix: bindingKey,
		}); err != nil {
			return err
		}
		slot := sharedtelemetry.AuditAssetSlotLight
		if logoColumn == "logo_dark_file_id" {
			slot = sharedtelemetry.AuditAssetSlotDark
		}
		return domainaudit.AppendOptionalRequest(ctx, tx, s.auditWriter, sharedtelemetry.AuditClientUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewClientLogoUpdatedAuditRecord(metadata, req.Msg.ClientId, slot, sharedtelemetry.AuditCollectionOperationRemoved, *oldFileID)
		})
	})

	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("client", req.Msg.ClientId)
		}
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}
