package referencecatalog

import (
	"context"

	"gorm.io/gorm"
)

// MenuTargets keeps menu references consistent when a catalog target changes.
// The menu domain implements this port; the catalog domain owns when it is called.
type MenuTargets interface {
	UpdateSlug(context.Context, *gorm.DB, MenuTargetSlugChange) error
	Remove(context.Context, *gorm.DB, MenuTarget) error
}

type MenuTarget struct {
	LinkType string
	ID       string
	Slug     string
}

type MenuTargetSlugChange struct {
	Target   MenuTarget
	NextSlug string
}
