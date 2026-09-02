// Package menuadapter contains composition adapters for the Menu domain.
package menuadapter

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// SiteSettingsReferences adapts Site Settings-owned Menu selections to Menu deletion.
type SiteSettingsReferences struct {
	auditWriter domainaudit.Appender
}

var _ menudomain.SiteSettingsReferences = SiteSettingsReferences{}

func NewSiteSettingsReferences(auditWriter domainaudit.Appender) SiteSettingsReferences {
	return SiteSettingsReferences{auditWriter: auditWriter}
}

func (a SiteSettingsReferences) ClearMenuReferences(
	ctx context.Context,
	tx *gorm.DB,
	menuID string,
) error {
	var settings model.SiteSettings
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&settings, "id = ?", 1).Error; err != nil {
		return errs.Internal(err)
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
		return nil
	}
	if err := tx.WithContext(ctx).Model(&model.SiteSettings{}).Where("id = ?", 1).Updates(updates).Error; err != nil {
		return errs.Internal(err)
	}
	return sitesettings.AppendAudit(ctx, tx, a.auditWriter, changed)
}
