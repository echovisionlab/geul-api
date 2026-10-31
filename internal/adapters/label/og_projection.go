// Package label adapts Label persistence to shared capabilities.
package label

import "github.com/echovisionlab/geul-api/internal/og"

// NewProjection owns the current OG pointer and binding for Label.
func NewProjection() *og.EntityProjection { return og.NewEntityProjection("label") }
