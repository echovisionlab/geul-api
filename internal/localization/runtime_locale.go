package localization

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// RuntimeLocale is one translation_locale catalog row. Canonical locale
// identity is code-owned by this package; the database row controls runtime
// enablement, public visibility, and machine-translation policy.
type RuntimeLocale struct {
	Code                      string    `gorm:"column:code;type:text;primaryKey"`
	DisplayName               string    `gorm:"column:display_name;type:text;not null"`
	Enabled                   bool      `gorm:"column:enabled;not null"`
	IsPublic                  bool      `gorm:"column:is_public;not null"`
	Dir                       string    `gorm:"column:dir;type:text;not null"`
	MachineTranslationAllowed bool      `gorm:"column:machine_translation_allowed;not null"`
	FontProfile               *string   `gorm:"column:font_profile;type:text"`
	SortOrder                 int32     `gorm:"column:sort_order;not null"`
	CreatedAt                 time.Time `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt                 time.Time `gorm:"column:updated_at;not null;default:now()"`
}

func (RuntimeLocale) TableName() string {
	return "translation_locale"
}

// Catalog reads and validates the database-owned runtime locale policy.
type Catalog struct {
	db *gorm.DB
}

// NewCatalog creates a runtime locale catalog backed by db.
func NewCatalog(db *gorm.DB) *Catalog {
	if db == nil {
		panic("localization catalog database is required")
	}
	return &Catalog{db: db}
}

// All returns every runtime locale in stable product order.
func (catalog *Catalog) All(ctx context.Context) ([]RuntimeLocale, error) {
	var locales []RuntimeLocale
	if err := catalog.db.WithContext(ctx).
		Order("sort_order ASC, code ASC").
		Find(&locales).Error; err != nil {
		return nil, err
	}
	if err := validateRuntimeCatalog(locales); err != nil {
		return nil, err
	}
	return locales, nil
}

// Enabled returns runtime-enabled locales in stable product order.
func (catalog *Catalog) Enabled(ctx context.Context) ([]RuntimeLocale, error) {
	return catalog.filter(ctx, func(locale RuntimeLocale) bool { return locale.Enabled })
}

// MachineTranslationTargets returns enabled locales admitted for provider
// generation in stable product order.
func (catalog *Catalog) MachineTranslationTargets(ctx context.Context) ([]RuntimeLocale, error) {
	return catalog.filter(ctx, func(locale RuntimeLocale) bool {
		return locale.Enabled && locale.MachineTranslationAllowed
	})
}

// Find returns the exact canonical runtime locale. Aliases belong at request
// normalization boundaries and are not accepted as persisted catalog keys.
func (catalog *Catalog) Find(ctx context.Context, code string) (RuntimeLocale, error) {
	code = strings.TrimSpace(code)
	locales, err := catalog.All(ctx)
	if err != nil {
		return RuntimeLocale{}, err
	}
	for _, locale := range locales {
		if locale.Code == code {
			return locale, nil
		}
	}
	return RuntimeLocale{}, gorm.ErrRecordNotFound
}

func (catalog *Catalog) filter(
	ctx context.Context,
	include func(RuntimeLocale) bool,
) ([]RuntimeLocale, error) {
	locales, err := catalog.All(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]RuntimeLocale, 0, len(locales))
	for _, locale := range locales {
		if include(locale) {
			filtered = append(filtered, locale)
		}
	}
	return filtered, nil
}

func validateRuntimeCatalog(locales []RuntimeLocale) error {
	byCode := make(map[string]RuntimeLocale, len(locales))
	for _, locale := range locales {
		normalized := NormalizeSupportedLocale(locale.Code)
		if normalized == nil || *normalized != locale.Code {
			return fmt.Errorf("runtime locale code %q is not canonical", locale.Code)
		}
		if _, duplicate := byCode[locale.Code]; duplicate {
			return fmt.Errorf("runtime locale %q is duplicated", locale.Code)
		}
		byCode[locale.Code] = locale
	}
	canonicalCodes := CanonicalLocaleCodes()
	for _, code := range canonicalCodes {
		if _, exists := byCode[code]; !exists {
			return fmt.Errorf("runtime locale catalog is missing canonical locale %q", code)
		}
	}
	if len(byCode) != len(canonicalCodes) {
		return fmt.Errorf(
			"runtime locale catalog has %d locales; canonical registry has %d",
			len(byCode),
			len(canonicalCodes),
		)
	}
	return nil
}
