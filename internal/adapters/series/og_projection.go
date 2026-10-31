// Package series adapts Series persistence to shared capabilities.
package series

import "github.com/echovisionlab/geul-api/internal/og"

// NewProjection owns the current OG pointers and bindings for Series.
func NewProjection() *og.TranslationProjection {
	return og.NewTranslationProjection(og.TranslationProjectionSpec{
		EntityType: "series", EntityTitle: "Series", Localeless: og.LocalelessMissing,
	})
}
