package campaign

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	campaignContentEntity = "campaign"
	emailContentProfile   = "email"
)

type campaignEmailContentRoot struct {
	ID                string         `gorm:"column:id"`
	ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
}

type campaignEmailLocaleMetadata struct {
	Locale  string  `gorm:"column:locale"`
	Subject *string `gorm:"column:subject"`
}

func requireCampaignContentEntity(entityType string) error {
	if entityType != campaignContentEntity {
		return fmt.Errorf("unsupported Campaign content entity type %q", entityType)
	}
	return nil
}

func loadCampaignEmailContentDocumentID(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (uuid.UUID, error) {
	return loadCampaignEmailContentDocumentRoot(ctx, db, entityType, entityID, false)
}

func loadCampaignEmailContentDocumentRoot(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	lock bool,
) (uuid.UUID, error) {
	if db == nil {
		return uuid.Nil, errs.Internal(errors.New("campaign content document database is required"))
	}
	if err := requireCampaignContentEntity(entityType); err != nil {
		return uuid.Nil, errs.Internal(err)
	}
	query := db.WithContext(ctx).
		Table("campaign").
		Select("id", "content_document_id").
		Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var root campaignEmailContentRoot
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uuid.Nil, errs.NotFound(campaignContentEntity, entityID)
		}
		return uuid.Nil, errs.Internal(err)
	}
	if !root.ContentDocumentID.Valid || strings.TrimSpace(root.ContentDocumentID.String) == "" {
		return uuid.Nil, errs.FailedPrecondition("Campaign content document is not initialized")
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return uuid.Nil, errs.Internal(fmt.Errorf("invalid Campaign content_document_id: %w", err))
	}
	return documentID, nil
}

func campaignEmailContentFence(entityType, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := requireCampaignContentEntity(entityType); err != nil {
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityType, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Campaign content document ownership changed; reload before saving")
		}
		var row struct {
			Status string `gorm:"column:status"`
		}
		if err := tx.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		if !campaignStatusAllowsEdit(row.Status) {
			return contentblock.DomainContext{}, errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
		}
		return lockCampaignEmailTranslationSource(ctx, tx, entityType, entityID)
	}
}

// campaignTranslationJobApplyFence permits an already-accepted TranslationJob
// to finish against any still-existing Campaign root. Current Campaign
// lifecycle controls new requests and interactive edits; it does not revoke a
// durable job whose request-time authorization was accepted.
func campaignTranslationJobApplyFence(entityType, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if err := requireCampaignContentEntity(entityType); err != nil {
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityType, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Campaign content document ownership changed; translation job cannot be applied")
		}
		return lockCampaignEmailTranslationSource(ctx, tx, entityType, entityID)
	}
}

func campaignEmailDeleteContentFence(entityType, entityID string) contentblock.DomainFence {
	return func(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		currentDocumentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, entityType, entityID, true)
		if err != nil {
			return contentblock.DomainContext{}, err
		}
		if currentDocumentID != documentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Campaign content document ownership changed; reload before saving")
		}
		var row struct {
			Status string `gorm:"column:status"`
		}
		if err := tx.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
			return contentblock.DomainContext{}, errs.Internal(err)
		}
		if row.Status != managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String() {
			return contentblock.DomainContext{}, errs.FailedPrecondition("only draft campaigns can be deleted")
		}
		return lockCampaignEmailTranslationSource(ctx, tx, entityType, entityID)
	}
}

