// Package series adapts Series persistence to shared capabilities.
package series

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

// NewProjection owns the current OG pointers and bindings for Series.
func NewProjection() *ogpostgres.TranslationProjection {
	return ogpostgres.NewTranslationProjection(ogpostgres.TranslationProjectionSpec{
		EntityType: "series", EntityTitle: "Series", Localeless: ogpostgres.LocalelessMissing,
	})
}
