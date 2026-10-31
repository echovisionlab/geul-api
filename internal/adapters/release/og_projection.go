// Package release adapts Release persistence to shared capabilities.
package release

import (
	"context"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
)

// Projection preserves completion of Release OG work created before new
// Release generation was disabled. No current request source creates it.
type Projection struct{ *og.EntityProjection }

func NewProjection() *Projection {
	return &Projection{EntityProjection: og.NewEntityProjection("release")}
}

func (*Projection) ReleasePending(context.Context, *gorm.DB, og.Target, string) error {
	return errs.FailedPrecondition("new Release OG generation is disabled")
}

var _ og.Projection = (*Projection)(nil)
