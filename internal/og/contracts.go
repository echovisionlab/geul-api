// Package og owns durable, domain-neutral Open Graph generation lifecycle.
package og

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/localization"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

var ErrTranslationTargetMissing = errors.New("OG translation target is missing")

// Target identifies one persisted OG projection without encoding a product
// domain's table, binding owner, or current-pointer rules.
type Target struct {
	EntityType string
	EntityID   string
	Locale     *string
	Kind       string
}

// CanonicalLocale returns the optional canonical locale used by projection
// rows and binding keys. A non-blank unsupported locale fails closed.
func (target Target) CanonicalLocale() (string, error) {
	locale, ok := localization.NormalizeOptionalSupportedLocale(target.Locale)
	if !ok {
		return "", errs.InvalidArgument("locale", "unsupported locale")
	}
	return locale, nil
}

// Request is a canonical target snapshot supplied by its owning adapter.
type Request struct {
	Target
	Title               string
	FeaturedImageFileID *string
}

// Plan is the durable run and generation identities allocated for requests.
type Plan struct {
	RunID         string
	GenerationIDs []string
}

// ReloadRequests rereads canonical target state only after every target lock.
type ReloadRequests func(context.Context, *gorm.DB) ([]Request, error)

// RenderConfig reads the canonical shared render configuration under the
// planner's target-lock transaction boundary.
type RenderConfig interface {
	Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error)
}

// Projection owns an entity family's current-pointer and public-binding
// transitions. It receives only the durable target identity and asset ID.
type Projection interface {
	Handles(Target) bool
	ReleasePending(context.Context, *gorm.DB, Target, string) error
	Complete(context.Context, *gorm.DB, Target, string, time.Time, string) error
}

// RequestSource owns canonical request snapshots for one entity family.
// It receives the decoded public selection but not lifecycle state.
type RequestSource interface {
	Handles(string) bool
	Resolve(context.Context, *gorm.DB, string, string, *managev1.OgTargetSelection) ([]Request, error)
}

// AllRequestSource contributes current requests for global regeneration.
type AllRequestSource interface {
	All(context.Context, *gorm.DB) ([]Request, error)
}

func projectionFor(projections []Projection, target Target) (Projection, error) {
	for _, projection := range projections {
		if projection != nil && projection.Handles(target) {
			return projection, nil
		}
	}
	return nil, errs.InvalidEntityType(target.EntityType)
}
