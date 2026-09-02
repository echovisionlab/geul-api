package emailauthoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

const (
	emailTemplateContentEntity = "email_template"
	emailContentProfile        = "email"
	emailLayoutContentProfile  = "compact"
)

type emailTemplateContentRoot struct {
	ID                string         `gorm:"column:id"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
	SourceLocale      string         `gorm:"column:source_locale"`
}

func loadCampaignEmailContentDocumentID(ctx context.Context, db *gorm.DB, entityType, entityID string) (uuid.UUID, error) {
	return loadCampaignEmailContentDocumentRoot(ctx, db, entityType, entityID, false)
}

func loadCampaignEmailContentDocumentRoot(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	forUpdate bool,
) (uuid.UUID, error) {
	if entityType != emailTemplateContentEntity {
		return uuid.Nil, errs.Internal(fmt.Errorf("unsupported Email Template content entity %q", entityType))
	}
	var root emailTemplateContentRoot
	query := db.WithContext(ctx).
		Table("email_template").
		Select("id", "content_document_id", "source_locale").
		Where("id = ?", entityID)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	result := query.Take(&root)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return uuid.Nil, errs.NotFound("email template", entityID)
	}
	if result.Error != nil {
		return uuid.Nil, errs.Internal(result.Error)
	}
	if !root.ContentDocumentID.Valid || strings.TrimSpace(root.ContentDocumentID.String) == "" {
		return uuid.Nil, errs.FailedPrecondition("content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, errs.Internal(fmt.Errorf("invalid email template content_document_id: %w", err))
	}
	return documentID, nil
}

func lockCampaignEmailTranslationSource(ctx context.Context, tx *gorm.DB, entityType, entityID string) (contentblock.DomainContext, error) {
	if entityType != emailTemplateContentEntity {
		return contentblock.DomainContext{}, errs.Internal(fmt.Errorf("unsupported Email Template content entity %q", entityType))
	}
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	result := tx.WithContext(ctx).Table("email_template").Clauses(clause.Locking{Strength: "UPDATE"}).Select("source_locale").Where("id = ?", entityID).Take(&state)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.DomainContext{}, errs.NotFound("email template", entityID)
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, errs.Internal(result.Error)
	}
	if strings.TrimSpace(state.SourceLocale) == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition("translation source locale is not initialized")
	}
	return contentblock.DomainContext{SourceLocale: state.SourceLocale}, nil
}

func loadCampaignEmailSourceContext(ctx context.Context, db *gorm.DB, entityType, entityID string) (contentblock.DomainContext, error) {
	if entityType != emailTemplateContentEntity {
		return contentblock.DomainContext{}, errs.Internal(fmt.Errorf("unsupported Email Template content entity %q", entityType))
	}
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	result := db.WithContext(ctx).Table("email_template").Select("source_locale").Where("id = ?", entityID).Take(&state)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.DomainContext{}, errs.NotFound("email template", entityID)
	}
	if result.Error != nil {
		return contentblock.DomainContext{}, errs.Internal(result.Error)
	}
	if strings.TrimSpace(state.SourceLocale) == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition("translation source locale is not initialized")
	}
	return contentblock.DomainContext{SourceLocale: state.SourceLocale}, nil
}

func loadCampaignEmailSourceSubject(ctx context.Context, db *gorm.DB, entityType, entityID, sourceLocale string) (string, error) {
	if entityType != emailTemplateContentEntity {
		return "", errs.Internal(fmt.Errorf("unsupported Email Template content entity %q", entityType))
	}
	var row struct{ Subject sql.NullString }
	result := db.WithContext(ctx).Table("email_template_translation").Select("subject").Where("entity_id = ? AND locale = ?", entityID, sourceLocale).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return "", errs.FailedPrecondition("source locale metadata is not initialized")
	}
	if result.Error != nil {
		return "", errs.Internal(result.Error)
	}
	if !row.Subject.Valid || strings.TrimSpace(row.Subject.String) == "" {
		return "", errs.FailedPrecondition("source locale subject is not initialized")
	}
	return strings.TrimSpace(row.Subject.String), nil
}

func updateCampaignEmailLocaleSubject(ctx context.Context, tx *gorm.DB, entityType, entityID, locale, subject string, now time.Time) (contentblock.MetadataEffect, error) {
	if entityType != emailTemplateContentEntity {
		return contentblock.MetadataEffect{}, errs.Internal(fmt.Errorf("unsupported Email Template content entity %q", entityType))
	}
	subject = strings.TrimSpace(subject)
	if strings.TrimSpace(locale) == "" {
		return contentblock.MetadataEffect{}, errs.Required("locale")
	}
	if subject == "" {
		return contentblock.MetadataEffect{}, errs.Required("subject")
	}
	if len(subject) > 500 {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("subject", "must be at most 500 characters")
	}
	domain, err := lockCampaignEmailTranslationSource(ctx, tx, entityType, entityID)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if locale != domain.SourceLocale {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("target email locale metadata is read-only")
	}
	current, exists, err := loadEmailTemplateExactLocaleMetadata(ctx, tx, entityID, locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if !exists {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("source locale metadata is not initialized")
	}
	if current.Subject.Valid && current.Subject.String == subject {
		return contentblock.MetadataEffect{}, nil
	}
	result := tx.WithContext(ctx).Table("email_template_translation").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Updates(map[string]any{"subject": subject, "updated_at": now})
	if result.Error != nil {
		return contentblock.MetadataEffect{}, errs.Internal(result.Error)
	}
	if result.RowsAffected != 1 {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("source locale metadata disappeared while saving")
	}
	return contentblock.MetadataEffect{
		Changed: true, AffectsTranslationSource: true, ChangedLocales: []string{locale},
	}, nil
}

func campaignEmailContentFence(
	references CampaignDeliveryReferences,
	entityType string,
	entityID string,
) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		current, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityType, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if current != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("content document ownership changed; reload before saving")
		}
		if err := ensureEmailTemplateMutableForActiveDelivery(ctx, tx, references, entityID); err != nil {
			return contentblock.DomainContext{}, err
		}
		return lockCampaignEmailTranslationSource(ctx, tx, entityType, entityID)
	}
}

// emailTemplateTranslationJobApplyFence permits an already-accepted
// TranslationJob to finish while the Email Template root still exists. Current
// publication and delivery lifecycle controls new authoring mutations, not the
// completion of a previously authorized job.
func emailTemplateTranslationJobApplyFence(entityType, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		current, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityType, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if current != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("content document ownership changed; translation job cannot be applied")
		}
		return lockCampaignEmailTranslationSource(ctx, tx, entityType, entityID)
	}
}

func campaignEmailDeleteContentFence(
	references CampaignDeliveryReferences,
	entityType string,
	entityID string,
) contentblock.DomainFence {
	return campaignEmailContentFence(references, entityType, entityID)
}

func normalizeCampaignEmailContentBlockError(entityType string, err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg(entityType + " content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.FailedPrecondition(entityType + " content revision changed; reload before saving")
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", "email documents cannot contain File Blocks")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("%s content document: %w", entityType, err))
	}
}

func asCampaignEmailConnectError(err error) error {
	if err == nil {
		return nil
	}
	var connectErr *connect.Error
	if errors.As(err, &connectErr) {
		return connectErr
	}
	return errs.Internal(err)
}
