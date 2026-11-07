package postpostgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/post"
)

func (s *store) AIDocumentBatchRows(ctx context.Context, postIDs []string) (post.AIDocumentBatchRows, error) {
	rows := post.AIDocumentBatchRows{
		Roots: map[string]post.AIDocumentBatchRoot{}, Locales: map[string]map[string]post.LocaleMetadata{},
		CategoryIDs: map[string][]string{}, TagIDs: map[string][]string{},
	}
	if len(postIDs) == 0 {
		return rows, nil
	}
	var roots []struct {
		ID                string           `gorm:"column:id"`
		Status            model.PostStatus `gorm:"column:status"`
		ContentDocumentID *string          `gorm:"column:content_document_id"`
		SourceLocale      string           `gorm:"column:source_locale"`
	}
	if err := s.tx.WithContext(ctx).Table("post").
		Select("id::text AS id", "status", "content_document_id::text AS content_document_id", "source_locale").
		Where("id IN ?", postIDs).Find(&roots).Error; err != nil {
		return post.AIDocumentBatchRows{}, fmt.Errorf("load Post AI document roots: %w", err)
	}
	snapshots := make([]contentblock.SnapshotRequest, 0, len(roots))
	for _, root := range roots {
		rows.Roots[root.ID] = post.AIDocumentBatchRoot{
			AIDocumentRoot: post.AIDocumentRoot{ID: root.ID, Status: post.Status(root.Status), ContentDocumentID: root.ContentDocumentID},
			SourceLocale:   root.SourceLocale,
		}
		if root.ContentDocumentID == nil {
			continue
		}
		// A document ID that does not parse fails that Post when its state is built.
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
	if err := s.tx.WithContext(ctx).Table("post_translation").
		Select("entity_id", "locale", "title", "summary", "updated_at").
		Where("entity_id IN ?", postIDs).Find(&locales).Error; err != nil {
		return post.AIDocumentBatchRows{}, fmt.Errorf("load Post AI document locales: %w", err)
	}
	for _, locale := range locales {
		if rows.Locales[locale.EntityID] == nil {
			rows.Locales[locale.EntityID] = map[string]post.LocaleMetadata{}
		}
		rows.Locales[locale.EntityID][locale.Locale] = post.LocaleMetadata{Locale: locale.Locale, Title: locale.Title, Summary: locale.Summary, UpdatedAt: locale.UpdatedAt}
	}
	var taxonomies []struct {
		ID          string         `gorm:"column:id"`
		CategoryIDs pq.StringArray `gorm:"column:category_ids;type:text[]"`
		TagIDs      pq.StringArray `gorm:"column:tag_ids;type:text[]"`
	}
	if err := s.tx.WithContext(ctx).Raw(
		`SELECT post.id::text AS id,
		        ARRAY(
		          SELECT post_category.category_id::text
		          FROM post_category
		          WHERE post_category.post_id = post.id
		          ORDER BY post_category.category_id
		        ) AS category_ids,
		        ARRAY(
		          SELECT post_tag.tag_id::text
		          FROM post_tag
		          WHERE post_tag.post_id = post.id
		          ORDER BY post_tag.tag_id
		        ) AS tag_ids
		 FROM post
		 WHERE post.id IN ?`,
		postIDs,
	).Scan(&taxonomies).Error; err != nil {
		return post.AIDocumentBatchRows{}, fmt.Errorf("load Post AI document taxonomy: %w", err)
	}
	for _, taxonomy := range taxonomies {
		rows.CategoryIDs[taxonomy.ID], rows.TagIDs[taxonomy.ID] = taxonomy.CategoryIDs, taxonomy.TagIDs
	}
	var err error
	rows.Snapshots, err = s.ports.ContentBlocks.LoadSnapshotBatch(ctx, s.tx, snapshots)
	if err != nil {
		return post.AIDocumentBatchRows{}, err
	}
	return rows, nil
}
