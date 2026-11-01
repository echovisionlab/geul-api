package campaign

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/emailcontent"

	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTranslationSourceDocument exposes the Campaign-owned typed document to
// the shared translation orchestrator.
func LoadTranslationSourceDocument(
	ctx context.Context,
	joined Joined,
	campaignID string,
) (*translation.SourceDocument, error) {
	return campaignContent.LoadTranslationSourceDocument(ctx, joined.Store, campaignID,
		func(ctx context.Context, store Store, id, sourceLocale string) (string, error) {
			return loadCampaignEmailSourceSubject(ctx, store, id, sourceLocale)
		},
	)
}

// ApplyTranslationCandidate replaces one generated Campaign target against the
// current document revision. Request-time document revisions are source
// artifacts, not target CAS tokens, so intervening target edits do not cancel
// an accepted AI replacement.
func ApplyTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job *emailcontent.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
) error {
	if job != nil && job.EntityType != campaignContentEntity {
		return fmt.Errorf("unsupported Campaign translation entity %q", job.EntityType)
	}
	var fence emailcontent.Fence
	var contentJob *emailcontent.TranslationJob
	if job != nil {
		fence = campaignTranslationJobApplyFence(joined.Store, job.EntityID)
		contentJob = &emailcontent.TranslationJob{
			EntityType: job.EntityType, EntityID: job.EntityID, SourceLocale: job.SourceLocale,
			TargetLocale: job.TargetLocale, RequestedByMemberID: job.RequestedByMemberID,
		}
	}
	bound := joined.Store
	return campaignContent.ApplyTranslationCandidate(ctx, bound, emailcontent.TranslationCandidate{
		Job: contentJob, Candidate: candidate, Input: input, Fence: fence,
	}, joined.Audit)
}
