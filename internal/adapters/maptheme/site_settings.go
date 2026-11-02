package mapthemeadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sitesettings"
	sitesettingspostgres "github.com/echovisionlab/geul-api/internal/sitesettings/postgres"
	"gorm.io/gorm"
)

type SiteSettings struct{}

func (SiteSettings) DefaultThemeID(ctx context.Context, tx *gorm.DB) (string, error) {
	return sitesettingspostgres.NewDefaultThemeStore(tx).DefaultThemeID(ctx)
}

func (SiteSettings) LockDefaultThemeID(ctx context.Context, tx *gorm.DB) (string, error) {
	return sitesettingspostgres.NewDefaultThemeStore(tx).LockDefaultThemeID(ctx)
}

func (SiteSettings) SetDefaultThemeID(ctx context.Context, tx *gorm.DB, themeID string) (mutation.Audit, error) {
	return sitesettings.SetDefaultTheme(ctx, sitesettingspostgres.NewDefaultThemeStore(tx), themeID)
}
