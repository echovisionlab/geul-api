package sitesettings

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// Setting is one requested setting value.
type Setting struct {
	Key   string
	Value structured.Value
}

// SetSettingCommand stores one setting in the singleton settings row.
type SetSettingCommand struct {
	Key   string
	Value structured.Value
}

// SetManySettingsCommand stores Settings atomically.
type SetManySettingsCommand struct {
	Settings []Setting
}

// AddSiteLoaderAssetCommand appends a File to the site loader assets.
type AddSiteLoaderAssetCommand struct {
	FileID string
}

// RemoveSiteLoaderAssetCommand removes a File from the site loader assets.
type RemoveSiteLoaderAssetCommand struct {
	FileID string
}

// SettingsUpdate is a committed settings change: the OG regeneration run it
// requested, if any.
type SettingsUpdate struct {
	OGGenerationRunID *string
}

// settingsChange is what a settings mutation stored.
type settingsChange struct {
	changedKeys []string
	ogRunID     *string
}

// SetSetting stores a single setting in the singleton settings row.
func (s *Service) SetSetting(ctx context.Context, command SetSettingCommand) (_ SettingsUpdate, returnErr error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return SettingsUpdate{}, err
	}
	if _, ok := defaultSettings[command.Key]; !ok {
		return SettingsUpdate{}, domainerr.InvalidField("key", fmt.Sprintf("invalid setting key: %s", command.Key))
	}
	defer failurecause.Annotate(&returnErr, slog.String("key", command.Key))
	return s.updateSettings(ctx, []string{command.Key}, func(settings *model.SiteSettings) error {
		if err := applySettingValue(settings, command.Key, command.Value); err != nil {
			return domainerr.InvalidField("value", err.Error())
		}
		return nil
	})
}

// SetManySettings stores multiple settings atomically.
func (s *Service) SetManySettings(ctx context.Context, command SetManySettingsCommand) (SettingsUpdate, error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return SettingsUpdate{}, err
	}
	if err := validateSiteSettingKeys(command.Settings); err != nil {
		return SettingsUpdate{}, err
	}
	keys := make([]string, 0, len(command.Settings))
	for _, setting := range command.Settings {
		keys = append(keys, setting.Key)
	}
	return s.updateSettings(ctx, keys, func(settings *model.SiteSettings) error {
		return applyRequestedSiteSettings(settings, command.Settings)
	})
}

// updateSettings applies the requested keys to the locked settings row and
// stores the ones that changed with their bindings, OG regeneration and
// audit record.
func (s *Service) updateSettings(ctx context.Context, keys []string, apply func(*model.SiteSettings) error) (SettingsUpdate, error) {
	var settings *model.SiteSettings
	var before model.SiteSettings
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[settingsChange]{
		// A value the row rejects answers before authority, as it always has.
		Lock: func(_ context.Context, tx *gorm.DB) error {
			var err error
			if settings, err = loadSettingsRowForUpdate(tx); err != nil {
				return err
			}
			before = cloneSiteSettingsForComparison(settings)
			return apply(settings)
		},
		Access: freshEdit(),
		Change: func(ctx context.Context, tx *gorm.DB) (settingsChange, error) {
			return s.storeSettings(ctx, tx, &before, settings, keys)
		},
		Audit: func(change settingsChange) mutation.Audit { return settingsAudit(change.changedKeys) },
	})
	return SettingsUpdate{OGGenerationRunID: change.ogRunID}, err
}

func (s *Service) storeSettings(
	ctx context.Context,
	tx *gorm.DB,
	before *model.SiteSettings,
	settings *model.SiteSettings,
	keys []string,
) (settingsChange, error) {
	changedKeys := changedSiteSettingKeys(before, settings, keys)
	if len(changedKeys) == 0 {
		return settingsChange{}, nil
	}
	if err := s.validateSiteSettingReferences(ctx, tx, settings, keys); err != nil {
		return settingsChange{}, err
	}
	if err := s.lockAndValidateSiteSettingAssets(ctx, tx, settings, keys); err != nil {
		return settingsChange{}, err
	}
	settings.UpdatedAt = time.Now()
	if err := tx.Save(settings).Error; err != nil {
		return settingsChange{}, err
	}
	for _, key := range changedKeys {
		if err := s.syncSiteSettingAssetBinding(ctx, tx, settings, key); err != nil {
			return settingsChange{}, err
		}
	}
	runID, err := s.og.Request(ctx, tx, before, settings, changedKeys)
	if err != nil {
		return settingsChange{}, err
	}
	return settingsChange{changedKeys: changedKeys, ogRunID: runID}, nil
}

