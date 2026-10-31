package post

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CheckSlugAvailableQuery asks whether Slug is free for a new Post, or for
// ExcludePostID's own Post.
type CheckSlugAvailableQuery struct {
	Slug          string
	ExcludePostID *string
}

func (s *Service) CheckSlugAvailable(ctx context.Context, query CheckSlugAvailableQuery) (*managev1.CheckSlugAvailableResponse, error) {
	if user := auth.GetUser(ctx); user == nil || !user.Authenticated || user.MemberID == "" {
		return nil, domainerr.AuthenticationRequired()
	}
	excludePostID := strings.TrimSpace(ptrStringValue(query.ExcludePostID))
	if err := s.authorizeSlugExclusion(ctx, excludePostID); err != nil {
		return nil, err
	}
	available, err := CheckSlugAvailable(ctx, s.db, query.Slug, excludePostID)
	if err != nil {
		return nil, err
	}
	return &managev1.CheckSlugAvailableResponse{Available: available}, nil
}

func (s *Service) authorizeSlugExclusion(ctx context.Context, postID string) error {
	if postID == "" {
		return nil
	}
	return s.readLocked(ctx, mutation.Checked(func(ctx context.Context, tx *gorm.DB) error {
		var post model.Post
		err := tx.Clauses(clause.Locking{Strength: "KEY SHARE"}).Where("id = ?", postID).First(&post).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.New(domainerr.NotFound, "", "post not found")
		}
		if err != nil {
			return err
		}
		_, err = requirePostActionForStatus(ctx, s.spiceDB, post.ID, post.Status, policyv1.Post.Edit)
		return err
	}), func(context.Context, *gorm.DB) error { return nil })
}
