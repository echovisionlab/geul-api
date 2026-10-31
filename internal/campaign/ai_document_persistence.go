package campaign

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

type campaignAIDocumentRoot struct {
	ID                string
	Status            string
	ContentDocumentID *string `gorm:"column:content_document_id"`
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
		return campaignAIDocumentRoot{}, uuid.Nil, errs.WithCause(errs.InternalMsg("Campaign content document ID is invalid"), err)
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
		return uuid.Nil, errs.WithCause(errs.AuthenticationRequired(), err)
	}
	allowed, err := checker.Can(ctx, decision)
	if err != nil {
		return uuid.Nil, errs.WithCause(errs.DependencyUnavailable("SpiceDB"), err)
	}
	if !allowed {
		return uuid.Nil, errs.NoPermission("edit", "campaign")
	}
	return memberID, nil
}
