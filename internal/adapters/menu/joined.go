package menuadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	"gorm.io/gorm"
)

// TargetLifecycle keeps Menu targets consistent with changes in owning
// domains, in the owning domain's transaction.
type TargetLifecycle struct {
	audit domainaudit.Appender
}

// NewTargetLifecycle returns the Menu target lifecycle. A nil audit leaves
// the rewrites unaudited.
func NewTargetLifecycle(audit domainaudit.Appender) TargetLifecycle {
	return TargetLifecycle{audit: audit}
}

// UpdateSlug rewrites Menu targets after an owning domain changes a slug.
func (l TargetLifecycle) UpdateSlug(ctx context.Context, tx *gorm.DB, linkType, targetID, oldSlug, nextSlug string) error {
	return menu.UpdateTargetSlug(ctx, menupostgres.Bind(tx, menupostgres.Ports{}, l.audit), linkType, targetID, oldSlug, nextSlug)
}

// Remove removes Menu targets after an owning domain deletes a target.
func (l TargetLifecycle) Remove(ctx context.Context, tx *gorm.DB, linkType, targetID, targetSlug string) error {
	return menu.RemoveTarget(ctx, menupostgres.Bind(tx, menupostgres.Ports{}, l.audit), linkType, targetID, targetSlug)
}
