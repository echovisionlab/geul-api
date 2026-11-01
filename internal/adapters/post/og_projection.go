// Package post adapts Post persistence to shared capabilities.
package post

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

// NewProjection owns the current OG pointers and bindings for Post.
func NewProjection() *ogpostgres.TranslationProjection {
	return ogpostgres.NewTranslationProjection(ogpostgres.TranslationProjectionSpec{
		EntityType: "post", EntityTitle: "Post", Localeless: ogpostgres.LocalelessEntityRow,
	})
}
