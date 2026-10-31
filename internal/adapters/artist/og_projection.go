// Package artist adapts Artist persistence to shared capabilities.
package artist

import "github.com/echovisionlab/geul-api/internal/og"

// NewProjection owns the current OG pointers and bindings for Artist.
func NewProjection() *og.TranslationProjection {
	return og.NewTranslationProjection(og.TranslationProjectionSpec{
		EntityType: "artist", EntityTitle: "Artist", Localeless: og.LocalelessRejected,
	})
}
