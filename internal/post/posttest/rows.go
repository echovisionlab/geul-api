package posttest

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/post"
)

func (s *Store) FindPost(_ context.Context, postID string) (post.Post, error) {
	if err := s.fail("FindPost"); err != nil {
		return post.Post{}, err
	}
	stored, err := s.lookup(postID)
	if err != nil {
		return post.Post{}, err
	}
	return s.withRelations(stored), nil
}

func (s *Store) ReadPost(_ context.Context, postID string) (post.Post, error) {
	return s.lookup(postID)
}
func (s *Store) TakePost(_ context.Context, postID string) (post.Post, error) {
	return s.lookup(postID)
}

func (s *Store) FindPostID(_ context.Context, postID string) error {
	_, err := s.lookup(postID)
	return err
}

func (s *Store) status(postID string) (post.Status, error) {
	stored, err := s.lookup(postID)
	return stored.Status, err
}

func (s *Store) PostStatus(_ context.Context, postID string) (post.Status, error) {
	return s.status(postID)
}

func (s *Store) LockPostStatus(_ context.Context, postID string) (post.Status, error) {
	if err := s.fail("LockPostStatus"); err != nil {
		return "", err
	}
	return s.status(postID)
}

func (s *Store) LockParticipantRoot(_ context.Context, postID string) (post.Status, error) {
	return s.status(postID)
}

func (s *Store) KeySharePostStatus(_ context.Context, postID string) (post.Status, error) {
	return s.status(postID)
}

func (s *Store) KeyShareLockPost(_ context.Context, postID string) (post.Post, error) {
	return s.lookup(postID)
}

func (s *Store) LockPostRoot(_ context.Context, postID string) error {
	_, err := s.lookup(postID)
	return err
}

func documentRoot(stored post.Post) post.DocumentRoot {
	root := post.DocumentRoot{ID: stored.ID, Status: stored.Status, SourceLocale: stored.SourceLocale}
	if stored.ContentDocumentID != nil {
		if documentID, err := uuid.Parse(*stored.ContentDocumentID); err == nil {
			root.ContentDocumentID = &documentID
		}
	}
	return root
}

func (s *Store) DocumentRoot(_ context.Context, postID string) (post.DocumentRoot, error) {
	stored, err := s.lookup(postID)
	return documentRoot(stored), err
}

func (s *Store) LockDocumentRoot(_ context.Context, postID string) (post.DocumentRoot, error) {
	stored, err := s.lookup(postID)
	return documentRoot(stored), err
}

func (s *Store) AIDocumentRoot(_ context.Context, postID string, _ post.RowLock) (post.AIDocumentRoot, error) {
	stored, err := s.lookup(postID)
	if err != nil {
		return post.AIDocumentRoot{}, err
	}
	return post.AIDocumentRoot{ID: stored.ID, Status: stored.Status, ContentDocumentID: stored.ContentDocumentID}, nil
}

func (s *Store) SourceLocale(_ context.Context, postID string) (string, error) {
	stored, err := s.lookup(postID)
	return stored.SourceLocale, err
}

func (s *Store) SwitchSourceLocale(_ context.Context, postID, from, to string, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.posts[postID]
	if !ok || stored.SourceLocale != from {
		return 0, nil
	}
	stored.SourceLocale, stored.UpdatedAt = to, now
	s.posts[postID] = stored
	return 1, nil
}

// visible reports whether scope lets a principal view stored.
func visible(stored post.Post, scope post.ViewScope) bool {
	if stored.Status == post.StatusArchived {
		return slices.Contains(scope.ArchivedIDs, stored.ID)
	}
	return slices.Contains(scope.ViewIDs, stored.ID)
}

// ListPosts implements post.Store over the scope in the default order,
// latest published first, then newest; the fake leaves filters and sorts to
// the SQL.
func (s *Store) ListPosts(_ context.Context, query post.ListQuery) ([]post.Post, int64, error) {
	if err := s.fail("ListPosts"); err != nil {
		return nil, 0, err
	}
	s.mu.Lock()
	var matched []post.Post
	for _, stored := range s.posts {
		if query.Scope == nil || visible(stored, *query.Scope) {
			matched = append(matched, stored)
		}
	}
	s.mu.Unlock()
	sort.Slice(matched, func(i, j int) bool {
		left, right := matched[i].PublishedAt, matched[j].PublishedAt
		switch {
		case (left == nil) != (right == nil):
			return left != nil
		case left != nil && !left.Equal(*right):
			return left.After(*right)
		case !matched[i].CreatedAt.Equal(matched[j].CreatedAt):
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		default:
			return matched[i].ID < matched[j].ID
		}
	})
	total := int64(len(matched))
	matched = page(matched, int(query.Offset), int(query.Limit))
	for i := range matched {
		matched[i] = s.withRelations(matched[i])
	}
	return matched, total, nil
}

