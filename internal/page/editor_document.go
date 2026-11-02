package page

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

type GetPageEditorDocumentQuery struct {
	PageID string
	Locale string
}

// EditorDocument is the editor document of the caller's session: its room
// document and the title and summary of the room locale, or of the source
// locale while the room locale has none.
type EditorDocument struct {
	Room    RoomDocument
	Title   *string
	Summary *string
}

// GetPageEditorDocument reads the editor document of the caller's session
// through the collaboration room bootstrap, which authorizes and
// materializes it.
func (s *Service) GetPageEditorDocument(ctx context.Context, query GetPageEditorDocumentQuery) (_ EditorDocument, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("page_id", query.PageID))

	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.SessionID == "" {
		return EditorDocument{}, domainerr.AuthenticationRequired()
	}
	room, err := s.LoadPageBlockDocument(ctx, LoadPageBlockDocumentQuery{
		PageID: query.PageID, Locale: query.Locale, SessionID: string(user.SessionID),
	})
	if err != nil {
		if mutation.IsDenied(err) {
			return EditorDocument{}, domainerr.ResourceNotFound("page", query.PageID)
		}
		return EditorDocument{}, err
	}
	metadata := room.LocaleMetadata
	if metadata == nil {
		metadata = &room.SourceMetadata
	}
	return EditorDocument{Room: room, Title: metadata.Title, Summary: metadata.Summary}, nil
}
