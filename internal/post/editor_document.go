package post

import (
	"context"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

// GetPostEditorDocument uses the same authorized projection as room bootstrap.
func (s *PostService) GetPostEditorDocument(ctx context.Context, req *connect.Request[managev1.GetPostEditorDocumentRequest]) (*connect.Response[managev1.GetPostEditorDocumentResponse], error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.SessionID == "" {
		return nil, errs.AuthenticationRequired()
	}
	if req.Msg == nil {
		return nil, errs.Required("request")
	}
	var response *managev1.GetPostEditorDocumentResponse
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		loader := &InternalPostService{db: tx, spiceDB: s.spiceDB, contentBlocks: s.contentBlocks, mediaLoader: s.mediaLoader}
		loaded, err := loader.LoadPostBlockDocument(ctx, connect.NewRequest(&intrav1.LoadPostBlockDocumentRequest{
			PostId: req.Msg.PostId, Locale: req.Msg.Locale, Principal: &intrav1.CollaborationPrincipal{SessionId: string(user.SessionID)},
		}))
		if err != nil {
			return err
		}
		var row struct{ Status string }
		if err := tx.Table("post").Select("status").Where("id = ?", req.Msg.PostId).Take(&row).Error; err != nil {
			return err
		}
		editable := true
		if row.Status == managev1.PostStatus_POST_STATUS_ARCHIVED.String() {
			_, editErr := requirePostActionForStatus(ctx, s.spiceDB, req.Msg.PostId, model.PostStatus(row.Status), policyv1.Post.Edit)
			if editErr != nil && connect.CodeOf(editErr) != connect.CodePermissionDenied {
				return editErr
			}
			editable = editErr == nil
		}
		body := loaded.Msg
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
	if err != nil {
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, errs.NotFound("post", req.Msg.PostId)
		}
		return nil, err
	}
	return connect.NewResponse(response), nil
}
