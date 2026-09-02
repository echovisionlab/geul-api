package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/translation"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// LoadTemplateTranslationSourceDocument exposes Email Template's typed source
// document to the shared translation orchestrator.
func LoadTemplateTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	templateID string,
) (*translation.SourceDocument, error) {
	if store == nil {
		return nil, errors.New("email template translation content block store is not configured")
	}
	domain, err := loadCampaignEmailSourceContext(ctx, db, emailTemplateContentEntity, templateID)
	if err != nil {
		return nil, err
	}
	subject, err := loadCampaignEmailSourceSubject(ctx, db, emailTemplateContentEntity, templateID, domain.SourceLocale)
	if err != nil {
		return nil, err
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, db, emailTemplateContentEntity, templateID)
	if err != nil {
		return nil, err
	}
	snapshot, err := store.LoadSnapshot(ctx, db, documentID, domain.SourceLocale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return nil, errs.FailedPrecondition("Email Template translation source requires the Email content profile")
	}
	document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, domain.SourceLocale)
	if err != nil {
		return nil, normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	return &translation.SourceDocument{
		Title: subject, ContentDocumentRevision: snapshot.Document.Revision.String(),
		ContentBlockDocument: document,
	}, nil
}

// ApplyTemplateTranslationCandidate commits one generated target for an
// already-accepted TranslationJob while its Email Template root still exists.
func ApplyTemplateTranslationCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	_ CampaignDeliveryReferences,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if store == nil || job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Email Template translation candidate and content Block store are required")
	}
	if job.EntityType != emailTemplateContentEntity {
		return errs.InvalidArgument("entity_type", "Email Template translation entity is required")
	}
	if candidate.ContentBlockLocaleOverlay.GetLocale() != job.TargetLocale {
		return errs.FailedPrecondition("translated Email Template Block overlay locale does not match the target locale")
	}
	documentID, err := loadCampaignEmailContentDocumentID(ctx, tx, emailTemplateContentEntity, job.EntityID)
	if err != nil {
		return err
	}
	applyFence := emailTemplateTranslationJobApplyFence(emailTemplateContentEntity, job.EntityID)
	domain, err := applyFence(ctx, tx, documentID)
	if err != nil {
		return err
	}
	currentSnapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if candidate.Title == nil || strings.TrimSpace(*candidate.Title) == "" {
		return errs.FailedPrecondition("translated email subject is required")
	}
	batch, err := translation.BuildProviderTargetRichTextBatch(
		currentSnapshot,
		contentv1.RichTextProfile_RICH_TEXT_PROFILE_EMAIL,
		job.TargetLocale,
		candidate,
	)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if candidate.HasProviderUnitPatch() && job.TargetLocale == domain.SourceLocale {
		memberID, parseErr := uuid.Parse(strings.TrimSpace(job.RequestedByMemberID))
		if parseErr != nil || memberID == uuid.Nil || memberID.String() != strings.TrimSpace(job.RequestedByMemberID) {
			return errs.InternalMsg("Email Template translation audit requires canonical requester Member")
		}
		batch.ContributorMemberIDs = []uuid.UUID{memberID}
		result, applyErr := store.ApplyBatchWithMetadata(
			ctx, tx, batch, applyFence,
			func(ctx context.Context, tx *gorm.DB) (contentblock.MetadataEffect, error) {
				return applyEmailTemplateAIDocumentSubject(ctx, tx, AIDocumentMutation{
					TemplateID: job.EntityID, Locale: domain.SourceLocale, ExpectedSource: domain.SourceLocale,
					ExpectedPresence: true, SetSubject: true, Subject: strings.TrimSpace(*candidate.Title),
				}, input.Now)
			},
		)
		if applyErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, applyErr)
		}
		if !result.Changed {
			return nil
		}
		if !result.TranslationSourceChanged {
			return errs.InternalMsg("provider source Email Template translation did not advance source state")
		}
		if err := tx.WithContext(ctx).Table("email_template").Where("id = ?", job.EntityID).Update("updated_at", input.Now).Error; err != nil {
			return errs.Internal(err)
		}
		resultSnapshot, loadErr := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
		if loadErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, loadErr)
		}
		if err := projectEmailTemplateMaterializedContent(ctx, tx, job.EntityID, resultSnapshot, nil, input.Now); err != nil {
			return err
		}
		localized, loadErr := contentblock.SnapshotToLocalizedRichTextDocument(resultSnapshot, domain.SourceLocale)
		if loadErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, loadErr)
		}
		projection, materializeErr := contentblock.MaterializeLocalizedRichTextDocument(ctx, localized, nil)
		if materializeErr != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, materializeErr)
		}
		if err := syncCustomEmailTemplateVariables(ctx, tx, job.EntityID, projection.HTML); err != nil {
			return err
		}
		return appendEmailTemplateLocaleContentAudit(
			ctx, tx, auditWriter, memberID.String(), job.EntityID, domain.SourceLocale,
			emailAuthoringLocaleContentOperation(true, false, false, true),
		)
	}
	output, err := applyEmailTemplateTargetMutation(
		ctx, tx, store,
		emailTemplateTargetMutationInput{
			TemplateID: job.EntityID, DocumentID: documentID, Locale: job.TargetLocale,
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
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	if output.Result.Changed {
		if strings.TrimSpace(job.RequestedByMemberID) == "" {
			return errs.InternalMsg("Email Template translation audit requires requester Member")
		}
		if err := appendEmailTemplateLocaleContentAudit(
			ctx, tx, auditWriter, strings.TrimSpace(job.RequestedByMemberID),
			job.EntityID, job.TargetLocale,
			emailAuthoringLocaleContentOperation(false, output.LocaleCreated, false, !output.LocaleCreated),
		); err != nil {
			return err
		}
	}
	resultSnapshot, err := store.LoadSnapshotInTransaction(ctx, tx, documentID, domain.SourceLocale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	document, err := contentblock.MaterializeSnapshotRichTextLocale(resultSnapshot, job.TargetLocale)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	materialized, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
	if err != nil {
		return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
	}
	candidate.ContentHTML = &materialized.HTML
	candidate.ContentText = &materialized.Text
	return nil
}

func projectEmailTemplateMaterializedContent(
	ctx context.Context,
	tx *gorm.DB,
	templateID string,
	snapshot contentblock.Snapshot,
	changedLocales []string,
	now time.Time,
) error {
	if snapshot.Document.Profile != emailContentProfile {
		return errs.FailedPrecondition("Email Template content document profile changed")
	}
	domain, err := loadCampaignEmailSourceContext(ctx, tx, emailTemplateContentEntity, templateID)
	if err != nil {
		return err
	}
	if snapshot.SourceLocale != domain.SourceLocale {
		return errs.FailedPrecondition("Email Template translation source locale changed; reload before saving")
	}
	// Translation rows are the persisted set of locale projections. A target
	// row may intentionally have no locale overlay because it currently falls
	// back to the source, so the Content Block document's stored overlays are
	// not sufficient to enumerate rows that must be refreshed.
	var persistedLocales []string
	if err := tx.WithContext(ctx).Table("email_template_translation").
		Where("entity_id = ?", templateID).
		Order("locale ASC").Pluck("locale", &persistedLocales).Error; err != nil {
		return errs.Internal(err)
	}
	requested := make(map[string]struct{}, len(changedLocales))
	for _, locale := range changedLocales {
		if locale = strings.TrimSpace(locale); locale != "" {
			requested[locale] = struct{}{}
		}
	}
	for _, locale := range persistedLocales {
		if len(requested) > 0 {
			if _, ok := requested[locale]; !ok {
				continue
			}
		}
		localized, err := contentblock.MaterializeSnapshotRichTextLocale(snapshot, locale)
		if err != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localized, nil)
		if err != nil {
			return normalizeCampaignEmailContentBlockError(emailTemplateContentEntity, err)
		}
		updates := map[string]any{
			"content_html": projection.HTML, "content_text": projection.Text,
		}
		if locale == domain.SourceLocale {
			updates["updated_at"] = now
		}
		result := tx.WithContext(ctx).Table("email_template_translation").
			Where("entity_id = ? AND locale = ?", templateID, locale).
			Updates(updates)
		if result.Error != nil {
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("Email Template locale metadata is not initialized")
		}
	}
	return nil
}
