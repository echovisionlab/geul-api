package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
			return campaignAIDocumentRoot{}, uuid.Nil, domainerr.ResourceNotFound("campaign", campaignID)
		}
		return campaignAIDocumentRoot{}, uuid.Nil, err
	}
	if root.ContentDocumentID == nil {
		return campaignAIDocumentRoot{}, uuid.Nil, domainerr.Precondition("Campaign content document is not initialized")
	}
	documentID, err := uuid.Parse(strings.TrimSpace(*root.ContentDocumentID))
	if err != nil || documentID == uuid.Nil {
		return campaignAIDocumentRoot{}, uuid.Nil, fmt.Errorf("Campaign content document ID %q is invalid", *root.ContentDocumentID)
	}
	return root, documentID, nil
}
