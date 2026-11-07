package pagetest

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/page"
)

// AIDocumentBatchRows implements page.Store.
func (s *Store) AIDocumentBatchRows(ctx context.Context, pageIDs []string) (page.AIDocumentBatchRows, error) {
	if err := s.fail("AIDocumentBatchRows"); err != nil {
		return page.AIDocumentBatchRows{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := page.AIDocumentBatchRows{
		Pages: map[string]page.Page{}, Locales: map[string]map[string]page.Locale{}, Snapshots: map[uuid.UUID]contentblock.DeferredSnapshot{},
	}
	for _, pageID := range pageIDs {
		stored, found := s.pages[pageID]
		if !found {
			continue
		}
		rows.Pages[pageID] = stored
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
		if !slices.Contains(pageIDs, key.pageID) {
			continue
		}
		if rows.Locales[key.pageID] == nil {
			rows.Locales[key.pageID] = map[string]page.Locale{}
		}
		rows.Locales[key.pageID][key.locale] = row
	}
	return rows, nil
}
