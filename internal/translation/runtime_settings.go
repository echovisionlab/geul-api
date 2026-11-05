package translation

import (
	"context"
	"fmt"
	"time"

	"github.com/echovisionlab/geul-api/internal/localization"
)

const DefaultLocale = localization.LocaleEnglish

// RuntimeSettings is the singleton authority for the default locale.
type RuntimeSettings struct {
	DefaultLocale string
	UpdatedAt     *time.Time
}

func DefaultRuntimeSettings() RuntimeSettings {
	return RuntimeSettings{
		DefaultLocale: DefaultLocale,
	}
}

func NormalizeRuntimeSettings(input RuntimeSettings) (RuntimeSettings, error) {
	settings := input
	if normalized := localization.NormalizeSupportedLocale(settings.DefaultLocale); normalized != nil {
		settings.DefaultLocale = *normalized
	} else {
		return RuntimeSettings{}, fmt.Errorf("default locale must be a supported locale")
	}
	return settings, nil
}

// RuntimeSettingsReader reads policy without initializing the settings singleton.
type RuntimeSettingsReader interface {
	RuntimeSettings(context.Context) (RuntimeSettings, error)
}
