package posttest

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/post"
)

// AIDocumentBatchRows implements post.Store.
func (s *Store) AIDocumentBatchRows(ctx context.Context, postIDs []string) (post.AIDocumentBatchRows, error) {
	if err := s.fail("AIDocumentBatchRows"); err != nil {
		return post.AIDocumentBatchRows{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := post.AIDocumentBatchRows{
		Roots: map[string]post.AIDocumentBatchRoot{}, Locales: map[string]map[string]post.LocaleMetadata{},
		CategoryIDs: map[string][]string{}, TagIDs: map[string][]string{}, Snapshots: map[uuid.UUID]contentblock.DeferredSnapshot{},
	}
	for _, postID := range postIDs {
		stored, found := s.posts[postID]
		if !found {
			continue
		}
		rows.Roots[postID] = post.AIDocumentBatchRoot{
			AIDocumentRoot: post.AIDocumentRoot{ID: stored.ID, Status: stored.Status, ContentDocumentID: stored.ContentDocumentID},
			SourceLocale:   stored.SourceLocale,
		}
		rows.CategoryIDs[postID], rows.TagIDs[postID] = s.links[post.Categories][postID], s.links[post.Tags][postID]
		if stored.ContentDocumentID == nil {
			continue
		}
		if documentID, err := uuid.Parse(*stored.ContentDocumentID); err == nil {
			rows.Snapshots[documentID] = func() (contentblock.Snapshot, error) {
				return s.LockedContentSnapshot(ctx, documentID, stored.SourceLocale)
			}
		}
	}
	for key, row := range s.locales {
		if !slices.Contains(postIDs, key.postID) {
			continue
		}
		if rows.Locales[key.postID] == nil {
			rows.Locales[key.postID] = map[string]post.LocaleMetadata{}
		}
		rows.Locales[key.postID][key.locale] = row.LocaleMetadata
	}
	return rows, nil
}
