// Package sitesettingspostgres is the PostgreSQL Store of the Site Settings
// use cases: the site_settings singleton and its site_setting_loader_file
// rows, and the File, Page, Menu and OG adapters a settings transaction
// drives, bound to one gorm transaction.
package sitesettingspostgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/favicon"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Assets is the shared File and public asset lifecycle of the File
// settings (sitesettingsadapter.Assets).
type Assets interface {
	ValidateAttachment(ctx context.Context, tx *gorm.DB, key, fileID string) error
	LockForAttachment(ctx context.Context, tx *gorm.DB, fileIDs []string) error
	BindReady(ctx context.Context, tx *gorm.DB, binding sitesettings.AssetBinding) (*mediaasset.Reference, error)
	Release(ctx context.Context, tx *gorm.DB, bindingPrefix string) error
	ReplaceFavicon(ctx context.Context, tx *gorm.DB, fileID *string) error
	ReadyRef(ctx context.Context, tx *gorm.DB, fileID, kind string) (*mediaasset.Reference, error)
	ProjectFavicon(ctx context.Context, tx *gorm.DB, fileID string) (*mediaasset.Reference, *favicon.Set)
}

// References validates the selected homepage Page and Menus against the
// Page and Menu rows (sitesettingsadapter.References).
type References interface {
	Validate(ctx context.Context, tx *gorm.DB, key string, value *string) error
}

// OG plans the OG regeneration a settings change requires
// (sitesettingsadapter.Invalidator).
type OG interface {
	Request(ctx context.Context, tx *gorm.DB, before, after sitesettings.Settings, changedKeys []string) (*string, error)
}

// Ports are the other domains' adapters the Store drives in the settings
// transaction.
type Ports struct {
	Assets     Assets
	References References
	OG         OG
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Assets": p.Assets == nil, "References": p.References == nil, "OG": p.OG == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("site settings store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the production unit of work of the Site Settings use
// cases.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[sitesettings.Store], error) {
	if db == nil {
		return nil, errors.New("site settings store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) sitesettings.Store { return store{tx: tx, ports: ports} }), nil
}

type store struct {
	tx    *gorm.DB
	ports Ports
}

func (s store) Settings(ctx context.Context) (sitesettings.Settings, error) {
	var row model.SiteSettings
	if err := s.tx.WithContext(ctx).First(&row, "id = 1").Error; err != nil {
		return sitesettings.Settings{}, err
	}
	return SettingsFromRow(row), nil
}

func (s store) LockSettings(context.Context) (sitesettings.Settings, error) {
	var row model.SiteSettings
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&row, "id = 1").Error; err != nil {
		return sitesettings.Settings{}, err
	}
	return SettingsFromRow(row), nil
}

func (s store) SaveSettings(_ context.Context, settings sitesettings.Settings) error {
	row := rowFromSettings(settings)
	return s.tx.Save(&row).Error
}

func (s store) TouchSettings(context.Context) error {
	return s.tx.Model(&model.SiteSettings{}).Where("id = ?", 1).Update("updated_at", time.Now()).Error
}

func (s store) LoaderFileIDs(ctx context.Context) ([]string, error) {
	var rows []struct {
		FileID string `gorm:"column:file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("site_setting_loader_file AS slf").
		Select("slf.file_id").
		Where("slf.site_setting_id = ?", 1).
		Order("slf.position ASC, slf.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		fileIDs = append(fileIDs, row.FileID)
	}
	return fileIDs, nil
}

func (s store) AddLoaderFile(_ context.Context, fileID string) (bool, error) {
	var nextPosition int32
	if err := s.tx.Table("site_setting_loader_file").
		Select("COALESCE(MAX(position) + 1, 0)").
		Where("site_setting_id = ?", 1).
		Scan(&nextPosition).Error; err != nil {
		return false, err
	}
	result := s.tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.SiteSettingLoaderFile{
		SiteSettingID: 1, FileID: fileID, Position: nextPosition,
	})
	return result.RowsAffected != 0, result.Error
}

func (s store) RemoveLoaderFile(_ context.Context, fileID string) (bool, error) {
	result := s.tx.Where("site_setting_id = ? AND file_id = ?", 1, fileID).Delete(&model.SiteSettingLoaderFile{})
	return result.RowsAffected != 0, result.Error
}

func (s store) LockFilesForAttachment(ctx context.Context, fileIDs []string) error {
	return s.ports.Assets.LockForAttachment(ctx, s.tx, fileIDs)
}

func (s store) ValidateAttachment(ctx context.Context, key, fileID string) error {
	return s.ports.Assets.ValidateAttachment(ctx, s.tx, key, fileID)
}

func (s store) BindAsset(ctx context.Context, binding sitesettings.AssetBinding) error {
	_, err := s.ports.Assets.BindReady(ctx, s.tx, binding)
	return err
}

func (s store) ReleaseAssets(ctx context.Context, prefix string) error {
	return s.ports.Assets.Release(ctx, s.tx, prefix)
}

func (s store) ReplaceFavicon(ctx context.Context, fileID *string) error {
	return s.ports.Assets.ReplaceFavicon(ctx, s.tx, fileID)
}

func (s store) ReadyAsset(ctx context.Context, fileID, kind string) (*sitesettings.Asset, error) {
	ref, err := s.ports.Assets.ReadyRef(ctx, s.tx, fileID, kind)
	if err != nil {
		return nil, err
	}
	return AssetFromRef(ref), nil
}

func (s store) Favicon(ctx context.Context, fileID string) (*sitesettings.Asset, *sitesettings.FaviconSet) {
	ref, set := s.ports.Assets.ProjectFavicon(ctx, s.tx, fileID)
	return AssetFromRef(ref), FaviconSetFromRef(set)
}

func (s store) ValidateReference(ctx context.Context, key string, id *string) error {
	return s.ports.References.Validate(ctx, s.tx, key, id)
}

func (s store) RequestOGRegeneration(ctx context.Context, before, after sitesettings.Settings, changedKeys []string) (*string, error) {
	return s.ports.OG.Request(ctx, s.tx, before, after, changedKeys)
}
