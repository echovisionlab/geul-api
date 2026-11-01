package campaign

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/sitevariable"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	campaignContentEntity = "campaign"
	emailContentProfile   = "email"
)

// campaignContent is the Campaign owner of shared email content.
var campaignContent = emailcontent.Owner[contentStore]{
	Name: "Campaign", Entity: campaignContentEntity, IDField: "campaign_id",
	FileBlocksRejected:        "Campaign documents cannot contain File Blocks",
	TranslatedSubjectRequired: "translated Campaign subject is required",
	Rows:                      campaignContentRows{},
	LocaleContentAudit: emailcontent.LocaleContentAudit{
		Action: sharedtelemetry.AuditCampaignUpdated,
		Record: sharedtelemetry.NewCampaignLocaleContentAuditRecord,
	},
}

// campaignContentRows reads the Campaign rows beside its email content.
type campaignContentRows struct{}

func (campaignContentRows) DocumentID(ctx context.Context, store contentStore, id string) (uuid.UUID, error) {
	return loadCampaignEmailContentDocumentRoot(ctx, store.tx, id, false)
}

func (campaignContentRows) SourceContext(ctx context.Context, store contentStore, id string, lock bool) (contentblock.DomainContext, error) {
	return loadCampaignSourceContext(ctx, store.tx, id, lock)
}

// ProjectSource rebuilds the source locale's materialized content; a
// Campaign's targets keep theirs until they are written.
func (campaignContentRows) ProjectSource(ctx context.Context, store contentStore, id string, snapshot contentblock.Snapshot, _ bool, now time.Time) error {
	return projectCampaignEmailMaterializedContent(ctx, store.tx, id, snapshot, []string{snapshot.SourceLocale}, now)
}

