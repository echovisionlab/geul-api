// Package post adapts Post persistence to shared capabilities.
package post

import "github.com/echovisionlab/geul-api/internal/og"

// NewProjection owns the current OG pointers and bindings for Post.
func NewProjection() *og.TranslationProjection {
	return og.NewTranslationProjection(og.TranslationProjectionSpec{
		EntityType: "post", EntityTitle: "Post", Localeless: og.LocalelessEntityRow,
	})
}
