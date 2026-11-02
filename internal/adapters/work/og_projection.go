// Package work adapts Work persistence to shared capabilities.
package work

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

// NewProjection owns the current OG pointers and bindings for Work.
func NewProjection() *ogpostgres.TranslationProjection {
	return ogpostgres.NewTranslationProjection(ogpostgres.TranslationProjectionSpec{
		EntityType: "work", EntityTitle: "Work", Localeless: ogpostgres.LocalelessRejected,
	})
}