func page[T any](values []T, offset, limit int) []T {
	if offset >= len(values) {
		return nil
	}
	values = values[offset:]
	if limit >= 0 && limit < len(values) {
		values = values[:limit]
	}
	return values
}

func (s *Store) ListAIDocuments(_ context.Context, query post.AIDocumentQuery) ([]post.AIDocumentListItem, int64, error) {
	s.mu.Lock()
	var items []post.AIDocumentListItem
	for _, stored := range s.posts {
		if !visible(stored, query.Scope) {
			continue
		}
		title := ""
		if row, ok := s.locales[localeKey{stored.ID, stored.SourceLocale}]; ok && row.Title != nil {
			title = *row.Title
		}
		if query.Search != "" && !strings.Contains(strings.ToLower(title), strings.ToLower(query.Search)) {
			continue
		}
		items = append(items, post.AIDocumentListItem{
			ID: stored.ID, Title: title, Slug: stored.Slug, SourceLocale: stored.SourceLocale,
			Status: string(stored.Status), UpdatedAt: stored.UpdatedAt, ConfigurationRevision: stored.ConfigurationRevision,
		})
	}
	s.mu.Unlock()
	sort.Slice(items, func(i, j int) bool {
		if !items[i].UpdatedAt.Equal(items[j].UpdatedAt) {
			return items[i].UpdatedAt.After(items[j].UpdatedAt)
		}
		return items[i].ID < items[j].ID
	})
	return page(items, query.Offset, query.Limit), int64(len(items)), nil
}

