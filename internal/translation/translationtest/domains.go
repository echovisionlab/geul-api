package translationtest

import (
	"context"
	"sync"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/translation"
	"github.com/echovisionlab/geul-api/internal/translation/application"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// Domains is the fake owning-domain registry over the Store's entities. It
// allows every check and loads an entity's Source unless a hook says
// otherwise.
type Domains struct {
	store *Store

	mu sync.Mutex
	// Deny answers an authorization check (the Domains method name) on an
	// entity; nil allows it.
	Deny func(check, entityType, entityID string) error
	// OG answers RequestLocaleOG; nil prepares nothing.
	OG func(entityType, entityID, locale, reason string) (bool, error)
	// LoadTarget and ApplyTarget answer the XLIFF interchange calls.
	LoadTarget  func(entityType, entityID, locale string, plan *translation.ExtractionPlan) (application.TranslationInterchangeTargetState, error)
	ApplyTarget func(apply application.TranslationInterchangeApply) (application.TranslationInterchangeApplyResult, error)

	checks   []string
	ogCalls  []string
	imported []application.TranslationInterchangeApply
}

// Checks returns the authorization checks asked, as "Method entityType/entityID".
func (d *Domains) Checks() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.checks...)
}

// OGRequests returns the locale OG requests, as "entityType/entityID/locale reason".
func (d *Domains) OGRequests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ogCalls...)
}

// Imported returns the XLIFF applies.
func (d *Domains) Imported() []application.TranslationInterchangeApply {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]application.TranslationInterchangeApply(nil), d.imported...)
}

func (d *Domains) check(name, entityType, entityID string) error {
	d.mu.Lock()
	d.checks = append(d.checks, name+" "+entityType+"/"+entityID)
	deny := d.Deny
	d.mu.Unlock()
	if deny != nil {
		return deny(name, entityType, entityID)
	}
	return nil
}

func (d *Domains) LoadSourceDocument(_ context.Context, entityType, entityID string) (*translation.SourceDocument, error) {
	d.store.mu.Lock()
	defer d.store.mu.Unlock()
	entity, err := d.store.entity(entityType, entityID)
	if err != nil {
		return nil, domainerr.ResourceNotFound(entityType, entityID)
	}
	if entity.Source == nil {
		return &translation.SourceDocument{SourceLocale: entity.SourceLocale}, nil
	}
	source := *entity.Source
	return &source, nil
}

func (d *Domains) RequestLocaleOG(_ context.Context, entityType, entityID, locale, reason string) (bool, error) {
	d.mu.Lock()
	d.ogCalls = append(d.ogCalls, entityType+"/"+entityID+"/"+locale+" "+reason)
	og := d.OG
	d.mu.Unlock()
	if og != nil {
		return og(entityType, entityID, locale, reason)
	}
	return false, nil
}

func (d *Domains) LockRoot(_ context.Context, entityType, entityID string) error {
	d.store.mu.Lock()
	defer d.store.mu.Unlock()
	if _, err := d.store.entity(entityType, entityID); err != nil {
		return domainerr.ResourceNotFound(entityType, entityID)
	}
	return nil
}

func (d *Domains) RequireEditable(_ context.Context, entityType, entityID string) error {
	return d.check("RequireEditable", entityType, entityID)
}

func (d *Domains) RequireInterchangeView(_ context.Context, entityType, entityID string) error {
	return d.check("RequireInterchangeView", entityType, entityID)
}

func (d *Domains) RequireInterchangeEdit(_ context.Context, entityType, entityID string) error {
	return d.check("RequireInterchangeEdit", entityType, entityID)
}

func (d *Domains) RequireTranslationRead(_ context.Context, entityType, entityID string) error {
	return d.check("RequireTranslationRead", entityType, entityID)
}

func (d *Domains) RequireSourceLocaleEdit(_ context.Context, entityType, entityID string) error {
	return d.check("RequireSourceLocaleEdit", entityType, entityID)
}

func (d *Domains) RequireDocumentContributors(_ context.Context, contributors []string) error {
	if len(contributors) == 0 {
		return domainerr.InvalidField("contributor_member_ids", "collaboration mutation requires contributors")
	}
	return nil
}

func (d *Domains) RequireSourceMutable(_ context.Context, entityType, entityID string) error {
	return d.check("RequireSourceMutable", entityType, entityID)
}

// SourceLocaleAudit answers the Post record of a source-locale switch.
func (d *Domains) SourceLocaleAudit(_ context.Context, _ string, entityID, previousLocale, newLocale string) (application.SourceLocaleAudit, error) {
	return application.SourceLocaleAudit{
		Action: sharedtelemetry.AuditPostUpdated,
		Build: func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewPostSourceLocaleAuditRecord(metadata, entityID, previousLocale, newLocale)
		},
	}, nil
}

func (d *Domains) LoadInterchangeTarget(_ context.Context, entityType, entityID, locale string, plan *translation.ExtractionPlan) (application.TranslationInterchangeTargetState, error) {
	d.mu.Lock()
	load := d.LoadTarget
	d.mu.Unlock()
	if load != nil {
		return load(entityType, entityID, locale, plan)
	}
	return application.TranslationInterchangeTargetState{}, nil
}

func (d *Domains) ApplyInterchange(_ context.Context, apply application.TranslationInterchangeApply) (application.TranslationInterchangeApplyResult, error) {
	d.mu.Lock()
	d.imported = append(d.imported, apply)
	applyTarget := d.ApplyTarget
	d.mu.Unlock()
	if applyTarget != nil {
		return applyTarget(apply)
	}
	return application.TranslationInterchangeApplyResult{
		Revision: "target-after-import", Changed: true, AffectedUnitHandles: apply.UnitHandles,
	}, nil
}

var _ application.Domains = (*Domains)(nil)
