package campaign

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTranslationSourceDocument exposes the Campaign-owned typed document to
// the shared translation orchestrator.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	campaignID string,
) (*translation.SourceDocument, error) {
	return campaignContent.LoadTranslationSourceDocument(ctx, db, store, campaignID,
		func(ctx context.Context, db *gorm.DB, id, sourceLocale string) (string, error) {
			return loadCampaignEmailSourceSubject(ctx, db, id, sourceLocale)
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
	if job != nil {
		fence = campaignTranslationJobApplyFence(job.EntityID)
	}
	return campaignContent.ApplyTranslationCandidate(ctx, tx, store, emailcontent.TranslationCandidate{
		Job: job, Candidate: candidate, Input: input, Fence: fence,
	}, auditWriter)
}
