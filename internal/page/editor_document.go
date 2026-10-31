package page

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type GetPageEditorDocumentQuery struct {
	PageID string
	Locale string
}

// GetPageEditorDocument reads the editor document of the caller's session
// through the collaboration room bootstrap, which authorizes and
// materializes it.
func (s *Service) GetPageEditorDocument(ctx context.Context, query GetPageEditorDocumentQuery) (_ *managev1.GetPageEditorDocumentResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.PageID))

	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.SessionID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	loaded, err := s.LoadPageBlockDocument(ctx, LoadPageBlockDocumentQuery{
		PageID: query.PageID, Locale: query.Locale, SessionID: string(user.SessionID),
	})
	if err != nil {
		if mutation.IsDenied(err) {
			return nil, domainerr.ResourceNotFound("page", query.PageID)
		}
		return nil, err
	}
	metadata := loaded.LocaleMetadata
	if metadata == nil {
		metadata = loaded.SourceMetadata
	}
	return &managev1.GetPageEditorDocumentResponse{
		Document: loaded.Document, DocumentRevision: loaded.DocumentRevision, Locale: loaded.Locale,
		LocaleExists: loaded.LocaleExists, TargetRevision: loaded.TargetRevision, SourceLocale: loaded.SourceMetadata.GetLocale(),
		BlockMedia: loaded.BlockMedia, Editable: true, DocumentLayout: loaded.DocumentLayout,
		Title: metadata.Title, Summary: metadata.Summary,
	}, nil
}
