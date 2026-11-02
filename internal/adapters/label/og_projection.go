// Package label adapts Label persistence to shared capabilities.
package label

import ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"

// NewProjection owns the current OG pointer and binding for Label.
func NewProjection() *ogpostgres.EntityProjection { return ogpostgres.NewEntityProjection("label") }
