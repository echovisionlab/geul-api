// Package og owns durable, domain-neutral Open Graph generation lifecycle.
package og

import (
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
)

var (
	// ErrTranslationTargetMissing is a projection's answer when the row a
	// locale target completes no longer exists.
	ErrTranslationTargetMissing = errors.New("OG translation target is missing")
	// ErrGenerationNotFound is the Store's answer for a missing generation.
	ErrGenerationNotFound = errors.New("OG generation not found")
	// ErrTargetNotFound is the Store's answer for a missing target.
	ErrTargetNotFound = errors.New("OG generation target not found")
	// ErrRunNotFound is the Store's answer for a missing run.
	ErrRunNotFound = errors.New("OG generation run not found")
	// ErrEntityNotFound is a request source's answer for a missing entity.
	ErrEntityNotFound = errors.New("OG entity not found")
)

// Target identifies one persisted OG projection without encoding a product
// domain's table, binding owner, or current-pointer rules.
type Target struct {
	EntityType string
	EntityID   string
	Locale     *string
	Kind       string
}

// CanonicalLocale returns the optional canonical locale used by projection
// rows and binding keys. A non-blank unsupported locale fails closed.
func (target Target) CanonicalLocale() (string, error) {
	locale, ok := localization.NormalizeOptionalSupportedLocale(target.Locale)
	if !ok {
		return "", domainerr.InvalidField("locale", "unsupported locale")
	}
	return locale, nil
}

// Request is a canonical target snapshot supplied by its owning adapter.
type Request struct {
	Target
	Title               string
	FeaturedImageFileID *string
}

// Plan is the durable run and generation identities allocated for requests.
type Plan struct {
	RunID         string
	GenerationIDs []string
}

// SelectionKind is which targets of an entity a generation request selects.
type SelectionKind int

const (
	// SelectionMissing selects nothing; sources answer it as missing.
	SelectionMissing SelectionKind = iota
	SelectionPrimary
	SelectionLocale
	SelectionAllLocales
)

// Selection names the targets of one entity a manual or automatic request
// regenerates. Locale is set only for SelectionLocale.
type Selection struct {
	Kind   SelectionKind
	Locale string
}

// RefreshSelection is the selection an owning entity's mutation requests:
// one locale, every locale, or the primary target.
func RefreshSelection(locale string, allLocales bool) Selection {
	locale = strings.TrimSpace(locale)
	if locale != "" {
		return Selection{Kind: SelectionLocale, Locale: locale}
	}
	if allLocales {
		return Selection{Kind: SelectionAllLocales}
	}
	return Selection{Kind: SelectionPrimary}
}

// AssetRef is the immutable CDN reference of a ready public asset.
type AssetRef struct {
	AssetID   string
	URL       string
	Extension string
	MimeType  string
	FileSize  int64
	SHA256    []byte
}

// WrittenAsset is what the renderer wrote for an allocated OG asset.
type WrittenAsset struct {
	AssetID  string
	FileSize int64
	SHA256   []byte
}

func optionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
