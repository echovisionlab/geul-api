package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// LoadTranslationSourceDocument exposes the Campaign-owned typed document to
// the shared translation orchestrator.
func LoadTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	campaignID string,
) (*translation.SourceDocument, error) {
	return loadCampaignEmailTypedTranslationSourceDocument(
		ctx, db, store, campaignContentEntity, campaignID,
	)
}

func loadCampaignEmailTypedTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	entityType string,
	entityID string,
) (*translation.SourceDocument, error) {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return nil, errs.Internal(err)
	}
	if store == nil {
		return nil, errors.New("campaign translation content block store is not configured")
	}
	domain, err := loadCampaignEmailSourceContext(ctx, db, entityType, entityID)
	if err != nil {
		return nil, err
	}
	subject, err := loadCampaignEmailSourceSubject(ctx, db, entityType, entityID, domain.SourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, db, entityType, entityID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, domain.SourceLocale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(entityType, err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return nil, errs.FailedPrecondition("Campaign translation source requires the Email content profile")
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, domain.SourceLocale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(entityType, err)
	}
	return &translation.SourceDocument{
		Title:                   subject,
		ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument:    document,
	}, nil
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
	return applyCampaignEmailTypedTranslationCandidateWithDB(ctx, tx, store, job, candidate, input, auditWriter)
}

func applyCampaignEmailTypedTranslationCandidateWithDB(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Campaign translation candidate and content Block store are required")
	}
	if job.EntityType != campaignContentEntity {
		return fmt.Errorf("unsupported Campaign translation entity %q", job.EntityType)
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return errs.FailedPrecondition("translated Campaign Block overlay locale does not match the target locale")
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, campaignContentEntity, job.EntityID)
	if err != nil {
		return err
	}
	applyFence := campaignTranslationJobApplyFence(campaignContentEntity, job.EntityID)
	domain, err := applyFence(ctx, tx, documentID)
	if err != nil {
		return err
	}
	currentSnapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if candidate.Title == nil || strings.TrimSpace(*candidate.Title) == "" {
		return errs.FailedPrecondition("translated Campaign subject is required")
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		currentSnapshot,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		job.TargetLocale,
		candidate,
	)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		memberID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errs.InternalMsg("Campaign translation audit requires canonical requester Member")
		}
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
		result, applyErr := store.ApplyBatchWithMetadata(
			ctx, tx, batch, applyFence,
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				return applyCampaignAIDocumentSubject(ctx, tx, AIDocumentMutation{
					CampaignID: job.EntityID, Locale: domain.SourceLocale, ExpectedSource: domain.SourceLocale,
					ExpectedPresence: true, SetSubject: true, Subject: strings.TrimSpace(*candidate.Title),
				}, input.Now)
			},
		)
		if applyErr != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Campaign translation did not advance source state")
		}
		if err := tx.WithContext(ctx).Table("campaign").Where("id = ?", job.EntityID).Update("updated_at", input.Now).Error; err != nil {
			return errs.Internal(err)
		}
		resultSnapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
		if loadErr != nil {
			return normalizeCampaignEmailContentBlockError(campaignContentEntity, loadErr)
		}
		if err := projectCampaignEmailMaterializedContent(
			ctx, tx, campaignContentEntity, job.EntityID, resultSnapshot, []string{domain.SourceLocale}, input.Now,
		); err != nil {
			return err
		}
		return appendCampaignLocaleContentAudit(
			ctx, tx, auditWriter, memberID.String(), job.EntityID, domain.SourceLocale,
			campaignLocaleContentOperation(true, false, false, true),
		)
	}
	output, err := applyCampaignTargetMutation(
		ctx, tx, store,
		campaignTargetMutationInput{
			CampaignID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale,
			Batch: batch, ExpectedDocumentRevision: currentSnapshot.Document.Revision,
			AllowCreate: true, AllowLocaleDeletes: true, OverwriteCurrentTargetCAS: true,
			SetSubject: true, Subject: candidate.Title, Now: input.Now, Fence: applyFence,
		},
	)
	if err != nil {
		var targetConflict *translation.TargetRevisionConflict
		if errors.As(err, &targetConflict) {
			return err
		}
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	if output.Result.Changed {
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errs.InternalMsg("Campaign translation audit requires requester Member")
		}
		if err := appendCampaignLocaleContentAudit(
			ctx, tx, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
			job.EntityID, job.TargetLocale,
			campaignLocaleContentOperation(false, output.LocaleCreated, false, !output.LocaleCreated),
		); err != nil {
			return err
		}
	}
	resultSnapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(resultSnapshot, job.TargetLocale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(campaignContentEntity, err)
	}
	candidate.ContentHTML = &materialized.HTML
	candidate.ContentText = &materialized.Text
	return nil
}
