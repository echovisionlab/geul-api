package emailauthoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/sitevariable"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	emailTemplateContentEntity = "email_template"
	emailContentProfile        = "email"
	emailLayoutContentProfile  = "compact"
)

// emailTemplateContent is the Email Template owner of shared email content.
var emailTemplateContent = emailcontent.Owner{
	Name: "Email Template", Entity: emailTemplateContentEntity, IDField: "email_template_id",
	FileBlocksRejected:        "email documents cannot contain File Blocks",
	TranslatedSubjectRequired: "translated email subject is required",
	ContentEntityType:         managev1.ContentEntityType_CONTENT_ENTITY_TYPE_EMAIL_TEMPLATE,
	Rows:                      emailTemplateContentRows{},
	LocaleContentAudit: emailcontent.LocaleContentAudit{
		Action: sharedtelemetry.AuditEmailTemplateUpdated,
		Record: sharedtelemetry.NewEmailTemplateLocaleContentAuditRecord,
	},
}

// emailTemplateContentRows reads the Email Template rows beside its email
// content.
type emailTemplateContentRows struct{}

func (emailTemplateContentRows) DocumentID(ctx context.Context, db *gorm.DB, id string) (uuid.UUID, error) {
	return loadEmailTemplateContentDocument(ctx, db, id, false)
}

func (emailTemplateContentRows) SourceContext(ctx context.Context, db *gorm.DB, id string, lock bool) (contentblock.DomainContext, error) {
	return loadEmailTemplateSourceContext(ctx, db, id, lock)
}

// ProjectSource rebuilds the source locale's materialized content. A changed
// translation source can alter the fallback output of every target row, so
// each keeps its sparse values but has its projection refreshed, and the
// template's custom variables follow the new source HTML.
func (emailTemplateContentRows) ProjectSource(ctx context.Context, tx *gorm.DB, id string, snapshot contentblock.Snapshot, sourceChanged bool, now time.Time) error {
	if !sourceChanged {
		return projectEmailTemplateMaterializedContent(ctx, tx, id, snapshot, []string{snapshot.SourceLocale}, now)
	}
	if err := projectEmailTemplateMaterializedContent(ctx, tx, id, snapshot, nil, now); err != nil {
		return err
	}
	return syncSourceEmailTemplateVariables(ctx, tx, id, snapshot)
}

// syncSourceEmailTemplateVariables stores the custom variables the source
// HTML of snapshot names.
func syncSourceEmailTemplateVariables(ctx context.Context, tx *gorm.DB, id string, snapshot contentblock.Snapshot) error {
	localized, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, snapshot.SourceLocale)
	if err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, localized, nil, sitevariable.TemplateContext())
	if err != nil {
		return emailTemplateContent.NormalizeContentBlockError(err)
	}
	return syncCustomEmailTemplateVariables(ctx, tx, id, projection.HTML)
}

type emailTemplateContentRoot struct {
	ID                string         `gorm:"column:id"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
	SourceLocale      string         `gorm:"column:source_locale"`
}

// loadEmailTemplateContentDocument reads the template's content document,
// locking the template row when forUpdate is set.
func loadEmailTemplateContentDocument(ctx context.Context, db *gorm.DB, templateID string, forUpdate bool) (uuid.UUID, error) {
	var root emailTemplateContentRoot
	query := db.WithContext(ctx).
		Table("email_template").
		Select("id", "content_document_id", "source_locale").
		Where("id = ?", templateID)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	result := query.Take(&root)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return uuid.Nil, domainerr.ResourceNotFound("email template", templateID)
	}
	if result.Error != nil {
		return uuid.Nil, result.Error
	}
	if !root.ContentDocumentID.Valid || strings.TrimSpace(root.ContentDocumentID.String) == "" {
		return uuid.Nil, domainerr.Precondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid email template content_document_id: %w", err)
	}
	return documentID, nil
}

// loadEmailTemplateSourceContext reads the template's translation source
// locale, locking the template row when lock is set.
func loadEmailTemplateSourceContext(ctx context.Context, db *gorm.DB, templateID string, lock bool) (contentblock.DomainContext, error) {
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	query := db.WithContext(ctx).Table("email_template").Select("source_locale").Where("id = ?", templateID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	result := query.Take(&state)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.DomainContext{}, domainerr.ResourceNotFound("email template", templateID)
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, result.Error
	}
	if strings.TrimSpace(state.SourceLocale) == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("translation source locale is not initialized")
	}
	return contentblock.DomainContext{SourceLocale: state.SourceLocale}, nil
}

func loadEmailTemplateSourceSubject(ctx context.Context, db *gorm.DB, templateID, sourceLocale string) (string, error) {
	var row struct{ Subject sql.NullString }
	result := db.WithContext(ctx).Table("email_template_translation").Select("subject").Where("entity_id = ? AND locale = ?", templateID, sourceLocale).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", domainerr.Precondition("source locale metadata is not initialized")
	}
	if result.Error != nil {
		return "", result.Error
	}
	if !row.Subject.Valid || strings.TrimSpace(row.Subject.String) == "" {
		return "", domainerr.Precondition("source locale subject is not initialized")
	}
	return strings.TrimSpace(row.Subject.String), nil
}

// updateEmailTemplateSourceSubject stores subject as the source locale's
// subject; an equal subject changes nothing.
func updateEmailTemplateSourceSubject(ctx context.Context, tx *gorm.DB, templateID, locale, subject string, now time.Time) (contentblock.MetadataEffect, error) {
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
	domain, err := loadEmailTemplateSourceContext(ctx, tx, templateID, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if locale != domain.SourceLocale {
		return contentblock.MetadataEffect{}, domainerr.Precondition("target email locale metadata is read-only")
	}
	current, exists, err := emailTemplateContent.LoadLocaleMetadata(ctx, tx, templateID, locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if !exists {
		return contentblock.MetadataEffect{}, domainerr.Precondition("source locale metadata is not initialized")
	}
	if current.Subject.Valid && current.Subject.String == subject {
		return contentblock.MetadataEffect{}, nil
	}
	result := tx.WithContext(ctx).Table("email_template_translation").
		Where("entity_id = ? AND locale = ?", templateID, locale).
		Updates(map[string]any{"subject": subject, "updated_at": now})
	if result.Error != nil {
		return contentblock.MetadataEffect{}, result.Error
	}
	if result.RowsAffected != 1 {
		return contentblock.MetadataEffect{}, domainerr.Precondition("source locale metadata disappeared while saving")
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: true, ChangedLocales: []string{locale},
	}, nil
}

// emailTemplateContentFence admits a content write while the template still
// owns documentID and no active delivery renders it.
func emailTemplateContentFence(references CampaignDeliveryReferences, templateID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		current, err := loadEmailTemplateContentDocument(ctx, tx, templateID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if current != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("content document ownership changed; reload before saving")
		}
		if err := references.RequireTemplateMutable(ctx, tx, templateID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return loadEmailTemplateSourceContext(ctx, tx, templateID, true)
	}
}

// emailTemplateTranslationJobApplyFence permits an already-accepted
// TranslationJob to finish while the Email Template root still exists. Current
// publication and delivery lifecycle controls new authoring mutations, not the
// completion of a previously authorized job.
func emailTemplateTranslationJobApplyFence(templateID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		current, err := loadEmailTemplateContentDocument(ctx, tx, templateID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if current != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("content document ownership changed; translation job cannot be applied")
		}
		return loadEmailTemplateSourceContext(ctx, tx, templateID, true)
	}
}
