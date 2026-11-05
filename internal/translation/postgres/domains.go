package translationpostgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
)

func (s *store) Domains() application.Domains {
	return domains{tx: s.tx, ports: s.ports}
}

// domains binds the owning domains' adapters to the Store's transaction.
type domains struct {
	tx    *gorm.DB
	ports Ports
}

func (d domains) LoadSourceDocument(ctx context.Context, entityType, entityID string) (*translation.SourceDocument, error) {
	return d.ports.Domains.LoadSourceDocument(ctx, d.tx, d.ports.ContentBlocks, entityType, entityID)
}

func (d domains) RequestLocaleOG(ctx context.Context, entityType, entityID, locale, reason string) (bool, error) {
	return d.ports.Domains.RequestLocaleOG(ctx, d.tx, d.ports.OGPlanner, d.ports.OGRefresher, entityType, entityID, locale, reason)
}

func (d domains) LockRoot(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.LockRoot(ctx, d.tx, entityType, entityID)
}

func (d domains) RequireEditable(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.RequireEditable(ctx, d.tx, entityType, entityID)
}

func (d domains) RequireInterchangeView(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.RequireTranslationInterchangeView(ctx, d.tx, d.ports.SpiceDB, entityType, entityID)
}

func (d domains) RequireInterchangeEdit(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.RequireTranslationInterchangeEdit(ctx, d.tx, d.ports.SpiceDB, entityType, entityID)
}

func (d domains) RequireTranslationRead(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.RequireTranslationRead(ctx, d.tx, d.ports.SpiceDB, entityType, entityID)
}

func (d domains) RequireSourceLocaleEdit(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.RequireSourceLocaleEdit(ctx, d.tx, d.ports.SpiceDB, entityType, entityID)
}

func (d domains) RequireDocumentContributors(ctx context.Context, contributors []string) error {
	return d.ports.Domains.RequireDocumentContributors(ctx, d.tx, contributors)
}

func (d domains) RequireSourceMutable(ctx context.Context, entityType, entityID string) error {
	return d.ports.Domains.RequireTranslationSourceMutable(ctx, d.tx, entityType, entityID)
}

func (d domains) SourceLocaleAudit(ctx context.Context, entityType, entityID, previousLocale, newLocale string) (application.SourceLocaleAudit, error) {
	return d.ports.Domains.SourceLocaleAudit(ctx, d.tx, entityType, entityID, previousLocale, newLocale)
}

func (d domains) LoadInterchangeTarget(ctx context.Context, entityType, entityID, locale string, plan *translation.ExtractionPlan) (application.TranslationInterchangeTargetState, error) {
	return d.ports.Interchange.LoadTranslationInterchangeTarget(ctx, d.tx, d.ports.ContentBlocks, entityType, entityID, locale, plan)
}

func (d domains) ApplyInterchange(ctx context.Context, apply application.TranslationInterchangeApply) (application.TranslationInterchangeApplyResult, error) {
	return d.ports.Interchange.ApplyTranslationInterchange(ctx, d.tx, d.ports.ContentBlocks, apply)
}
