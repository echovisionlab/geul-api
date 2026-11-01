package campaign

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	emailcontentpostgres "github.com/echovisionlab/geul-api/internal/emailcontent/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/translation"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

// LoadTranslationSourceDocument exposes the Campaign-owned typed document to
// the shared translation orchestrator.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	campaignID string,
) (*translation.SourceDocument, error) {
	return campaignContent.LoadTranslationSourceDocument(ctx, bindContent(db, store), campaignID,
		func(ctx context.Context, store contentStore, id, sourceLocale string) (string, error) {
			return loadCampaignEmailSourceSubject(ctx, store.tx, id, sourceLocale)
		},
	)
}

// ApplyTranslationCandidate replaces one generated Campaign target against the
// current document revision. Request-time document revisions are source
// artifacts, not target CAS tokens, so intervening target edits do not cancel
// an accepted AI replacement.
func ApplyTranslationCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if job != nil && job.EntityType != campaignContentEntity {
		return fmt.Errorf("unsupported Campaign translation entity %q", job.EntityType)
	}
	var fence contentblock.DomainFence
	var contentJob *emailcontent.TranslationJob
	if job != nil {
		fence = campaignTranslationJobApplyFence(job.EntityID)
		contentJob = &emailcontent.TranslationJob{
			EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
			TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
		}
	}
	bound := bindContent(tx, store)
	return campaignContent.ApplyTranslationCandidate(ctx, bound, emailcontent.TranslationCandidate{
		Job: contentJob, Candidate: candidate, Input: input, Fence: contentFence(bound, fence),
	}, joinedAudit{audit: auditWriter, tx: tx})
}

// joinedAudit appends in tx whatever transaction the shared write names.
type joinedAudit struct {
	audit domainaudit.Appender
	tx    *gorm.DB
}

func (a joinedAudit) AppendDomainAuditInTransaction(ctx context.Context, _ *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return a.audit.AppendDomainAuditInTransaction(ctx, a.tx, record)
}

// contentStore is the Campaign's email content bound to one gorm
// transaction, until Campaign moves to its own Store.
type contentStore struct {
	emailcontentpostgres.Store
	tx *gorm.DB
}

func bindContent(tx *gorm.DB, contents *contentblock.Store) contentStore {
	return contentStore{Store: emailcontentpostgres.New(tx, contents, campaignContentEntity), tx: tx}
}

// contentFence runs a gorm fence over the store's transaction.
func contentFence(store contentStore, fence contentblock.DomainFence) emailcontent.Fence {
	if fence == nil {
		return nil
	}
	return func(ctx context.Context, documentID uuid.UUID) (contentblock.DomainContext, error) {
		return fence(ctx, store.tx, documentID)
	}
}

func contentUnits(db *gorm.DB, contents *contentblock.Store) mutation.UnitOfWork[contentStore] {
	return mutation.Postgres(db, func(tx *gorm.DB) contentStore { return bindContent(tx, contents) })
}

func contentRunner(db *gorm.DB, deps InternalDeps) (*mutation.StoreRunner[contentStore], error) {
	return mutation.NewStoreRunner(contentUnits(db, deps.ContentBlocks), deps.SpiceDB, identitystate.PostgresFence{}, deps.Audit)
}
