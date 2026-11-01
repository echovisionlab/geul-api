package emailauthoring

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AIDocumentService is Email Template's AI document seam: emailcontent owns
// the shared write and Email Template adds its authorization, delivery
// lifecycle fence and contributor lock, while the transport adapter owns
// only DCDP conversion.
type AIDocumentService struct{ emailcontent.AIDocuments }

// TemplateAIDocuments returns the Email Template AI document use cases.
func (s *InternalService) TemplateAIDocuments() *AIDocumentService {
	return &AIDocumentService{emailcontent.AIDocuments{
		Owner: emailTemplateContent, DB: s.db, Store: s.contentBlocks,
		Mutations: s.mutations, Authorize: s.authorizeTemplateAIDocument,
	}}
}

// authorizeTemplateAIDocument reads the Email Template for a load. For a
// mutation it locks the template, rejects a template an active delivery
// still renders and locks the editing Member as the contributor. Both check
// the one exact Edit decision.
func (s *InternalService) authorizeTemplateAIDocument(ctx context.Context, tx *gorm.DB, templateID string, forUpdate bool) (uuid.UUID, uuid.UUID, error) {
	documentID, err := loadEmailTemplateContentDocument(ctx, tx, templateID, forUpdate)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if forUpdate {
		if err := s.references.RequireTemplateMutable(ctx, tx, templateID); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	can, canErr := emailTemplateEditCan(templateID)
	memberID, err := emailcontent.RequireEditor(ctx, s.spiceDB, can, canErr, "email_template_id", "email template")
	if err != nil || !forUpdate {
		return documentID, memberID, err
	}
	return documentID, memberID, s.lockAIDocumentContributor(ctx, tx, memberID)
}
