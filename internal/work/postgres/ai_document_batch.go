package workpostgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/work"
)

func (s *store) AIDocumentBatchRows(ctx context.Context, workIDs []string) (work.AIDocumentBatchRows, error) {
	rows := work.AIDocumentBatchRows{Works: map[string]work.Work{}, Locales: map[string]map[string]work.LocaleMetadata{}}
	if len(workIDs) == 0 {
		return rows, nil
	}
	var works []model.Work
	if err := s.tx.WithContext(ctx).Table("work").Where("id IN ?", workIDs).Find(&works).Error; err != nil {
		return work.AIDocumentBatchRows{}, fmt.Errorf("load Work AI document roots: %w", err)
	}
	snapshots := make([]contentblock.SnapshotRequest, 0, len(works))
	for _, row := range works {
		root := workFromRow(row)
		rows.Works[root.ID] = root
		if root.ContentDocumentID == nil {
			continue
		}
		// A document ID that does not parse fails that Work when its state is built.
		if documentID, err := uuid.Parse(*root.ContentDocumentID); err == nil {
			snapshots = append(snapshots, contentblock.SnapshotRequest{DocumentID: documentID, SourceLocale: root.SourceLocale})
		}
	}
	var locales []struct {
		EntityID  string    `gorm:"column:entity_id"`
		Locale    string    `gorm:"column:locale"`
		Title     *string   `gorm:"column:title"`
		Summary   *string   `gorm:"column:summary"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := s.tx.WithContext(ctx).Table("work_translation").
		Select("entity_id", "locale", "title", "summary", "updated_at").
		Where("entity_id IN ?", workIDs).Find(&locales).Error; err != nil {
		return work.AIDocumentBatchRows{}, fmt.Errorf("load Work AI document locales: %w", err)
	}
	for _, row := range locales {
		if rows.Locales[row.EntityID] == nil {
			rows.Locales[row.EntityID] = map[string]work.LocaleMetadata{}
		}
		rows.Locales[row.EntityID][row.Locale] = work.LocaleMetadata{Locale: row.Locale, Title: row.Title, Summary: row.Summary, UpdatedAt: row.UpdatedAt}
	}
	var err error
	rows.Snapshots, err = s.ports.ContentBlocks.LoadSnapshotBatch(ctx, s.tx, snapshots)
	if err != nil {
		return work.AIDocumentBatchRows{}, err
	}
	return rows, nil
}
