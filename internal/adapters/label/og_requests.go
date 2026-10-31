package label

import "github.com/echovisionlab/geul-api/internal/og"

const (
	titleSQL = `COALESCE(NULLIF((SELECT lt.title FROM label_translation AS lt
		JOIN label AS source ON source.id = lt.entity_id
			AND source.source_locale = lt.locale
		WHERE lt.entity_id = label.id LIMIT 1), ''), '')`
	featuredImageSQL = `COALESCE(label.logo_light_file_id, label.logo_dark_file_id)`
)

// NewRequests reads canonical Label snapshots for OG generation.
func NewRequests() *og.EntitySource {
	return og.NewEntitySource(og.EntityRequestSpec{
		EntityType: "label", Table: "label",
		TitleExpression: titleSQL, FeaturedImageExpression: featuredImageSQL,
		MissingImage: "label OG generation requires a light or dark logo",
	})
}
