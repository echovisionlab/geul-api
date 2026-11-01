package emailauthoring

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

// AIDocumentService is Email Template's AI document seam: emailcontent owns
// the shared write and Email Template adds its authorization, delivery
// lifecycle fence and contributor lock, while the transport adapter owns
// only DCDP conversion.
type AIDocumentService struct {
	emailcontent.AIDocuments[Store]
}

// TemplateAIDocuments returns the Email Template AI document use cases.
func (s *InternalService) TemplateAIDocuments() *AIDocumentService {
	return &AIDocumentService{emailcontent.AIDocuments[Store]{
		Owner: emailTemplateContent, Units: s.units, Mutations: s.mutations,
		Authorize: s.authorizeTemplateAIDocument,
	}}
}

// authorizeTemplateAIDocument reads the Email Template for a load. For a
// mutation it locks the template, rejects a template an active delivery
// still renders and locks the editing Member as the contributor. Both check
// the one exact Edit decision.
func (s *InternalService) authorizeTemplateAIDocument(ctx context.Context, store Store, templateID string, forUpdate bool) (uuid.UUID, uuid.UUID, error) {
	documentID, err := loadTemplateContentDocument(ctx, store, templateID, forUpdate)
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	if forUpdate {
		if err := store.RequireTemplateMutable(ctx, templateID); err != nil {
			return uuid.Nil, uuid.Nil, err
		}
	}
	can, canErr := emailTemplateEditCan(templateID)
	memberID, err := emailcontent.RequireEditor(ctx, s.spiceDB, can, canErr, "email_template_id", "email template")
	if err != nil || !forUpdate {
		return documentID, memberID, err
	}
	return documentID, memberID, lockAIDocumentContributor(ctx, store, memberID)
}
