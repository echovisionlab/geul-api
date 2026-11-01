package menuadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/menu"
	menupostgres "github.com/echovisionlab/geul-api/internal/menu/postgres"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
)

// Join binds the Menu Store and audit to tx, a transaction another pipeline
// opened, for a Menu step that joins it. A nil audit stays nil: the joined
// step is explicitly unaudited.
func Join(tx *gorm.DB, audit domainaudit.Appender) menu.Joined {
	joined := menu.Joined{Store: menupostgres.NewStore(tx, menupostgres.Ports{})}
	if audit != nil {
		joined.Audit = boundAudit{audit: audit, tx: tx}
	}
	return joined
}

// boundAudit appends in the joined transaction whatever transaction the
// Menu step names, which is nil.
type boundAudit struct {
	audit domainaudit.Appender
	tx    *gorm.DB
}

func (b boundAudit) AppendDomainAuditInTransaction(ctx context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return b.audit.AppendDomainAuditInTransaction(ctx, b.tx, record)
}

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
	return menu.UpdateTargetSlug(ctx, Join(tx, l.audit), linkType, targetID, oldSlug, nextSlug)
}

// Remove removes Menu targets after an owning domain deletes a target.
func (l TargetLifecycle) Remove(ctx context.Context, tx *gorm.DB, linkType, targetID, targetSlug string) error {
	return menu.RemoveTarget(ctx, Join(tx, l.audit), linkType, targetID, targetSlug)
}
