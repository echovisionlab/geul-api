package post

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// CheckSlugAvailableQuery asks whether Slug is free for a new Post, or for
// ExcludePostID's own Post.
type CheckSlugAvailableQuery struct {
	Slug          string
	ExcludePostID *string
}

// CheckSlugAvailable reports whether the slug is unclaimed by both a Post
// and the shared Page route namespace.
func (s *Service) CheckSlugAvailable(ctx context.Context, query CheckSlugAvailableQuery) (bool, error) {
	if user := auth.GetUser(ctx); user == nil || !user.Authenticated || user.MemberID == "" {
		return false, domainerr.AuthenticationRequired()
	}
	excludePostID := strings.TrimSpace(ptrStringValue(query.ExcludePostID))
	if err := s.authorizeSlugExclusion(ctx, excludePostID); err != nil {
		return false, err
	}
	return readStore(ctx, s, func(store Store) (bool, error) { return checkSlugAvailable(ctx, store, query.Slug, excludePostID) })
}

// checkSlugAvailable reports whether slug is unclaimed by both a Post and the
// shared Page route namespace. excludePostID is the Post currently being
// edited and must already have passed authorization.
func checkSlugAvailable(ctx context.Context, store Store, slug, excludePostID string) (bool, error) {
	if strings.Contains(slug, "/") {
		return false, domainerr.InvalidField("slug", "must not contain '/'")
	}
	taken, err := store.SlugTaken(ctx, slug, excludePostID)
	if err != nil {
		return false, err
	}
	if taken {
		return false, nil
	}
	return store.RouteAvailable(ctx, slug)
}

func (s *Service) authorizeSlugExclusion(ctx context.Context, postID string) error {
	if postID == "" {
		return nil
	}
	return s.readLocked(ctx, mutation.Checked(func(ctx context.Context, store Store) error {
		stored, err := store.KeyShareLockPost(ctx, postID)
		if errors.Is(err, ErrPostNotFound) {
			return domainerr.New(domainerr.NotFound, "", "post not found")
		}
		if err != nil {
			return err
		}
		_, err = requirePostActionForStatus(ctx, s.spiceDB, stored.ID, stored.Status, policyv1.Post.Edit)
		return err
	}), func(context.Context, Store) error { return nil })
}
