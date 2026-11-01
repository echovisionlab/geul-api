// Package publictest holds the in-memory public.Store the public Post read
// tests run on. It reads the Posts, locales and comments of a
// posttest.Store and keeps share links of its own; ordering, filters and
// map queries stay with the PostgreSQL reads.
package publictest

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/post/posttest"
	"github.com/echovisionlab/geul-api/internal/post/public"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ShareLink is a share link of a draft Post.
type ShareLink struct {
	Token, Password, PostID string
	ExpiresAt               *time.Time
}

// Store is the in-memory public.Store.
type Store struct {
	*posttest.Store

	mu    sync.Mutex
	links []ShareLink
}

var _ public.Store = (*Store)(nil)

// New returns a fake public Store over posts and its unit of work.
func New(posts *posttest.Store) (*Store, *mutationtest.UnitOfWork[public.Store]) {
	store := &Store{Store: posts}
	return store, mutationtest.New[public.Store](store)
}

// PutShareLink stores a share link.
func (s *Store) PutShareLink(link ShareLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links = append(s.links, link)
}

// ShareLink implements public.Store: a live link of the Post whose token
// and password match.
func (s *Store) ShareLink(_ context.Context, token, password, postID string) (*mediaasset.ContentDownloadShareLinkWitness, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, link := range s.links {
		if link.Token != token || link.Password != password || link.PostID != postID {
			continue
		}
		if link.ExpiresAt != nil && !link.ExpiresAt.After(time.Now()) {
			continue
		}
		return &mediaasset.ContentDownloadShareLinkWitness{ID: link.Token, EntityType: "post", EntityID: postID, ExpiresAt: link.ExpiresAt}, nil
	}
	return nil, public.ErrShareLinkNotFound
}

// ShareLockPost implements public.Store by ID or slug.
func (s *Store) ShareLockPost(ctx context.Context, slugOrID string, byID bool) (public.PublicPost, error) {
	if byID {
		stored, err := s.ReadPost(ctx, slugOrID)
		return public.PublicPost{Post: stored}, err
	}
	posts, _, err := s.Store.ListPosts(ctx, post.ListQuery{Limit: -1})
	if err != nil {
		return public.PublicPost{}, err
	}
	for _, stored := range posts {
		if stored.Slug != nil && *stored.Slug == slugOrID {
			return public.PublicPost{Post: stored}, nil
		}
	}
	return public.PublicPost{}, post.ErrPostNotFound
}

// ListPosts implements public.Store over the published Posts, latest
// published first.
func (s *Store) ListPosts(ctx context.Context, query public.ListQuery) ([]post.Post, int64, error) {
	posts, _, err := s.Store.ListPosts(ctx, post.ListQuery{Limit: -1})
	if err != nil {
		return nil, 0, err
	}
	var published []post.Post
	for _, stored := range posts {
		if stored.Status == post.StatusPublished {
			published = append(published, stored)
		}
	}
	total := int64(len(published))
	start := min(int(query.Offset), len(published))
	end := len(published)
	if query.Limit > 0 {
		end = min(start+int(query.Limit), end)
	}
	return published[start:end], total, nil
}

// ListMapFeatures implements public.Store; the fake places no Post on the
// map.
func (s *Store) ListMapFeatures(context.Context, public.MapQuery) ([]public.MapFeatureRow, error) {
	return nil, nil
}

// SearchPosts implements public.Store over published source titles.
func (s *Store) SearchPosts(ctx context.Context, pattern string, limit int) ([]post.Post, error) {
	posts, _, err := s.ListPosts(ctx, public.ListQuery{})
	if err != nil {
		return nil, err
	}
	var found []post.Post
	for _, stored := range posts {
		locale, ok := s.Locale(stored.ID, stored.SourceLocale)
		if ok && locale.Title != nil && strings.Contains(strings.ToLower(*locale.Title), strings.ToLower(pattern)) {
			found = append(found, stored)
		}
	}
	return found[:min(limit, len(found))], nil
}

// ResolveLocalization implements public.Store with the source locale row.
func (s *Store) ResolveLocalization(_ context.Context, postID, acceptLanguage string) (public.LocalizedContentSelection, error) {
	stored, ok := s.Post(postID)
	if !ok {
		return public.LocalizedContentSelection{}, post.ErrPostNotFound
	}
	selection := public.LocalizedContentSelection{
		RequestedLocale: acceptLanguage, DisplayedLocale: stored.SourceLocale, SourceLocale: stored.SourceLocale,
		AvailableLocales: []string{stored.SourceLocale}, IsOriginal: true,
	}
	if locale, ok := s.Locale(postID, stored.SourceLocale); ok {
		selection.Title, selection.Summary = locale.Title, locale.Summary
	}
	return selection, nil
}

// ResolveOgConsistency implements public.Store; the fake keeps selection.
func (s *Store) ResolveOgConsistency(_ context.Context, _ string, selection public.LocalizedContentSelection) (public.LocalizedContentSelection, error) {
	return selection, nil
}

// ReadyOgAsset implements public.Store; the fake has no ready asset.
func (s *Store) ReadyOgAsset(context.Context, ...*string) (*commonv1.AssetRef, error) {
	return nil, nil
}
