package campaign

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

type campaignAIDocumentRoot struct {
	ID                string
	Status            string
	ContentDocumentID *string `gorm:"column:content_document_id"`
}

func campaignAuthorizedAIDocumentFence(
	expectedDocumentID uuid.UUID,
	domain contentblock.DomainContext,
) contentblock.DomainFence {
	return func(_ context.Context, _ *gorm.DB, documentID uuid.UUID) (contentblock.DomainContext, error) {
		if documentID != expectedDocumentID {
			return contentblock.DomainContext{}, errs.FailedPrecondition("Campaign content document changed; reload before saving")
		}
		return domain, nil
	}
}

func loadCampaignAIDocumentRoot(
	ctx context.Context,
	tx *gorm.DB,
	campaignID string,
	lock string,
) (campaignAIDocumentRoot, uuid.UUID, error) {
	var root campaignAIDocumentRoot
	query := tx.WithContext(ctx).Table("campaign").Where("id = ?", campaignID)
	if lock != "" {
		query = query.Clauses(clause.Locking{Strength: lock})
	}
	if err := query.Take(&root).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return campaignAIDocumentRoot{}, uuid.Nil, errs.NotFound("campaign", campaignID)
		}
		return campaignAIDocumentRoot{}, uuid.Nil, errs.Internal(err)
	}
	if root.ContentDocumentID == nil {
		return campaignAIDocumentRoot{}, uuid.Nil, errs.FailedPrecondition("Campaign content document is not initialized")
	}
	documentID, err := uuid.Parse(strings.TrimSpace(*root.ContentDocumentID))
	if err != nil || documentID == uuid.Nil {
		return campaignAIDocumentRoot{}, uuid.Nil, errs.InternalMsg("Campaign content document ID is invalid")
	}
	return root, documentID, nil
}

func requireCampaignAIDocumentAuthority(
	ctx context.Context,
	checker CollaborationPermissionChecker,
	campaignID string,
) (uuid.UUID, error) {
	principal := auth.GetUser(ctx)
	if principal == nil || !principal.Authenticated {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	if principal.Banned {
		return uuid.Nil, errs.AccountBanned()
	}
	if !principal.Onboarded {
		return uuid.Nil, errs.NoPermission("edit", "campaign")
	}
	memberID, err := uuid.Parse(principal.MemberID.String())
	if err != nil || memberID == uuid.Nil || memberID.String() != principal.MemberID.String() {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	can, err := campaignEditCan(campaignID)
	if err != nil {
		return uuid.Nil, errs.InvalidArgument("campaign_id", "must be a canonical UUID")
	}
	decision, err := auth.AuthorizationDecision(ctx, can)
	if err != nil {
		return uuid.Nil, errs.AuthenticationRequired()
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return uuid.Nil, errs.DependencyUnavailable("SpiceDB")
	}
	if !allowed {
		return uuid.Nil, errs.NoPermission("edit", "campaign")
	}
	return memberID, nil
}

func loadCampaignAIDocumentSubject(
	ctx context.Context,
	db *gorm.DB,
	campaignID string,
	locale string,
	forUpdate bool,
) (*string, bool, error) {
	var row struct{ Subject sql.NullString }
	query := db.WithContext(ctx).Table("campaign_translation").
		Select("subject").Where("entity_id = ? AND locale = ?", campaignID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	result := query.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if result.Error != nil {
		return nil, false, errs.Internal(result.Error)
	}
	if !row.Subject.Valid {
		return nil, true, nil
	}
	value := row.Subject.String
	return &value, true, nil
}

func applyCampaignAIDocumentSubject(
	ctx context.Context,
	tx *gorm.DB,
	mutation AIDocumentMutation,
	now time.Time,
) (contentblock.MetadataEffect, error) {
	current, exists, err := loadCampaignAIDocumentSubject(ctx, tx, mutation.CampaignID, mutation.Locale, true)
	if err != nil {
		return contentblock.MetadataEffect{}, err
	}
	if exists != mutation.ExpectedPresence {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Campaign locale presence changed; reload before saving")
	}
	if !exists && mutation.Locale == mutation.ExpectedSource {
		return contentblock.MetadataEffect{}, errs.FailedPrecondition("Campaign source locale is missing")
	}
	next := cloneCampaignAIDocumentString(current)
	if mutation.SetSubject {
		next = cloneCampaignAIDocumentString(&mutation.Subject)
	}
	if mutation.Locale == mutation.ExpectedSource && (next == nil || strings.TrimSpace(*next) == "") {
		return contentblock.MetadataEffect{}, errs.InvalidArgument("subject", "Campaign source subject cannot be empty")
	}
	ensureLocale := !exists && mutation.Locale != mutation.ExpectedSource
	changed := ensureLocale || !campaignAIDocumentStringEqual(current, next)
	if !changed {
		return contentblock.MetadataEffect{}, nil
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO campaign_translation (
			entity_id, locale, subject, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			subject = EXCLUDED.subject, updated_at = EXCLUDED.updated_at`,
		mutation.CampaignID, mutation.Locale, next, now, now,
	).Error; err != nil {
		return contentblock.MetadataEffect{}, errs.Internal(err)
	}
	return contentblock.MetadataEffect{
		Changed: changed, AffectsTranslationSource: mutation.Locale == mutation.ExpectedSource,
		SourceLocale: mutation.ExpectedSource, ChangedLocales: []string{mutation.Locale},
	}, nil
}

func canonicalCampaignAIDocumentID(field, value string) (string, error) {
	value = strings.TrimSpace(value)
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil || id.String() != value {
		return "", errs.InvalidArgument(field, "must be a canonical UUID")
	}
	return value, nil
}

func campaignAIDocumentStringEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
