package sitesettings

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
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
	return s.updateSettings(ctx, []string{command.Key}, func(settings *Settings) error {
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
	return s.updateSettings(ctx, keys, func(settings *Settings) error {
		return applyRequestedSiteSettings(settings, command.Settings)
	})
}

// updateSettings applies the requested keys to the locked settings and
// stores the ones that changed with their bindings, OG regeneration and
// audit record.
func (s *Service) updateSettings(ctx context.Context, keys []string, apply func(*Settings) error) (SettingsUpdate, error) {
	var settings, before Settings
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[settingsChange, Store]{
		// A value the row rejects answers before authority, as it always has.
		Lock: func(ctx context.Context, store Store) error {
			var err error
			if settings, err = store.LockSettings(ctx); err != nil {
				return err
			}
			before = settings.Clone()
			return apply(&settings)
		},
		Access: freshEdit(),
		Change: func(ctx context.Context, store Store) (settingsChange, error) {
			return storeSettings(ctx, store, &before, &settings, keys)
		},
		Audit: func(change settingsChange) mutation.Audit { return settingsAudit(change.changedKeys) },
	})
	return SettingsUpdate{OGGenerationRunID: change.ogRunID}, err
}

func storeSettings(ctx context.Context, store Store, before, settings *Settings, keys []string) (settingsChange, error) {
	changedKeys := changedSiteSettingKeys(before, settings, keys)
	if len(changedKeys) == 0 {
		return settingsChange{}, nil
	}
	if err := validateSiteSettingReferences(ctx, store, settings, keys); err != nil {
		return settingsChange{}, err
	}
	if err := lockAndValidateSiteSettingAssets(ctx, store, settings, keys); err != nil {
		return settingsChange{}, err
	}
	settings.UpdatedAt = time.Now()
	if err := store.SaveSettings(ctx, *settings); err != nil {
		return settingsChange{}, err
	}
	for _, key := range changedKeys {
		if err := syncSiteSettingAssetBinding(ctx, store, settings, key); err != nil {
			return settingsChange{}, err
		}
	}
	runID, err := store.RequestOGRegeneration(ctx, *before, *settings, changedKeys)
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
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		// The File is checked before authority, as it always has been.
		Lock: func(ctx context.Context, store Store) error {
			if _, err := store.LockSettings(ctx); err != nil {
				return err
			}
			if err := store.LockFilesForAttachment(ctx, []string{fileID}); err != nil {
				return err
			}
			return store.ValidateAttachment(ctx, "loader", fileID)
		},
		Access: freshEdit(),
		Change: func(ctx context.Context, store Store) (bool, error) { return addLoaderFile(ctx, store, fileID) },
		Audit:  loaderAudit,
	})
	return err
}

func addLoaderFile(ctx context.Context, store Store, fileID string) (bool, error) {
	added, err := store.AddLoaderFile(ctx, fileID)
	if err != nil || !added {
		return false, err
	}
	if err := store.BindAsset(ctx, AssetBinding{SourceFileID: fileID, Key: "loader_" + fileID, Kind: "loader"}); err != nil {
		return false, err
	}
	return true, store.TouchSettings(ctx)
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
	_, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Lock: func(ctx context.Context, store Store) error {
			_, err := store.LockSettings(ctx)
			return err
		},
		Access: freshEdit(),
		Change: func(ctx context.Context, store Store) (bool, error) {
			removed, err := store.RemoveLoaderFile(ctx, fileID)
			if err != nil || !removed {
				return false, err
			}
			if err := store.ReleaseAssets(ctx, "loader_"+fileID); err != nil {
				return false, err
			}
			return true, store.TouchSettings(ctx)
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
