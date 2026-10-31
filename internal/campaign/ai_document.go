package campaign

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
	errs "github.com/echovisionlab/geul-api/internal/errors"
)

// AIDocumentService is Campaign's AI document seam: emailcontent owns the
// shared write and Campaign adds its authorization, draft lifecycle and
// contributor locks, while the transport adapter owns only DCDP conversion.
type AIDocumentService struct{ emailcontent.AIDocuments }

func NewAIDocumentService(internal *InternalCampaignService) (*AIDocumentService, error) {
	if internal == nil || internal.db == nil || internal.spiceDB == nil ||
		internal.contentBlocks == nil || internal.auditWriter == nil {
		return nil, errors.New("campaign AI document dependencies are required")
	}
	return &AIDocumentService{emailcontent.AIDocuments{
		Owner: campaignContent, DB: internal.db, Store: internal.contentBlocks,
		Audit: internal.auditWriter, Authorize: internal.authorizeAIDocument,
	}}, nil
}

// authorizeAIDocument share-locks the Campaign for a load. For a mutation it
// locks the Campaign, proves the draft lifecycle and locks the editing
// Member as the contributor. Both check the one exact Edit decision.
func (s *InternalCampaignService) authorizeAIDocument(ctx context.Context, tx *gorm.DB, campaignID string, forUpdate bool) (uuid.UUID, uuid.UUID, error) {
	lock := clause.LockingStrengthShare
	if forUpdate {
		lock = clause.LockingStrengthUpdate
	}
	root, documentID, err := loadCampaignAIDocumentRoot(ctx, tx, campaignID, lock)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if forUpdate && !campaignStatusAllowsEdit(root.Status) {
		return uuid.Nil, uuid.Nil, errs.FailedPrecondition(errs.MsgCampaignCannotUpdateSent)
	}
	memberID, err := requireCampaignAIDocumentAuthority(ctx, s.spiceDB, campaignID)
	if err != nil || !forUpdate {
		return documentID, memberID, err
	}
	return documentID, memberID, requireCampaignContributors(ctx, tx, s.contributors, []string{memberID.String()})
}
