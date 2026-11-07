package pagepostgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/page"
)

func (s *store) AIDocumentBatchRows(ctx context.Context, pageIDs []string) (page.AIDocumentBatchRows, error) {
	rows := page.AIDocumentBatchRows{Pages: map[string]page.Page{}, Locales: map[string]map[string]page.Locale{}}
	if len(pageIDs) == 0 {
		return rows, nil
	}
	var pages []model.Page
	if err := s.tx.WithContext(ctx).Where("id IN ?", pageIDs).Find(&pages).Error; err != nil {
		return page.AIDocumentBatchRows{}, fmt.Errorf("load Page AI document roots: %w", err)
	}
	snapshots := make([]contentblock.SnapshotRequest, 0, len(pages))
	for _, row := range pages {
		rows.Pages[row.ID] = pageFromRow(row)
		if row.ContentDocumentID == nil {
			continue
		}
		// A document ID that does not parse fails that Page when its state is built.
		if documentID, err := uuid.Parse(*row.ContentDocumentID); err == nil {
			snapshots = append(snapshots, contentblock.SnapshotRequest{DocumentID: documentID, SourceLocale: row.SourceLocale})
		}
	}
	var locales []struct {
		EntityID      string    `gorm:"column:entity_id"`
		Locale        string    `gorm:"column:locale"`
		Title         *string   `gorm:"column:title"`
		Summary       *string   `gorm:"column:summary"`
		IncarnationID uuid.UUID `gorm:"column:incarnation_id"`
		UpdatedAt     time.Time `gorm:"column:updated_at"`
	}
	if err := s.tx.WithContext(ctx).Table("page_translation").
		Select("entity_id", "locale", "title", "summary", "incarnation_id", "updated_at").
		Where("entity_id IN ?", pageIDs).Find(&locales).Error; err != nil {
		return page.AIDocumentBatchRows{}, fmt.Errorf("load Page AI document locales: %w", err)
	}
	for _, row := range locales {
		if rows.Locales[row.EntityID] == nil {
			rows.Locales[row.EntityID] = map[string]page.Locale{}
		}
		rows.Locales[row.EntityID][row.Locale] = page.Locale{
			Locale: row.Locale, Title: row.Title, Summary: row.Summary, IncarnationID: row.IncarnationID, UpdatedAt: row.UpdatedAt,
		}
	}
	var err error
	rows.Snapshots, err = s.ports.ContentBlocks.LoadSnapshotBatch(ctx, s.tx, snapshots)
	if err != nil {
		return page.AIDocumentBatchRows{}, err
	}
	return rows, nil
}