// AddSiteLoaderAsset appends a ready loader File to the site loader assets.
func (s *Service) AddSiteLoaderAsset(ctx context.Context, command AddSiteLoaderAssetCommand) (returnErr error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	fileID := strings.TrimSpace(command.FileID)
	if fileID == "" {
		return domainerr.InvalidField("file_id", "file_id is required")
	}
	defer failurecause.Annotate(&returnErr, slog.String("file_id", fileID))
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		// The File is checked before authority, as it always has been.
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if _, err := loadSettingsRowForUpdate(tx); err != nil {
				return err
			}
			if err := s.assets.LockForAttachment(ctx, tx, []string{fileID}); err != nil {
				return err
			}
			return s.assets.ValidateAttachment(ctx, tx, "loader", fileID)
		},
		Access: freshEdit(),
		Change: func(ctx context.Context, tx *gorm.DB) (bool, error) { return s.addLoaderFile(ctx, tx, fileID) },
		Audit:  loaderAudit,
	})
	return err
}

func (s *Service) addLoaderFile(ctx context.Context, tx *gorm.DB, fileID string) (bool, error) {
	var nextPosition int32
	if err := tx.Table("site_setting_loader_file").
		Select("COALESCE(MAX(position) + 1, 0)").
		Where("site_setting_id = ?", 1).
		Scan(&nextPosition).Error; err != nil {
		return false, err
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.SiteSettingLoaderFile{
		SiteSettingID: 1, FileID: fileID, Position: nextPosition,
	})
	if result.Error != nil || result.RowsAffected == 0 {
		return false, result.Error
	}
	if _, err := s.assets.BindReady(ctx, tx, AssetBinding{
		SourceFileID: fileID, Key: "loader_" + fileID, Kind: "loader",
	}); err != nil {
		return false, err
	}
	return true, touchSettings(tx)
}

// RemoveSiteLoaderAsset removes a File from the site loader assets.
func (s *Service) RemoveSiteLoaderAsset(ctx context.Context, command RemoveSiteLoaderAssetCommand) (returnErr error) {
	if err := authz.RequireAuthenticatedPrincipal(ctx); err != nil {
		return err
	}
	fileID := strings.TrimSpace(command.FileID)
	if fileID == "" {
		return domainerr.InvalidField("file_id", "file_id is required")
	}
	defer failurecause.Annotate(&returnErr, slog.String("file_id", fileID))
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock: func(_ context.Context, tx *gorm.DB) error {
			_, err := loadSettingsRowForUpdate(tx)
			return err
		},
		Access: freshEdit(),
		Change: func(ctx context.Context, tx *gorm.DB) (bool, error) {
			result := tx.Where("site_setting_id = ? AND file_id = ?", 1, fileID).Delete(&model.SiteSettingLoaderFile{})
			if result.Error != nil || result.RowsAffected == 0 {
				return false, result.Error
			}
			if err := s.assets.Release(ctx, tx, "loader_"+fileID); err != nil {
				return false, err
			}
			return true, touchSettings(tx)
		},
		Audit: loaderAudit,
	})
	return err
}

func loaderAudit(changed bool) mutation.Audit {
	if !changed {
		return mutation.Unchanged()
	}
	return settingsAudit([]string{"loader_file_ids"})
}

func touchSettings(tx *gorm.DB) error {
	return tx.Model(&model.SiteSettings{}).Where("id = ?", 1).Update("updated_at", time.Now()).Error
}
