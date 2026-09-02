// Package referencecatalogmenuadapter contains the menu-owned adapter for
// Reference Catalog target maintenance.
package referencecatalogmenuadapter

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	menudomain "github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

// Targets adapts menu-owned reference maintenance to the Reference Catalog boundary.
type Targets struct {
	lifecycle menudomain.TargetLifecycle
}

var _ referencecatalog.MenuTargets = Targets{}

func NewTargets(auditWriter domainaudit.Appender) Targets {
	return Targets{lifecycle: menudomain.NewTargetLifecycle(auditWriter)}
}

func (m Targets) UpdateSlug(
	ctx context.Context,
	db *gorm.DB,
	change referencecatalog.MenuTargetSlugChange,
) error {
	return m.lifecycle.UpdateSlug(
		ctx,
		db,
		change.Target.LinkType,
		change.Target.ID,
		change.Target.Slug,
		change.NextSlug,
	)
}

func (m Targets) Remove(
	ctx context.Context,
	db *gorm.DB,
	target referencecatalog.MenuTarget,
) error {
	return m.lifecycle.Remove(
		ctx,
		db,
		target.LinkType,
		target.ID,
		target.Slug,
	)
}
