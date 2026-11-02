package post

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	"github.com/echovisionlab/geul-api/internal/mutation"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

// GetPostEditorDocumentQuery reads one locale of a Post for the browser
// editor.
type GetPostEditorDocumentQuery struct{ PostID, Locale string }

// EditorDocument is one locale of a Post as the browser editor opens it.
type EditorDocument struct {
	Document         *contentv1.LocalizedRichTextDocument
	DocumentRevision string
	Locale           string
	LocaleExists     bool
	TargetRevision   *string
	SourceLocale     string
	BlockMedia       []*fileref.BlockMediaItem
	Editable         bool
	Title            *string
	Summary          *string
}

// GetPostEditorDocument uses the same authorized projection as room bootstrap.
func (s *Service) GetPostEditorDocument(ctx context.Context, query GetPostEditorDocumentQuery) (_ EditorDocument, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.SessionID == "" {
		return EditorDocument{}, domainerr.AuthenticationRequired()
	}
	sessionID := string(user.SessionID)
	var editor EditorDocument
	err := s.units.ReadLocked(ctx, func(store Store) error {
		body, err := s.loadPostBlockDocument(ctx, func(postID string) (uuid.UUID, error) {
			return loadPostContentDocumentID(ctx, store, postID)
		}, store, LoadPostBlockDocumentQuery{
			PostID: query.PostID, Locale: query.Locale, SessionID: &sessionID,
		})
		if err != nil {
			return err
		}
		editable, err := s.postEditable(ctx, store, query.PostID)
		if err != nil {
			return err
		}
		metadata := body.LocaleMetadata
		if metadata == nil {
			metadata = &body.SourceMetadata
		}
		editor = EditorDocument{
			Document: body.Document, DocumentRevision: body.DocumentRevision, Locale: body.Locale,
			LocaleExists: body.LocaleExists, TargetRevision: body.TargetRevision, SourceLocale: body.SourceMetadata.Locale,
			BlockMedia: body.BlockMedia, Editable: editable,
			Title: metadata.Title, Summary: metadata.Summary,
		}
		return nil
	})
	if mutation.IsDenied(err) {
		return EditorDocument{}, domainerr.ResourceNotFound("post", query.PostID).WithCause(err)
	}
	return editor, err
}

// postEditable reports whether the caller may edit the Post: an archived
// Post only for the archived edit permission.
func (s *Service) postEditable(ctx context.Context, store Store, postID string) (bool, error) {
	status, err := store.PostStatus(ctx, postID)
	if err != nil {
		return false, err
	}
	if status != StatusArchived {
		return true, nil
	}
	_, err = requirePostActionForStatus(ctx, s.spiceDB, postID, status, policyv1.Post.Edit)
	if err != nil && !mutation.IsDenied(err) {
		return false, err
	}
	return err == nil, nil
}
