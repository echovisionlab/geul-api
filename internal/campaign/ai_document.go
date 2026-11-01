package campaign

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// AIDocumentService is Campaign's AI document seam: emailcontent owns the
// shared write and Campaign adds its authorization, draft lifecycle and
// contributor locks, while the transport adapter owns only DCDP conversion.
type AIDocumentService struct{ emailcontent.AIDocuments }

// AIDocuments returns the Campaign AI document use cases.
func (s *InternalService) AIDocuments() *AIDocumentService {
	return &AIDocumentService{emailcontent.AIDocuments{
		Owner: campaignContent, DB: s.db, Store: s.contentBlocks,
		Audit: s.audit, Authorize: s.authorizeAIDocument,
	}}
}

// authorizeAIDocument share-locks the Campaign for a load. For a mutation it
// locks the Campaign, proves the draft lifecycle and locks the editing
// Member as the contributor. Both check the one exact Edit decision.
func (s *InternalService) authorizeAIDocument(ctx context.Context, tx *gorm.DB, campaignID string, forUpdate bool) (uuid.UUID, uuid.UUID, error) {
	lock := clause.LockingStrengthShare
	if forUpdate {
		lock = clause.LockingStrengthUpdate
	}
	root, documentID, err := loadCampaignAIDocumentRoot(ctx, tx, campaignID, lock)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if forUpdate && !campaignStatusAllowsEdit(root.Status) {
		return uuid.Nil, uuid.Nil, domainerr.Precondition(msgCampaignCannotUpdateSent)
	}
	can, canErr := campaignEditCan(campaignID)
	memberID, err := emailcontent.RequireEditor(ctx, s.permissions, can, canErr, "campaign_id", "campaign")
	if err != nil || !forUpdate {
		return documentID, memberID, err
	}
	return documentID, memberID, requireCampaignContributors(ctx, tx, s.members, []string{memberID.String()})
}
