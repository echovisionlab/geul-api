package sitesettingspostgres

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func NewDefaultThemeStore(tx *gorm.DB) sitesettings.DefaultThemeStore {
	return referenceStore{tx: tx}
}

func Bind(tx *gorm.DB, audit domainaudit.Appender) sitesettings.Joined {
	return sitesettings.Joined{Store: referenceStore{tx: tx}, Audit: domainaudit.Bind(tx, audit)}
}

type referenceStore struct{ tx *gorm.DB }

func (s referenceStore) DefaultThemeID(ctx context.Context) (string, error) {
	var settings model.SiteSettings
	if err := s.tx.WithContext(ctx).Select("default_map_theme_id").Where("id = ?", 1).Take(&settings).Error; err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.DefaultMapThemeID) == "" {
		return "", fmt.Errorf("site settings default map theme is empty")
	}
	return settings.DefaultMapThemeID, nil
}

func (s referenceStore) LockDefaultThemeID(context.Context) (string, error) {
	var settings model.SiteSettings
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&settings, "id = 1").Error; err != nil {
		return "", err
	}
	return settings.DefaultMapThemeID, nil
}

func (s referenceStore) SetDefaultThemeID(_ context.Context, themeID string) error {
	settings := model.SiteSettings{ID: 1}
	return s.tx.Model(settings).Updates(structured.Fields{
		"default_map_theme_id": themeID,
		"updated_at":           time.Now(),
	}).Error
}

func (s referenceStore) ClearMenuReferences(ctx context.Context, menuID string) ([]string, error) {
	var settings model.SiteSettings
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&settings, "id = ?", 1).Error; err != nil {
		return nil, err
	}

	changed := make([]string, 0, 4)
	updates := structured.Fields{"updated_at": time.Now().UTC()}
	for _, slot := range []struct {
		field string
		value *string
	}{
		{"menu_avatar_dropdown_id", settings.MenuAvatarDropdownID},
		{"menu_footer_id", settings.MenuFooterID},
		{"menu_header_id", settings.MenuHeaderID},
		{"menu_secondary_id", settings.MenuSecondaryID},
	} {
		if slot.value == nil || strings.TrimSpace(*slot.value) != menuID {
			continue
		}
		updates[slot.field] = nil
		changed = append(changed, slot.field)
	}
	if len(changed) == 0 {
		return nil, nil
	}
	if err := s.tx.WithContext(ctx).Model(&model.SiteSettings{}).Where("id = ?", 1).Updates(updates).Error; err != nil {
		return nil, err
	}
	return changed, nil
}
