package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// LoadTemplateTranslationSourceDocument returns the Email Template typed
// source document and subject, read through store in the translation
// domain's transaction.
func LoadTemplateTranslationSourceDocument(ctx context.Context, store Store, templateID string) (*translation.SourceDocument, error) {
	return emailTemplateContent.LoadTranslationSourceDocument(ctx, store, templateID, loadTemplateSourceSubject)
}

// ApplyTemplateTranslationCandidate replaces one generated target in the
// translation delivery's transaction that joined binds.
func ApplyTemplateTranslationCandidate(
	ctx context.Context,
	joined Joined,
	job emailcontent.TranslationJob,
	candidate *translation.Candidate,
	input translation.EntryWrite,
) error {
	if candidate == nil || candidate.ContentBlockLocaleOverlay == nil {
		return errors.New("typed Email Template translation candidate is required")
	}
	if job.EntityType != emailTemplateContentEntity {
		return domainerr.InvalidField("entity_type", "Email Template translation entity is required")
	}
	return emailTemplateContent.ApplyTranslationCandidate(ctx, joined.Store, emailcontent.TranslationCandidate{
		Job: &job, Candidate: candidate, Input: input,
		Fence: emailTemplateTranslationJobApplyFence(joined.Store, job.EntityID),
	}, joined.Audit)
}

// projectEmailTemplateMaterializedContent stores the materialized content of
// each persisted locale of changedLocales, or of every locale when it is
// empty.
func projectEmailTemplateMaterializedContent(
	ctx context.Context,
	store Store,
	templateID string,
	snapshot contentblock.Snapshot,
	changedLocales []string,
	now time.Time,
) error {
	if snapshot.Document.Profile != emailContentProfile {
		return domainerr.Precondition("Email Template content document profile changed")
	}
	sourceLocale, err := loadTemplateSourceLocale(ctx, store, templateID, false)
	if err != nil {
		return err
	}
	if snapshot.SourceLocale != sourceLocale {
		return domainerr.Precondition("Email Template translation source locale changed; reload before saving")
	}
	persistedLocales, err := store.LocaleNames(ctx, templateID)
	if err != nil {
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
		update := emailcontent.LocaleProjection{HTML: projection.HTML, Text: projection.Text}
		if locale == sourceLocale {
			update.UpdatedAt = &now
		}
		projected, err := store.ProjectLocale(ctx, templateID, locale, update)
		if err != nil {
			return err
		}
		if !projected {
			return domainerr.Precondition("Email Template locale metadata is not initialized")
		}
	}
	return nil
}
