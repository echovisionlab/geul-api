package sitesettings

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// SetSetting stores a single setting in the singleton settings row.
func (s *SiteSettingService) SetSetting(
	ctx context.Context,
	req *connect.Request[managev1.SetSettingRequest],
) (*connect.Response[managev1.SetSettingResponse], error) {
	can, err := policyv1.SiteSetting.Edit(siteSettingAuthorizationID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	if _, ok := defaultSettings[req.Msg.Key]; !ok {
		return nil, errs.InvalidArgument("key", fmt.Sprintf("invalid setting key: %s", req.Msg.Key))
	}
	var value siteSettingValue
	if req.Msg.Value != nil {
		value = req.Msg.Value.AsInterface()
	}

	var ogGenerationRunID *string
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		settings, err := s.loadSettingsRowForUpdate(tx)
		if err != nil {
			return err
		}
		before := cloneSiteSettingsForComparison(settings)
		if err := s.applySettingValue(settings, req.Msg.Key, value); err != nil {
			return errs.InvalidArgument("value", err.Error())
		}
		changedKeys := s.changedSiteSettingKeys(&before, settings, []string{req.Msg.Key})
		if len(changedKeys) == 0 {
			return nil
		}
		if req.Msg.Key == "homepage_page_id" {
			if err := s.references.Validate(ctx, tx, req.Msg.Key, settings.HomepagePageID); err != nil {
				return err
			}
		} else if isSiteSettingMenuReference(req.Msg.Key) {
			if err := s.references.Validate(ctx, tx, req.Msg.Key, siteSettingReferenceValue(settings, req.Msg.Key)); err != nil {
				return err
			}
		}
		if fileID, _, _, managed := siteSettingAssetBinding(settings, req.Msg.Key); managed && fileID != nil {
			if err := s.assets.LockForAttachment(ctx, tx, []string{*fileID}); err != nil {
				return err
			}
			if err := s.assets.ValidateAttachment(ctx, tx, req.Msg.Key, *fileID); err != nil {
				return err
			}
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		settings.UpdatedAt = time.Now()
		if err := tx.Save(settings).Error; err != nil {
			return err
		}
		if err := s.syncSiteSettingAssetBinding(ctx, tx, settings, req.Msg.Key); err != nil {
			return err
		}
		ogGenerationRunID, err = s.og.Request(ctx, tx, &before, settings, []string{req.Msg.Key})
		if err != nil {
			return err
		}
		return s.appendSettingsAudit(ctx, tx, changedKeys)
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		slog.Error("failed to set setting", "key", req.Msg.Key, "error", err)
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.SetSettingResponse{Success: true, OgGenerationRunId: ogGenerationRunID}), nil
}

// SetManySettings stores multiple settings atomically.
func (s *SiteSettingService) SetManySettings(
	ctx context.Context,
	req *connect.Request[managev1.SetManySettingsRequest],
) (*connect.Response[managev1.SetManySettingsResponse], error) {
	can, err := policyv1.SiteSetting.Edit(siteSettingAuthorizationID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	if err := validateSiteSettingKeys(req.Msg.Settings); err != nil {
		return nil, err
	}
	runID, err := s.applySiteSettingBatch(ctx, req.Msg.Settings, can)
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		slog.Error("failed to set multiple settings", "error", err)
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.SetManySettingsResponse{Success: true, OgGenerationRunId: runID}), nil
}

func (s *SiteSettingService) AddSiteLoaderAsset(
	ctx context.Context,
	req *connect.Request[managev1.AddSiteLoaderAssetRequest],
) (*connect.Response[managev1.AddSiteLoaderAssetResponse], error) {
	can, err := policyv1.SiteSetting.Edit(siteSettingAuthorizationID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	fileID := strings.TrimSpace(req.Msg.FileId)
	if fileID == "" {
		return nil, errs.InvalidArgument("file_id", "file_id is required")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.loadSettingsRowForUpdate(tx); err != nil {
			return err
		}
		if err := s.assets.LockForAttachment(ctx, tx, []string{fileID}); err != nil {
			return err
		}
		if err := s.assets.ValidateAttachment(ctx, tx, "loader", fileID); err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		var nextPosition int32
		if err := tx.Table("site_setting_loader_file").
			Select("COALESCE(MAX(position) + 1, 0)").
			Where("site_setting_id = ?", 1).
			Scan(&nextPosition).Error; err != nil {
			return err
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.SiteSettingLoaderFile{
			SiteSettingID: 1, FileID: fileID, Position: nextPosition,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if _, err := s.assets.BindReady(ctx, tx, AssetBinding{
			SourceFileID: fileID, Key: "loader:" + fileID, Kind: "loader",
		}); err != nil {
			return err
		}
		if err := tx.Model(&model.SiteSettings{}).Where("id = ?", 1).Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		return s.appendSettingsAudit(ctx, tx, []string{"loader_file_ids"})
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		slog.Error("failed to add site loader asset", "file_id", fileID, "error", err)
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.AddSiteLoaderAssetResponse{Success: true}), nil
}

func (s *SiteSettingService) RemoveSiteLoaderAsset(
	ctx context.Context,
	req *connect.Request[managev1.RemoveSiteLoaderAssetRequest],
) (*connect.Response[managev1.RemoveSiteLoaderAssetResponse], error) {
	can, err := policyv1.SiteSetting.Edit(siteSettingAuthorizationID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return nil, err
	}
	fileID := strings.TrimSpace(req.Msg.FileId)
	if fileID == "" {
		return nil, errs.InvalidArgument("file_id", "file_id is required")
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := s.loadSettingsRowForUpdate(tx); err != nil {
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		result := tx.Where("site_setting_id = ? AND file_id = ?", 1, fileID).Delete(&model.SiteSettingLoaderFile{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}
		if err := s.assets.Release(ctx, tx, "loader:"+fileID); err != nil {
			return err
		}
		if err := tx.Model(&model.SiteSettings{}).Where("id = ?", 1).Update("updated_at", time.Now()).Error; err != nil {
			return err
		}
		return s.appendSettingsAudit(ctx, tx, []string{"loader_file_ids"})
	})
	if err != nil {
		if connectErr, ok := err.(*connect.Error); ok {
			return nil, connectErr
		}
		slog.Error("failed to remove site loader asset", "file_id", fileID, "error", err)
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.RemoveSiteLoaderAssetResponse{Success: true}), nil
}
