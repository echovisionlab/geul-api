// Package artist adapts Artist persistence to shared capabilities.
package artist

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

// NewProjection owns the current OG pointers and bindings for Artist.
func NewProjection() *ogpostgres.TranslationProjection {
	return ogpostgres.NewTranslationProjection(ogpostgres.TranslationProjectionSpec{
		EntityType: "artist", EntityTitle: "Artist", Localeless: ogpostgres.LocalelessRejected,
	})
}
