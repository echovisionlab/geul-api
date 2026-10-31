// Package work adapts Work persistence to shared capabilities.
package work

import "github.com/echovisionlab/geul-api/internal/og"

// NewProjection owns the current OG pointers and bindings for Work.
func NewProjection() *og.TranslationProjection {
	return og.NewTranslationProjection(og.TranslationProjectionSpec{
		EntityType: "work", EntityTitle: "Work", Localeless: og.LocalelessRejected,
	})
}