func requireCampaignEmailDocumentLoad(
	ctx context.Context,
	db *gorm.DB,
	checker CollaborationPermissionChecker,
	principal *intrav1.CollaborationPrincipal,
	resourceType intrav1.CollaborationResourceType,
	resourceID string,
) error {
	if resourceType != intrav1.CollaborationResourceType_COLLABORATION_RESOURCE_TYPE_CAMPAIGN {
		return errs.InvalidArgument("resource.type", "must be Campaign")
	}
	if principal == nil || strings.TrimSpace(principal.GetSessionId()) == "" {
		return errs.AuthenticationRequired()
	}
	resolved, err := auth.ResolveAuthenticatedPrincipalBySessionID(ctx, db, principal.GetSessionId())
	if errors.Is(err, auth.ErrSessionPrincipalInvalid) {
		return errs.AuthenticationRequired()
	}
	if err != nil {
		return errs.Internal(fmt.Errorf("resolve Campaign collaboration principal: %w", err))
	}
	if resolved == nil || !resolved.Authenticated {
		return errs.AuthenticationRequired()
	}
	if resolved.Banned {
		return errs.AccountBanned()
	}
	if !resolved.Onboarded {
		return errs.NoPermission("edit", "campaign")
	}
	if err := requireCampaignEmailDocumentEditable(ctx, db, campaignContentEntity, resourceID); err != nil {
		return err
	}
	if checker == nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	can, err := campaignEditCan(resourceID)
	if err != nil {
		return errs.InvalidArgument("resource.id", "must be a canonical Campaign UUID")
	}
	authorizationCtx := auth.WithUser(ctx, resolved)
	decision, err := auth.AuthorizationDecision(authorizationCtx, can)
	if err != nil {
		return errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(authorizationCtx, decision)
	if err != nil {
		return errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return errs.NoPermission(can.Action().Name(), "campaign")
	}
	return nil
}

func requireCampaignEmailDocumentEditable(ctx context.Context, db *gorm.DB, entityType, entityID string) error {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return errs.Internal(err)
	}
	var row struct {
		Status string `gorm:"column:status"`
	}
	if err := db.WithContext(ctx).Table("campaign").Select("status").Where("id = ?", entityID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errs.NotFound("campaign", entityID)
		}
		return errs.Internal(err)
	}
	if !campaignStatusAllowsEdit(row.Status) {
		return errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
	}
	return nil
}

func lockCampaignEmailTranslationSource(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
) (contentblock.DomainContext, error) {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	return loadCampaignSourceContext(ctx, tx, entityID, true)
}

func loadCampaignEmailSourceContext(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) (contentblock.DomainContext, error) {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	return loadCampaignSourceContext(ctx, db, entityID, false)
}

func loadCampaignSourceContext(ctx context.Context, db *gorm.DB, entityID string, lock bool) (contentblock.DomainContext, error) {
	if db == nil {
		return contentblock.DomainContext{}, errs.Internal(errors.New("campaign source database is required"))
	}
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	query := db.WithContext(ctx).
		Table("campaign").
		Select("source_locale").
		Where("id = ?", entityID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return contentblock.DomainContext{}, errs.NotFound(campaignContentEntity, entityID)
		}
		return contentblock.DomainContext{}, errs.Internal(err)
	}
	sourceLocale := strings.TrimSpace(state.SourceLocale)
	if sourceLocale == "" {
		return contentblock.DomainContext{}, errs.FailedPrecondition("Campaign translation source is not initialized")
	}
	normalized, err := normalizeCampaignDocumentLocale(sourceLocale)
	if err != nil {
		return contentblock.DomainContext{}, errs.FailedPrecondition("Campaign translation source locale is invalid")
	}
	return contentblock.DomainContext{SourceLocale: normalized}, nil
}

func loadCampaignEmailSourceSubject(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
	sourceLocale string,
) (string, error) {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return "", errs.Internal(err)
	}
	var row struct {
		Subject sql.NullString `gorm:"column:subject"`
	}
	if err := db.WithContext(ctx).
		Table("campaign_translation").
		Select("subject").
		Where("entity_id = ? AND locale = ?", entityID, sourceLocale).
		Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", errs.FailedPrecondition("Campaign source locale metadata is not initialized")
		}
		return "", errs.Internal(err)
	}
	subject := strings.TrimSpace(row.Subject.String)
	if !row.Subject.Valid || subject == "" {
		return "", errs.FailedPrecondition("Campaign source locale subject is not initialized")
	}
	return subject, nil
}

func loadCampaignEmailLocaleMetadata(
	ctx context.Context,
	db *gorm.DB,
	entityType string,
	entityID string,
) ([]campaignEmailLocaleMetadata, string, error) {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return nil, "", errs.Internal(err)
	}
	domain, err := loadCampaignEmailSourceContext(ctx, db, entityType, entityID)
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
		return nil, "", errs.Internal(err)
	}
	return locales, domain.SourceLocale, nil
}