type campaignEmailContentRoot struct {
	ID                string         `gorm:"column:id"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
}

type campaignEmailLocaleMetadata struct {
	Locale  string  `gorm:"column:locale"`
	Subject *string `gorm:"column:subject"`
}

// loadCampaignEmailContentDocumentRoot reads the Campaign's content
// document, locking the Campaign row when lock is set.
func loadCampaignEmailContentDocumentRoot(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	lock bool,
) (uuid.UUID, error) {
	query := db.WithContext(ctx).
		Table("campaign").
		Select("id", "content_document_id").
		Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var root campaignEmailContentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, domainerr.ResourceNotFound(campaignContentEntity, entityID)
		}
		return uuid.Nil, err
	}
	if !root.ContentDocumentID.Valid || strings.TrimSpace(root.ContentDocumentID.String) == "" {
		return uuid.Nil, domainerr.Precondition("Campaign content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid Campaign content_document_id: %w", err)
	}
	return documentID, nil
}

func campaignEmailContentFence(entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Campaign content document ownership changed; reload before saving")
		}
		var row struct {
			Status string `gorm:"column:status"`
		}
		if err := tx.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
			return contentblock.DomainContext{}, err
		}
		if !campaignStatusAllowsEdit(row.Status) {
			return contentblock.DomainContext{}, domainerr.Precondition(msgCampaignCannotUpdateSent)
		}
		return loadCampaignSourceContext(ctx, tx, entityID, true)
	}
}

// campaignTranslationJobApplyFence permits an already-accepted TranslationJob
// to finish against any still-existing Campaign root. Current Campaign
// lifecycle controls new requests and interactive edits; it does not revoke a
// durable job whose request-time authorization was accepted.
func campaignTranslationJobApplyFence(entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Campaign content document ownership changed; translation job cannot be applied")
		}
		return loadCampaignSourceContext(ctx, tx, entityID, true)
	}
}

func campaignEmailDeleteContentFence(entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, domainerr.Precondition("Campaign content document ownership changed; reload before saving")
		}
		var row struct {
			Status string `gorm:"column:status"`
		}
		if err := tx.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
			return contentblock.DomainContext{}, err
		}
		if model.CampaignStatus(row.Status) != model.CampaignStatusDraft {
			return contentblock.DomainContext{}, domainerr.Precondition("only draft campaigns can be deleted")
		}
		return loadCampaignSourceContext(ctx, tx, entityID, true)
	}
}

// requireCampaignEmailDocumentLoad authorizes the collaboration principal
// of a room load to edit the draft Campaign.
func requireCampaignEmailDocumentLoad(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	principal *intrav1.CollaborationPrincipal,
	resourceID string,
) error {
	if principal == nil || strings.TrimSpace(principal.GetSessionId()) == "" {
		return domainerr.AuthenticationRequired()
	}
	resolved, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, db, principal.GetSessionId())
	// The boundary keeps *_id request fields; this one is a session secret.
	err = failurecause.WithPrivate(err, principal.GetSessionId())
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	if err != nil {
		return fmt.Errorf("resolve Campaign collaboration principal: %w", err)
	}
	if resolved == nil || !resolved.Authenticated {
		return domainerr.AuthenticationRequired()
	}
	if resolved.Banned {
		return domainerr.AccountBanned()
	}
	if !resolved.Onboarded {
		return domainerr.NoPermission("edit", "campaign")
	}
	if err := requireCampaignEmailDocumentEditable(ctx, db, resourceID); err != nil {
		return err
	}
	can, err := campaignEditCan(resourceID)
	if err != nil {
		return domainerr.InvalidField("resource.id", "must be a canonical Campaign UUID")
	}
	authorizationCtx := auth.WithUser(ctx, resolved)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return domainerr.AuthenticationRequired().WithCause(err)
	}
	allowed, err := checker.Can(authorizationCtx, decision)
	if err != nil {
		return domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if !allowed {
		return domainerr.NoPermission(can.Action().Name(), "campaign")
	}
	return nil
}

func requireCampaignEmailDocumentEditable(ctx context.Context, db *gorm.DB, entityID string) error {
	var row struct {
		Status string `gorm:"column:status"`
	}
	if err := db.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("campaign", entityID)
		}
		return err
	}
	if !campaignStatusAllowsEdit(row.Status) {
		return domainerr.Precondition(msgCampaignCannotUpdateSent)
	}
	return nil
}

// loadCampaignSourceContext reads the Campaign's translation source locale,
// locking the Campaign row when lock is set.
func loadCampaignSourceContext(ctx context.Context, db *gorm.DB, entityID string, lock bool) (contentblock.DomainContext, error) {
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	query := db.WithContext(ctx).
		Table("campaign").
		Select("source_locale").
		Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	if err := query.Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, domainerr.ResourceNotFound(campaignContentEntity, entityID)
		}
		return contentblock.DomainContext{}, err
	}
	sourceLocale := strings.TrimSpace(state.SourceLocale)
	if sourceLocale == "" {
		return contentblock.DomainContext{}, domainerr.Precondition("Campaign translation source is not initialized")
	}
	normalized, err := emailcontent.NormalizeLocale(sourceLocale)
	if err != nil {
		return contentblock.DomainContext{}, domainerr.Precondition("Campaign translation source locale is invalid").WithCause(failurecause.ServerFault(err))
	}
	return contentblock.DomainContext{SourceLocale: normalized}, nil
}

func loadCampaignEmailSourceSubject(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
	sourceLocale string,
) (string, error) {
	var row struct {
		Subject sql.NullString `gorm:"column:subject"`
	}
	if err := db.WithContext(ctx).
		Table("campaign_translation").
		Select("subject").
		Where("entity_id = ? AND locale = ?", entityID, sourceLocale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", domainerr.Precondition("Campaign source locale metadata is not initialized")
		}
		return "", err
	}
	subject := strings.TrimSpace(row.Subject.String)
	if !row.Subject.Valid || subject == "" {
		return "", domainerr.Precondition("Campaign source locale subject is not initialized")
	}
	return subject, nil
}

func loadCampaignEmailLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	entityID string,
) ([]campaignEmailLocaleMetadata, string, error) {
	domain, err := loadCampaignSourceContext(ctx, db, entityID, false)
	if err != nil {
		return nil, "", err
	}
	var locales []campaignEmailLocaleMetadata
	if err := db.WithContext(ctx).
		Table("campaign_translation").
		Select("locale", "subject").
		Where("entity_id = ?", entityID).
		Order("locale ASC").
		Find(&locales).Error; err != nil {
		return nil, "", err
	}
	return locales, domain.SourceLocale, nil
}

func updateCampaignEmailLocaleSubject(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	locale string,
	subject string,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	locale, err := emailcontent.NormalizeLocale(locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return contentblock.MetadataEffect{}, domainerr.Required("subject")
	}
	if len(subject) > 500 {
		return contentblock.MetadataEffect{}, domainerr.InvalidField("subject", "must be at most 500 characters")
	}
	domain, err := loadCampaignSourceContext(ctx, tx, entityID, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if locale != domain.SourceLocale {
		return contentblock.MetadataEffect{}, domainerr.Precondition("target Campaign locale metadata is read-only")
	}
	var current struct {
		Subject sql.NullString `gorm:"column:subject"`
	}
	result := tx.WithContext(ctx).
		Table("campaign_translation").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("subject").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Take(&current)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.MetadataEffect{}, result.Error
	}
	if result.Error == nil && current.Subject.Valid && current.Subject.String == subject {
		return contentblock.MetadataEffect{}, nil
	}
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		if err := tx.WithContext(ctx).Exec(
			`INSERT INTO campaign_translation (
				entity_id, locale, subject, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?)`,
			entityID, locale, subject, now, now,
		).Error; err != nil {
			return contentblock.MetadataEffect{}, err
		}
	} else if err := tx.WithContext(ctx).
		Table("campaign_translation").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Updates(map[string]any{
			"subject": subject, "updated_at": now,
		}).Error; err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if err := tx.WithContext(ctx).
		Table("campaign").
		Where("id = ?", entityID).
		Updates(map[string]any{"subject": subject, "updated_at": now}).Error; err != nil {
		return contentblock.MetadataEffect{}, err
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func projectCampaignEmailMaterializedContent(
	ctx context.Context,
	tx *gorm.DB,
	entityID string,
	snapshot contentblock.Snapshot,
	changedLocales []string,
	now time.Time,
) error {
	if snapshot.Document.Profile != emailContentProfile {
		return domainerr.Precondition("Campaign content document profile changed")
	}
	domain, err := loadCampaignSourceContext(ctx, tx, entityID, false)
	if err != nil {
		return err
	}
	if snapshot.SourceLocale != domain.SourceLocale {
		return domainerr.Precondition("Campaign translation source locale changed; reload before saving")
	}
	requested := make(map[string]struct{}, len(changedLocales))
	for _, locale := range changedLocales {
		if locale = strings.TrimSpace(locale); locale != "" {
			requested[locale] = struct{}{}
		}
	}
	if len(requested) == 0 {
		return errors.New("campaign materialized projection requires an exact locale")
	}
	locales := make([]string, 0, len(requested))
	for locale := range requested {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
		if err != nil {
			return campaignContent.NormalizeContentBlockError(err)
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil, sitevariable.TemplateContext())
		if err != nil {
			return campaignContent.NormalizeContentBlockError(err)
		}
		updates := map[string]any{
			"content_html": projection.HTML,
			"content_text": projection.Text,
			"updated_at":   now,
		}
		result := tx.WithContext(ctx).
			Table("campaign_translation").
			Where("entity_id = ? AND locale = ?", entityID, locale).
			Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return domainerr.Precondition("Campaign locale metadata is not initialized")
		}
	}
	return nil
}
