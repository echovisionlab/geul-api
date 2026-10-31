package post

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// GetPostEditorDocumentQuery reads one locale of a Post for the browser
// editor.
type GetPostEditorDocumentQuery struct{ PostID, Locale string }

// GetPostEditorDocument uses the same authorized projection as room bootstrap.
func (s *Service) GetPostEditorDocument(ctx context.Context, query GetPostEditorDocumentQuery) (_ *managev1.GetPostEditorDocumentResponse, returnErr error) {
	defer failurecause.Annotate(&returnErr, slog.String("post_id", query.PostID))

	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.SessionID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	sessionID := string(user.SessionID)
	var response *managev1.GetPostEditorDocumentResponse
	err := mutation.ReadLocked(ctx, s.db, func(tx *gorm.DB) error {
		body, err := s.loadPostBlockDocumentWithDB(ctx, tx, tx, LoadPostBlockDocumentQuery{
			PostID: query.PostID, Locale: query.Locale, SessionID: &sessionID,
		})
		if err != nil {
			return err
		}
		editable, err := s.postEditable(ctx, tx, query.PostID)
		if err != nil {
			return err
		}
		metadata := body.LocaleMetadata
		if metadata == nil {
			metadata = body.SourceMetadata
		}
		response = &managev1.GetPostEditorDocumentResponse{
			Document: body.Document, DocumentRevision: body.DocumentRevision, Locale: body.Locale,
			LocaleExists: body.LocaleExists, TargetRevision: body.TargetRevision, SourceLocale: body.SourceMetadata.GetLocale(),
			BlockMedia: body.BlockMedia, Editable: editable,
			Title: metadata.Title, Summary: metadata.Summary,
		}
		return nil
	})
	if mutation.IsDenied(err) {
		return nil, domainerr.ResourceNotFound("post", query.PostID).WithCause(err)
	}
	return response, err
}

// postEditable reports whether the caller may edit the Post: an archived
// Post only for the archived edit permission.
func (s *Service) postEditable(ctx context.Context, tx *gorm.DB, postID string) (bool, error) {
	var row struct{ Status model.PostStatus }
	if err := tx.Table("post").Select("status").Where("id = ?", postID).Take(&row).Error; err != nil {
		return false, err
	}
	if row.Status != model.PostStatusArchived {
		return true, nil
	}
	_, err := requirePostActionForStatus(ctx, s.spiceDB, postID, row.Status, policyv1.Post.Edit)
	if err != nil && !mutation.IsDenied(err) {
		return false, err
	}
	return err == nil, nil
}
