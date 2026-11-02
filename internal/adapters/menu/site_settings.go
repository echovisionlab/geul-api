// Package menuadapter contains composition adapters for the Menu domain.
package menuadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/sitesettings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
)

// SiteSettingsReferences adapts Site Settings-owned Menu selections to Menu deletion.
type SiteSettingsReferences struct {
	auditWriter domainaudit.Appender
}

var _ menupostgres.SiteSettings = SiteSettingsReferences{}

func NewSiteSettingsReferences(auditWriter domainaudit.Appender) SiteSettingsReferences {
	return SiteSettingsReferences{auditWriter: auditWriter}
}

func (a SiteSettingsReferences) ClearMenuReferences(
	ctx context.Context,
	tx *gorm.DB,
	menuID string,
) error {
	return sitesettings.ClearMenuReferences(ctx, sitesettingspostgres.Bind(tx, a.auditWriter), menuID)
}
