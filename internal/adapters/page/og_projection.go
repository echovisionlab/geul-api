// Package page adapts Page persistence to shared capabilities.
package page

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

// NewProjection owns the current OG pointers and bindings for Page.
func NewProjection() *ogpostgres.TranslationProjection {
	return ogpostgres.NewTranslationProjection(ogpostgres.TranslationProjectionSpec{
		EntityType: "page", EntityTitle: "Page", Localeless: ogpostgres.LocalelessEntityRow,
	})
}
