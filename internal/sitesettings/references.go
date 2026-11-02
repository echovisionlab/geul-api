package sitesettings

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/mutation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

type DefaultThemeStore interface {
	DefaultThemeID(context.Context) (string, error)
	LockDefaultThemeID(context.Context) (string, error)
	SetDefaultThemeID(context.Context, string) error
}

type MenuReferenceStore interface {
	ClearMenuReferences(context.Context, string) ([]string, error)
}

// Joined binds settings references and their audit to a collaborating transaction.
type Joined struct {
	Store MenuReferenceStore
	Audit domainaudit.Appender
}

func SetDefaultTheme(ctx context.Context, store DefaultThemeStore, themeID string) (mutation.Audit, error) {
	if err := store.SetDefaultThemeID(ctx, themeID); err != nil {
		return mutation.Audit{}, err
	}
	return settingsAudit([]string{"default_map_theme_id"}), nil
}

func ClearMenuReferences(ctx context.Context, joined Joined, menuID string) error {
	changed, err := joined.Store.ClearMenuReferences(ctx, menuID)
	if err != nil {
		return err
	}
	if len(changed) == 0 {
		return nil
	}
	return domainaudit.AppendRequest(ctx, nil, joined.Audit, sharedtelemetry.AuditSiteSettingsUpdated, AuditRecord(changed))
}
