package emailauthoring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	emailTemplateContentEntity = "email_template"
	emailContentProfile        = "email"
	emailLayoutContentProfile  = "compact"
)

// emailTemplateContent is the Email Template owner of shared email content.
var emailTemplateContent = emailcontent.Owner[Store]{
	Name: "Email Template", Entity: emailTemplateContentEntity, IDField: "email_template_id",
	FileBlocksRejected:        "email documents cannot contain File Blocks",
	TranslatedSubjectRequired: "translated email subject is required",
	Rows:                      emailTemplateContentRows{},
	LocaleContentAudit: emailcontent.LocaleContentAudit{
		Action: sharedtelemetry.AuditEmailTemplateUpdated,
		Record: sharedtelemetry.NewEmailTemplateLocaleContentAuditRecord,
	},
}

// emailTemplateContentRows reads the Email Template rows beside its email
// content.
type emailTemplateContentRows struct{}

func (emailTemplateContentRows) DocumentID(ctx context.Context, store Store, id string) (uuid.UUID, error) {
	return loadTemplateContentDocument(ctx, store, id, false)
}

func (emailTemplateContentRows) SourceContext(ctx context.Context, store Store, id string, lock bool) (contentblock.DomainContext, error) {
	sourceLocale, err := loadTemplateSourceLocale(ctx, store, id, lock)
	return contentblock.DomainContext{SourceLocale: sourceLocale}, err
}

// ProjectSource rebuilds the source locale's materialized content; a change
// of the translation source rebuilds every locale and the custom variables.
func (emailTemplateContentRows) ProjectSource(ctx context.Context, store Store, id string, snapshot contentblock.Snapshot, sourceChanged bool, now time.Time) error {
	if !sourceChanged {
		return projectEmailTemplateMaterializedContent(ctx, store, id, snapshot, []string{snapshot.SourceLocale}, now)
	}
	if err := projectEmailTemplateMaterializedContent(ctx, store, id, snapshot, nil, now); err != nil {
		return err
	}
	return syncSourceEmailTemplateVariables(ctx, store, id, snapshot)
}

func syncSourceEmailTemplateVariables(ctx context.Context, store Store, id string, snapshot contentblock.Snapshot) error {
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, snapshot.SourceLocale)
	if err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localized, nil, sitevariable.TemplateContext())
	if err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	return store.SetCustomTemplateVariables(ctx, id, customEmailTemplateVariables(projection.HTML))
}

// loadTemplateContentDocument reads the template's content document,
// locking the template row when forUpdate is set.
func loadTemplateContentDocument(ctx context.Context, store Store, templateID string, forUpdate bool) (uuid.UUID, error) {
	documentID, err := store.TemplateDocument(ctx, templateID, forUpdate)
	if errors.Is(err, ErrTemplateNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound("email template", templateID)
	}
	if err != nil {
		return uuid.Nil, err
	}
	if documentID == nil || strings.TrimSpace(*documentID) == "" {
		return uuid.Nil, domainerr.Precondition("content document is not initialized")
	}
	parsed, err := uuid.Parse(*documentID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid email template content_document_id: %w", err)
	}
	return parsed, nil
}

// loadTemplateSourceLocale reads the template's translation source locale,
// locking the template row when lock is set.
func loadTemplateSourceLocale(ctx context.Context, store Store, templateID string, lock bool) (string, error) {
	sourceLocale, err := store.TemplateSourceLocale(ctx, templateID, lock)
	if errors.Is(err, ErrTemplateNotFound) {
		return "", domainerr.ResourceNotFound("email template", templateID)
	}
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(sourceLocale) == "" {
		return "", domainerr.Precondition("translation source locale is not initialized")
	}
	return sourceLocale, nil
}

func loadTemplateSourceSubject(ctx context.Context, store Store, templateID, sourceLocale string) (string, error) {
	row, exists, err := store.LocaleRow(ctx, templateID, sourceLocale, emailcontent.Unlocked)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", domainerr.Precondition("source locale metadata is not initialized")
	}
	if row.Subject == nil || strings.TrimSpace(*row.Subject) == "" {
		return "", domainerr.Precondition("source locale subject is not initialized")
	}
	return strings.TrimSpace(*row.Subject), nil
}

func updateEmailTemplateSourceSubject(ctx context.Context, store Store, templateID, locale, subject string, now time.Time) (contentblock.MetadataEffect, error) {
	subject = strings.TrimSpace(subject)
	if strings.TrimSpace(locale) == "" {
		return contentblock.MetadataEffect{}, domainerr.Required("locale")
	}
	if subject == "" {
		return contentblock.MetadataEffect{}, domainerr.Required("subject")
	}
	if len(subject) > 500 {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("subject", "must be at most 500 characters")
	}
	sourceLocale, err := loadTemplateSourceLocale(ctx, store, templateID, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if locale != sourceLocale {
		return contentblock.MetadataEffect{}, domainerr.Precondition("target email locale metadata is read-only")
	}
	current, exists, err := emailTemplateContent.LoadLocaleMetadata(ctx, store, templateID, locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if !exists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("source locale metadata is not initialized")
	}
	if current.Subject != nil && *current.Subject == subject {
		return contentblock.MetadataEffect{}, nil
	}
	updated, err := store.UpdateLocale(ctx, templateID, locale, &subject, now)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if !updated {
		return contentblock.MetadataEffect{}, domainerr.Precondition("source locale metadata disappeared while saving")
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: true, ChangedLocales: []string{locale},
	}, nil
}

// emailTemplateContentFence is the fence of an editor's or administrator's
// content write: the template keeps its document, and no active delivery
// renders it.
func emailTemplateContentFence(store Store, templateID string) emailcontent.Fence {
	return func(ctx context.Context, documentID uuid.UUID) (contentblock.DomainContext, error) {
		current, err := loadTemplateContentDocument(ctx, store, templateID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if current != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("content document ownership changed; reload before saving")
		}
		if err := store.RequireTemplateMutable(ctx, templateID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return emailTemplateContentRows{}.SourceContext(ctx, store, templateID, true)
	}
}