func (s *Store) InsertPost(_ context.Context, stored *post.Post) error {
	if err := s.fail("InsertPost"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored.Slug != nil {
		for _, other := range s.posts {
			if other.Slug != nil && *other.Slug == *stored.Slug {
				return post.ErrSlugTaken
			}
		}
	}
	stored.ID = uuid.NewString()
	stored.ConfigurationRevision = uuid.NewString()
	stored.CreatedAt = s.now().UTC().Truncate(time.Microsecond)
	stored.UpdatedAt = stored.CreatedAt
	saved := *stored
	saved.Title, saved.Summary, saved.Categories, saved.Tags = "", nil, nil, nil
	s.posts[stored.ID] = saved
	return nil
}

func (s *Store) UpdateSettings(_ context.Context, stored *post.Post, change post.SettingsChange) error {
	if err := s.fail("UpdateSettings"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated, ok := s.posts[stored.ID]
	if !ok {
		return nil
	}
	before := updated
	if change.Slug != nil {
		updated.Slug = change.Slug.Value
	}
	if change.CommentsEnabled != nil {
		updated.CommentsEnabled = *change.CommentsEnabled
	}
	if change.MapPlaceID != nil {
		updated.MapPlaceID = change.MapPlaceID.Value
	}
	if change.DocumentLayout != nil {
		updated.DocumentLayout = *change.DocumentLayout
	}
	if !optionalEqual(before.Slug, updated.Slug) || before.CommentsEnabled != updated.CommentsEnabled ||
		!optionalEqual(before.MapPlaceID, updated.MapPlaceID) || before.DocumentLayout != updated.DocumentLayout {
		updated.ConfigurationRevision = uuid.NewString()
	}
	updated.UpdatedAt = change.UpdatedAt
	s.posts[stored.ID] = updated
	updated.Title, updated.Summary, updated.SourceLocaleOgAssetID = stored.Title, stored.Summary, stored.SourceLocaleOgAssetID
	*stored = updated
	return nil
}

func optionalEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (s *Store) UpdateLifecycle(_ context.Context, postID string, change post.LifecycleChange) error {
	if err := s.fail("UpdateLifecycle"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.posts[postID]
	if !ok {
		return nil
	}
	stored.Status, stored.UpdatedAt = change.Status, change.UpdatedAt
	if change.ClearSchedule {
		stored.ScheduledAt, stored.ScheduledTimeZone = nil, nil
	}
	if change.ScheduledAt != nil {
		scheduledAt := *change.ScheduledAt
		stored.ScheduledAt = &scheduledAt
	}
	if change.ScheduledTimeZone != nil {
		stored.ScheduledTimeZone = text(*change.ScheduledTimeZone)
	}
	if change.PublishedAt != nil {
		publishedAt := *change.PublishedAt
		stored.PublishedAt = &publishedAt
	}
	s.posts[postID] = stored
	return nil
}

func (s *Store) FeaturedImageFileID(_ context.Context, postID string) (*string, error) {
	stored, err := s.lookup(postID)
	return stored.FeaturedImageFileID, err
}

func (s *Store) SetFeaturedImage(_ context.Context, postID string, fileID *string, now time.Time) error {
	if err := s.fail("SetFeaturedImage"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.posts[postID]; ok {
		stored.FeaturedImageFileID, stored.UpdatedAt = fileID, now
		s.posts[postID] = stored
	}
	return nil
}

func (s *Store) TouchPost(_ context.Context, postID string, now time.Time) error {
	if err := s.fail("TouchPost"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if stored, ok := s.posts[postID]; ok {
		stored.UpdatedAt = now
		s.posts[postID] = stored
	}
	return nil
}

func (s *Store) DeletePost(_ context.Context, postID string) error {
	if err := s.fail("DeletePost"); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.posts, postID)
	for key := range s.locales {
		if key.postID == postID {
			delete(s.locales, key)
		}
	}
	for key := range s.relations {
		if key.postID == postID {
			delete(s.relations, key)
		}
	}
	for _, links := range s.links {
		delete(links, postID)
	}
	for id, comment := range s.comments {
		if comment.PostID == postID {
			delete(s.comments, id)
		}
	}
	delete(s.versions, postID)
	return nil
}

func (s *Store) DatabaseNow(context.Context) (time.Time, error) {
	if err := s.fail("DatabaseNow"); err != nil {
		return time.Time{}, err
	}
	return s.now(), nil
}

func (s *Store) LockDuePosts(_ context.Context, limit int) ([]post.Post, error) {
	now := s.now()
	s.mu.Lock()
	var due []post.Post
	for _, stored := range s.posts {
		if stored.Status == post.StatusScheduled && stored.ScheduledAt != nil && !stored.ScheduledAt.After(now) {
			due = append(due, stored)
		}
	}
	s.mu.Unlock()
	sort.Slice(due, func(i, j int) bool {
		if !due[i].ScheduledAt.Equal(*due[j].ScheduledAt) {
			return due[i].ScheduledAt.Before(*due[j].ScheduledAt)
		}
		return due[i].ID < due[j].ID
	})
	return page(due, 0, limit), nil
}

func (s *Store) PublishDuePost(_ context.Context, postID string, publishedAt *time.Time, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.posts[postID]
	if !ok || stored.Status != post.StatusScheduled || stored.ScheduledAt == nil || stored.ScheduledAt.After(now) {
		return false, nil
	}
	stored.Status, stored.ScheduledAt, stored.ScheduledTimeZone, stored.UpdatedAt = post.StatusPublished, nil, nil, now
	if publishedAt != nil {
		stored.PublishedAt = publishedAt
	}
	s.posts[postID] = stored
	return true, nil
}

func (s *Store) SlugTaken(_ context.Context, slug, excludeID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, stored := range s.posts {
		if stored.ID != excludeID && stored.Slug != nil && *stored.Slug == slug {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) RouteAvailable(_ context.Context, slug string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.routes[slug], nil
}

func (s *Store) CheckRoute(ctx context.Context, slug string) error {
	available, _ := s.RouteAvailable(ctx, slug)
	if !available {
		return routeTaken(slug)
	}
	return nil
}

func (s *Store) LockRoute(ctx context.Context, slug string) error {
	if err := s.fail("LockRoute"); err != nil {
		return err
	}
	return s.CheckRoute(ctx, slug)
}

func (s *Store) MapPlaceExists(_ context.Context, mapPlaceID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mapPlaces[mapPlaceID], nil
}
