package emailauthoring

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// AIDocumentService is Email Template's AI document seam: emailcontent owns
// the shared write and Email Template adds its authorization, delivery
// lifecycle fence and contributor lock, while the transport adapter owns
// only DCDP conversion.
type AIDocumentService struct{ emailcontent.AIDocuments }

func NewAIDocumentService(internal *InternalEmailTemplateService) (*AIDocumentService, error) {
	if internal == nil || internal.db == nil || internal.spiceDB == nil ||
		internal.contentBlocks == nil || internal.auditWriter == nil {
		return nil, errors.New("email template AI document dependencies are required")
	}
	return &AIDocumentService{emailcontent.AIDocuments{
		Owner: emailTemplateContent, DB: internal.db, Store: internal.contentBlocks,
		Audit: internal.auditWriter, Authorize: internal.authorizeAIDocument,
	}}, nil
}

// authorizeAIDocument reads the Email Template for a load. For a mutation it
// locks the template, rejects a template an active delivery still renders
// and locks the editing Member as the contributor. Both check the one exact
// Edit decision.
func (s *InternalEmailTemplateService) authorizeAIDocument(ctx context.Context, tx *gorm.DB, templateID string, forUpdate bool) (uuid.UUID, uuid.UUID, error) {
	documentID, err := loadCampaignEmailContentDocumentRoot(ctx, tx, emailTemplateContentEntity, templateID, forUpdate)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if forUpdate {
		if err := ensureEmailTemplateMutableForActiveDelivery(ctx, tx, s.references, templateID); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	memberID, err := requireEmailTemplateAIDocumentAuthority(ctx, s.spiceDB, templateID)
	if err != nil || !forUpdate {
		return documentID, memberID, err
	}
	return documentID, memberID, lockEmailAIDocumentContributor(ctx, tx, memberID)
}