func updateCampaignEmailLocaleSubject(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	locale string,
	subject string,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	locale, err := normalizeCampaignDocumentLocale(locale)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	subject = strings.TrimSpace(subject)
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
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("target Campaign locale metadata is read-only")
	}
	var current struct {
		Subject sql.NullString `gorm:"column:subject"`
	}
	result := tx.WithContext(ctx).
		Table("campaign_translation").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("subject").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Take(&current)
	if result.Error != nil && !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return contentblock.MetadataEffect{}, errs.Internal(result.Error)
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
			return contentblock.MetadataEffect{}, errs.Internal(err)
		}
	} else if err := tx.WithContext(ctx).
		Table("campaign_translation").
		Where("entity_id = ? AND locale = ?", entityID, locale).
		Updates(map[string]any{
			"subject": subject, "updated_at": now,
		}).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	if err := tx.WithContext(ctx).
		Table("campaign").
		Where("id = ?", entityID).
		Updates(map[string]any{"subject": subject, "updated_at": now}).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{Changed: true, AffectsTranslationSource: true}, nil
}

func normalizeCampaignEmailContentBlockError(entityType string, err error) error {
	if err == nil || connect.CodeOf(err) != connect.CodeUnknown {
		return err
	}
	if entityType != campaignContentEntity {
		return errs.Internal(fmt.Errorf("unsupported Campaign content entity %q: %w", entityType, err))
	}
	switch {
	case errors.Is(err, contentblock.ErrDocumentNotFound):
		return errs.NotFoundMsg("campaign content document not found")
	case errors.Is(err, contentblock.ErrStaleRevision):
		return errs.FailedPrecondition("campaign content revision changed; reload before saving")
	case errors.Is(err, contentblock.ErrCrossDocument):
		return errs.InvalidArgument("blocks", "a Block belongs to another document")
	case errors.Is(err, contentblock.ErrFileReference):
		return errs.InvalidArgument("blocks", "Campaign documents cannot contain File Blocks")
	case errors.Is(err, contentblock.ErrInvalidMutation):
		return errs.InvalidArgument("blocks", err.Error())
	default:
		return errs.Internal(fmt.Errorf("campaign content document: %w", err))
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

func projectCampaignEmailMaterializedContent(
	ctx context.Context,
	tx *gorm.DB,
	entityType string,
	entityID string,
	snapshot contentblock.Snapshot,
	changedLocales []string,
	now time.Time,
) error {
	if err := requireCampaignContentEntity(entityType); err != nil {
		return errs.Internal(err)
	}
	if snapshot.Document.Profile != emailContentProfile {
		return errs.FailedPrecondition("Campaign content document profile changed")
	}
	domain, err := loadCampaignEmailSourceContext(ctx, tx, entityType, entityID)
	if err != nil {
		return err
	}
	if snapshot.SourceLocale != domain.SourceLocale {
		return errs.FailedPrecondition("Campaign translation source locale changed; reload before saving")
	}
	requested := make(map[string]struct{}, len(changedLocales))
	for _, locale := range changedLocales {
		if locale = strings.TrimSpace(locale); locale != "" {
			requested[locale] = struct{}{}
		}
	}
	if len(requested) == 0 {
		return errs.Internal(errors.New("campaign materialized projection requires an exact locale"))
	}
	locales := make([]string, 0, len(requested))
	for locale := range requested {
		locales = append(locales, locale)
	}
	sort.Strings(locales)
	for _, locale := range locales {
		document, err := contentblock.SnapshotToLocalizedRichTextDocument(snapshot, locale)
		if err != nil {
			return normalizeCampaignEmailContentBlockError(entityType, err)
		}
		projection, err := contentblock.MaterializeLocalizedRichTextDocument(ctx, document, nil)
		if err != nil {
			return normalizeCampaignEmailContentBlockError(entityType, err)
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
			return errs.Internal(result.Error)
		}
		if result.RowsAffected != 1 {
			return errs.FailedPrecondition("Campaign locale metadata is not initialized")
		}
	}
	return nil
}
