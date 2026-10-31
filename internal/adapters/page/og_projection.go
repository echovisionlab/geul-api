// Package page adapts Page persistence to shared capabilities.
package page

import "github.com/echovisionlab/geul-api/internal/og"

// NewProjection owns the current OG pointers and bindings for Page.
func NewProjection() *og.TranslationProjection {
	return og.NewTranslationProjection(og.TranslationProjectionSpec{
		EntityType: "page", EntityTitle: "Page", Localeless: og.LocalelessEntityRow,
	})
}
