package worktest

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/work"
)

// AIDocumentBatchRows implements work.Store.
func (s *Store) AIDocumentBatchRows(ctx context.Context, workIDs []string) (work.AIDocumentBatchRows, error) {
	if err := s.fail("AIDocumentBatchRows"); err != nil {
		return work.AIDocumentBatchRows{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := work.AIDocumentBatchRows{
		Works: map[string]work.Work{}, Locales: map[string]map[string]work.LocaleMetadata{}, Snapshots: map[uuid.UUID]contentblock.DeferredSnapshot{},
	}
	for _, workID := range workIDs {
		stored, found := s.works[workID]
		if !found {
			continue
		}
		rows.Works[workID] = stored
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
		if !slices.Contains(workIDs, key.workID) {
			continue
		}
		if rows.Locales[key.workID] == nil {
			rows.Locales[key.workID] = map[string]work.LocaleMetadata{}
		}
		rows.Locales[key.workID][key.locale] = row
	}
	return rows, nil
}
