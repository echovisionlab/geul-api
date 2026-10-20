package post

import (
	"context"
	"log/slog"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// PostService implements the PostService Connect handler
func (s *PostService) CheckSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckSlugAvailableRequest],
) (_ *connect.Response[managev1.CheckSlugAvailableResponse], returnErr error) {
	defer func() {
		if returnErr == nil {
			return
		}
		level := errs.OperationLogLevel(returnErr)
		slog.Log(ctx, level, "content operation failed", "domain", "post", "operation", "post.CheckSlugAvailable", "outcome", "failed", failurecause.Attr(returnErr))
	}()

	if _, err := s.authz.MustAuthenticate(ctx); err != nil {
		return nil, err
	}

	excludePostID := strings.TrimSpace(ptrStringValue(req.Msg.ExcludePostId))
	if err := s.authorizeSlugExclusion(ctx, excludePostID); err != nil {
		return nil, err
	}
	available, err := CheckSlugAvailable(ctx, s.db, req.Msg.Slug, excludePostID)
	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.CheckSlugAvailableResponse{
		Available: available,
	}), nil
}

func (s *PostService) authorizeSlugExclusion(ctx context.Context, postID string) error {
	if postID == "" {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var post model.Post
		err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).Where("id = ?", postID).First(&post).Error
		if err == gorm.ErrRecordNotFound {
			return errs.NotFoundMsg("post not found")
		}
		if err != nil {
			return errs.Internal(err)
		}
		_, err = requirePostActionForStatus(ctx, s.spiceDB, post.ID, post.Status, policyv1.Post.Edit)
		return err
	})
}

// ListMyPosts returns posts for the authenticated user
