package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTemplateTranslationSourceDocument exposes Email Template's typed source
// document to the shared translation orchestrator.
func LoadTemplateTranslationSourceDocument(
	ctx context.Context,
	db *gorm.DB,
	store *contentblock.Store,
	templateID string,
) (*translation.SourceDocument, error) {
	return emailTemplateContent.LoadTranslationSourceDocument(ctx, db, store, templateID, loadEmailTemplateSourceSubject)
}

// ApplyTemplateTranslationCandidate commits one generated target for an
// already-accepted TranslationJob while its Email Template root still exists.
func ApplyTemplateTranslationCandidate(
	ctx context.Context,
	tx *gorm.DB,
	store *contentblock.Store,
	job *model.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
	auditWriter domainaudit.Appender,
) error {
	if job == nil || candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Email Template translation candidate is required")
	}
	if job.EntityType != emailTemplateContentEntity {
		return domainerr.InvalidField("entity_type", "Email Template translation entity is required")
	}
	return emailTemplateContent.ApplyTranslationCandidate(ctx, tx, store, emailcontent.TranslationCandidate{
		Job: job, Candidate: candidate, Input: input,
		Fence: emailTemplateTranslationJobApplyFence(job.EntityID),
	}, auditWriter)
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
		return domainerr.Precondition("Email Template content document profile changed")
	}
	domain, err := loadEmailTemplateSourceContext(ctx, tx, templateID, false)
	if err != nil {
		return err
	}
	if snapshot.SourceLocale != domain.SourceLocale {
		return domainerr.Precondition("Email Template translation source locale changed; reload before saving")
	}
	// Translation rows are the persisted set of locale projections. A target
	// row may intentionally have no locale overlay because it currently falls
	// back to the source, so the Content Block document's stored overlays are
	// not sufficient to enumerate rows that must be refreshed.
	var persistedLocales []string
	if err := tx.WithContext(ctx).Table("email_template_translation").
		Where("entity_id = ?", templateID).
		Order("locale ASC").Pluck("locale", &persistedLocales).Error; err != nil {
		return err
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
			return emailTemplateContent.NormalizeContentBlockError(err)
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localized, nil, sitevariable.TemplateContext())
		if err != nil {
			return emailTemplateContent.NormalizeContentBlockError(err)
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
			return result.Error
		}
		if result.RowsAffected != 1 {
			return domainerr.Precondition("Email Template locale metadata is not initialized")
		}
	}
	return nil
}
