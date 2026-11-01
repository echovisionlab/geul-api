package postpostgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// PostFromRow maps a post row to the domain Post.
func PostFromRow(row model.Post) post.Post {
	stored := post.Post{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale, Title: row.Title,
		Summary: row.Summary, Slug: row.Slug, ConfigurationRevision: row.ConfigurationRevision,
		DocumentLayout: documentLayoutFromRow(row.DocumentLayout), Status: post.Status(row.Status),
		CommentsEnabled: row.CommentsEnabled, SeriesID: row.SeriesID, SeriesOrder: row.SeriesOrder,
		MapPlaceID: row.MapPlaceID, FeaturedImageFileID: row.FeaturedImageFileID, OgAssetID: row.OgAssetID,
		SourceLocaleOgAssetID: row.SourceLocaleOgAssetID, PublishedAt: row.PublishedAt, ScheduledAt: row.ScheduledAt,
		ScheduledTimeZone: row.ScheduledTimeZone, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		ContentRevision: row.ContentRevision,
	}
	for _, category := range row.Categories {
		stored.Categories = append(stored.Categories, post.Category{
			ID: category.ID, Name: category.Name, Slug: category.Slug, Description: category.Description, CreatedAt: category.CreatedAt,
		})
	}
	for _, tag := range row.Tags {
		stored.Tags = append(stored.Tags, post.Tag{ID: tag.ID, Name: tag.Name, Slug: tag.Slug, CreatedAt: tag.CreatedAt})
	}
	if row.Series != nil {
		stored.Series = &post.Series{
			ID: row.Series.ID, SourceLocale: row.Series.SourceLocale, Title: row.Series.Title,
			Status: string(row.Series.Status), Slug: row.Series.Slug, Description: row.Series.Description,
			CreatedAt: row.Series.CreatedAt,
		}
	}
	return stored
}

func rowFromPost(stored post.Post) model.Post {
	return model.Post{
		ID: stored.ID, ContentDocumentID: stored.ContentDocumentID, SourceLocale: stored.SourceLocale,
		Slug: stored.Slug, ConfigurationRevision: stored.ConfigurationRevision,
		DocumentLayout: documentLayoutRow(stored.DocumentLayout), Status: model.PostStatus(stored.Status),
		CommentsEnabled: stored.CommentsEnabled, SeriesID: stored.SeriesID, SeriesOrder: stored.SeriesOrder,
		MapPlaceID: stored.MapPlaceID, FeaturedImageFileID: stored.FeaturedImageFileID, OgAssetID: stored.OgAssetID,
		PublishedAt: stored.PublishedAt, ScheduledAt: stored.ScheduledAt, ScheduledTimeZone: stored.ScheduledTimeZone,
		CreatedAt: stored.CreatedAt, UpdatedAt: stored.UpdatedAt,
	}
}

func documentLayoutFromRow(layout model.DocumentLayout) post.DocumentLayout {
	return post.DocumentLayout{
		ContentHeight: string(layout.ContentHeight), PageChrome: string(layout.PageChrome), Footer: string(layout.Footer),
		HideTableOfContents: layout.HideTableOfContents,
	}
}

func documentLayoutRow(layout post.DocumentLayout) model.DocumentLayout {
	return model.DocumentLayout{
		ContentHeight: model.DocumentContentHeight(layout.ContentHeight), PageChrome: model.DocumentRegionPlacement(layout.PageChrome),
		Footer: model.DocumentRegionPlacement(layout.Footer), HideTableOfContents: layout.HideTableOfContents,
	}
}

func (s *store) FindPost(ctx context.Context, postID string) (post.Post, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).
		Preload("Categories").
		Preload("Tags").
		Preload("Series").
		First(&row, "id = ?", postID).Error; err != nil {
		return post.Post{}, notFound(err, post.ErrPostNotFound)
	}
	return PostFromRow(row), nil
}

func (s *store) ReadPost(ctx context.Context, postID string) (post.Post, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", postID).Error; err != nil {
		return post.Post{}, notFound(err, post.ErrPostNotFound)
	}
	return PostFromRow(row), nil
}

func (s *store) TakePost(ctx context.Context, postID string) (post.Post, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).Take(&row, "id = ?", postID).Error; err != nil {
		return post.Post{}, notFound(err, post.ErrPostNotFound)
	}
	return PostFromRow(row), nil
}

func (s *store) FindPostID(ctx context.Context, postID string) error {
	var row model.Post
	return notFound(s.tx.WithContext(ctx).Select("id").First(&row, "id = ?", postID).Error, post.ErrPostNotFound)
}

func (s *store) PostStatus(ctx context.Context, postID string) (post.Status, error) {
	var row struct{ Status model.PostStatus }
	if err := s.tx.WithContext(ctx).Table("post").Select("status").Where("id = ?", postID).Take(&row).Error; err != nil {
		return "", notFound(err, post.ErrPostNotFound)
	}
	return post.Status(row.Status), nil
}

func (s *store) LockPostStatus(ctx context.Context, postID string) (post.Status, error) {
	var row struct {
		Status model.PostStatus `gorm:"column:status"`
	}
	if err := s.tx.WithContext(ctx).
		Table("post").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("status").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		return "", notFound(err, post.ErrPostNotFound)
	}
	return post.Status(row.Status), nil
}

func (s *store) LockParticipantRoot(ctx context.Context, postID string) (post.Status, error) {
	var row struct {
		ID     string           `gorm:"column:id"`
		Status model.PostStatus `gorm:"column:status"`
	}
	if err := s.tx.WithContext(ctx).
		Table("post").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		return "", notFound(err, post.ErrPostNotFound)
	}
	return post.Status(row.Status), nil
}

func (s *store) KeySharePostStatus(ctx context.Context, postID string) (post.Status, error) {
	var root struct {
		Status model.PostStatus `gorm:"column:status"`
	}
	if err := s.tx.WithContext(ctx).Table("post").Clauses(clause.Locking{Strength: "KEY SHARE"}).
		Select("status").Where("id = ?::uuid", postID).Take(&root).Error; err != nil {
		return "", notFound(err, post.ErrPostNotFound)
	}
	return post.Status(root.Status), nil
}

func (s *store) KeyShareLockPost(ctx context.Context, postID string) (post.Post, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: "KEY SHARE"}).First(&row, "id = ?", postID).Error; err != nil {
		return post.Post{}, notFound(err, post.ErrPostNotFound)
	}
	return PostFromRow(row), nil
}

func (s *store) LockPostRoot(ctx context.Context, postID string) error {
	var row struct {
		ID string `gorm:"column:id"`
	}
	return notFound(s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("post").
		Select("id").
		Where("id = ?", postID).
		Take(&row).Error, post.ErrPostNotFound)
}

type documentRootRow struct {
	ID                string           `gorm:"column:id"`
	Status            model.PostStatus `gorm:"column:status"`
	ContentDocumentID *uuid.UUID       `gorm:"column:content_document_id;type:uuid"`
	SourceLocale      string           `gorm:"column:source_locale"`
}

func (row documentRootRow) root() post.DocumentRoot {
	return post.DocumentRoot{ID: row.ID, Status: post.Status(row.Status), ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale}
}

func (s *store) DocumentRoot(ctx context.Context, postID string) (post.DocumentRoot, error) {
	var root documentRootRow
	if err := s.tx.WithContext(ctx).
		Table("post").
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?::uuid", postID).
		Take(&root).Error; err != nil {
		return post.DocumentRoot{}, notFound(err, post.ErrPostNotFound)
	}
	return root.root(), nil
}

func (s *store) LockDocumentRoot(ctx context.Context, postID string) (post.DocumentRoot, error) {
	var root documentRootRow
	if err := s.tx.WithContext(ctx).
		Table("post").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "status", "content_document_id", "source_locale").
		Where("id = ?::uuid", postID).
		Take(&root).Error; err != nil {
		return post.DocumentRoot{}, notFound(err, post.ErrPostNotFound)
	}
	return root.root(), nil
}

func (s *store) AIDocumentRoot(ctx context.Context, postID string, lock post.RowLock) (post.AIDocumentRoot, error) {
	query := s.tx.WithContext(ctx).
		Table("post").
		Select("id::text AS id", "status", "content_document_id::text AS content_document_id").
		Where("id = ?::uuid", postID)
	if lock != post.Unlocked {
		query = query.Clauses(clause.Locking{Strength: string(lock)})
	}
	var root struct {
		ID                string           `gorm:"column:id"`
		Status            model.PostStatus `gorm:"column:status"`
		ContentDocumentID *string          `gorm:"column:content_document_id"`
	}
	if err := query.Take(&root).Error; err != nil {
		return post.AIDocumentRoot{}, notFound(err, post.ErrPostNotFound)
	}
	return post.AIDocumentRoot{ID: root.ID, Status: post.Status(root.Status), ContentDocumentID: root.ContentDocumentID}, nil
}

func (s *store) SourceLocale(ctx context.Context, postID string) (string, error) {
	var row struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	if err := s.tx.WithContext(ctx).
		Table("post").
		Select("source_locale").
		Where("id = ?::uuid", postID).
		Take(&row).Error; err != nil {
		return "", notFound(err, post.ErrPostNotFound)
	}
	return row.SourceLocale, nil
}

func (s *store) SwitchSourceLocale(ctx context.Context, postID, from, to string, now time.Time) (int64, error) {
	updated := s.tx.WithContext(ctx).Table("post").
		Where("id = ?::uuid AND source_locale = ?", postID, from).
		Updates(map[string]any{"source_locale": to, "updated_at": now})
	return updated.RowsAffected, updated.Error
}

func (s *store) InsertPost(ctx context.Context, stored *post.Post) error {
	row := rowFromPost(*stored)
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return slugTakenError{err}
		}
		return err
	}
	created := PostFromRow(row)
	stored.ID, stored.ConfigurationRevision = created.ID, created.ConfigurationRevision
	stored.CreatedAt, stored.UpdatedAt = created.CreatedAt, created.UpdatedAt
	return nil
}

func (s *store) UpdateSettings(ctx context.Context, stored *post.Post, change post.SettingsChange) error {
	fields := structured.Fields{"updated_at": change.UpdatedAt}
	if change.Slug != nil {
		fields["slug"] = optionalValue(change.Slug.Value)
	}
	if change.CommentsEnabled != nil {
		fields["comments_enabled"] = *change.CommentsEnabled
	}
	if change.MapPlaceID != nil {
		fields["map_place_id"] = optionalValue(change.MapPlaceID.Value)
	}
	if change.DocumentLayout != nil {
		fields["document_layout"] = documentLayoutRow(*change.DocumentLayout)
	}
	row := rowFromPost(*stored)
	if err := s.tx.WithContext(ctx).Model(&row).Clauses(clause.Returning{}).Updates(fields).Error; err != nil {
		return err
	}
	updated := PostFromRow(row)
	updated.Title, updated.Summary, updated.SourceLocaleOgAssetID = stored.Title, stored.Summary, stored.SourceLocaleOgAssetID
	*stored = updated
	return nil
}

func optionalValue(value *string) structured.Value {
	if value == nil {
		return nil
	}
	return *value
}

func (s *store) UpdateLifecycle(ctx context.Context, postID string, change post.LifecycleChange) error {
	updates := structured.Fields{"status": string(change.Status), "updated_at": change.UpdatedAt}
	if change.ClearSchedule {
		updates["scheduled_at"] = nil
		updates["scheduled_time_zone"] = nil
	}
	if change.ScheduledAt != nil {
		updates["scheduled_at"] = *change.ScheduledAt
	}
	if change.ScheduledTimeZone != nil {
		updates["scheduled_time_zone"] = *change.ScheduledTimeZone
	}
	if change.PublishedAt != nil {
		updates["published_at"] = *change.PublishedAt
	}
	return s.tx.WithContext(ctx).Model(&model.Post{ID: postID}).Updates(updates).Error
}

func (s *store) FeaturedImageFileID(ctx context.Context, postID string) (*string, error) {
	var row model.Post
	if err := s.tx.WithContext(ctx).Select("id", "status", "featured_image_file_id").First(&row, "id = ?", postID).Error; err != nil {
		return nil, notFound(err, post.ErrPostNotFound)
	}
	return row.FeaturedImageFileID, nil
}

func (s *store) SetFeaturedImage(ctx context.Context, postID string, fileID *string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Post{}).Where("id = ?", postID).Updates(structured.Fields{
		"featured_image_file_id": optionalValue(fileID),
		"updated_at":             now,
	}).Error
}

func (s *store) TouchPost(ctx context.Context, postID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Post{}).Where("id = ?", postID).UpdateColumn("updated_at", now).Error
}

func (s *store) DeletePost(ctx context.Context, postID string) error {
	return s.tx.WithContext(ctx).Delete(&model.Post{ID: postID}).Error
}

func (s *store) DatabaseNow(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := s.tx.WithContext(ctx).Raw("SELECT CURRENT_TIMESTAMP").Scan(&now).Error
	return now, err
}

func (s *store) LockDuePosts(ctx context.Context, limit int) ([]post.Post, error) {
	var due []model.Post
	if err := s.tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Options: clause.LockingOptionsSkipLocked}).
		Select("id", "status", "scheduled_at", "published_at").
		Where("status = ? AND scheduled_at <= CURRENT_TIMESTAMP", string(model.PostStatusScheduled)).
		Order("scheduled_at ASC, id ASC").
		Limit(limit).
		Find(&due).Error; err != nil {
		return nil, err
	}
	posts := make([]post.Post, len(due))
	for index, row := range due {
		posts[index] = PostFromRow(row)
	}
	return posts, nil
}

func (s *store) PublishDuePost(ctx context.Context, postID string, publishedAt *time.Time, now time.Time) (bool, error) {
	updates := structured.Fields{
		"status":              string(model.PostStatusPublished),
		"scheduled_at":        nil,
		"scheduled_time_zone": nil,
		"updated_at":          now,
	}
	if publishedAt != nil {
		updates["published_at"] = *publishedAt
	}
	result := s.tx.WithContext(ctx).Model(&model.Post{}).
		Where("id = ? AND status = ? AND scheduled_at <= CURRENT_TIMESTAMP", postID, string(model.PostStatusScheduled)).
		Updates(updates)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func (s *store) SlugTaken(ctx context.Context, slug, excludeID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Post{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) RouteAvailable(ctx context.Context, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, s.tx, "posts", slug)
}

func (s *store) CheckRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.tx, "post", "posts", slug)
}

func (s *store) LockRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, s.tx, "post", "posts", slug)
}

func (s *store) MapPlaceExists(ctx context.Context, mapPlaceID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Table("map_place").
		Where("id = ?", mapPlaceID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

// scopedPosts limits query to the Posts scope lets a principal view.
func scopedPosts(query *gorm.DB, scope post.ViewScope) *gorm.DB {
	archivedStatus := string(model.PostStatusArchived)
	switch {
	case len(scope.ViewIDs) > 0 && len(scope.ArchivedIDs) > 0:
		return query.Where(
			"((status <> ? AND id IN ?) OR (status = ? AND id IN ?))",
			archivedStatus,
			scope.ViewIDs,
			archivedStatus,
			scope.ArchivedIDs,
		)
	case len(scope.ViewIDs) > 0:
		return query.Where("status <> ? AND id IN ?", archivedStatus, scope.ViewIDs)
	case len(scope.ArchivedIDs) > 0:
		return query.Where("status = ? AND id IN ?", archivedStatus, scope.ArchivedIDs)
	default:
		return query.Where("FALSE")
	}
}

func (s *store) ListPosts(ctx context.Context, query post.ListQuery) ([]post.Post, int64, error) {
	stored := s.tx.WithContext(ctx).Model(&model.Post{})
	if query.Scope != nil {
		stored = scopedPosts(stored, *query.Scope)
	}
	stored, remaining := applyPostRelationFilters(stored, query.Filters, query.ByAuthor)
	stored, err := PostAdminFilterConfig.ApplyFilters(stored, remaining)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	if stored, err = postSortConfig.ApplySort(stored, query.Sorts); err != nil {
		return nil, 0, err
	}
	stored = stored.Preload("Categories").Preload("Tags")
	if query.Series {
		stored = stored.Preload("Series")
	}
	var rows []model.Post
	if err := stored.Limit(int(query.Limit)).Offset(int(query.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	posts := make([]post.Post, len(rows))
	for index, row := range rows {
		posts[index] = PostFromRow(row)
	}
	return posts, total, nil
}

func (s *store) ListAIDocuments(ctx context.Context, query post.AIDocumentQuery) ([]post.AIDocumentListItem, int64, error) {
	listed := scopedPosts(s.tx.WithContext(ctx).Model(&model.Post{}), query.Scope)
	if query.Search != "" {
		listed = listed.Where(
			"("+PostSourceTitleSQL+") ILIKE ? ESCAPE E'\\\\'",
			"%"+escapePostTitleSearch(query.Search)+"%",
		)
	}
	var total int64
	if err := listed.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	type row struct {
		ID                    string           `gorm:"column:id"`
		Title                 string           `gorm:"column:title"`
		Slug                  *string          `gorm:"column:slug"`
		SourceLocale          string           `gorm:"column:source_locale"`
		Status                model.PostStatus `gorm:"column:status"`
		UpdatedAt             time.Time        `gorm:"column:updated_at"`
		ConfigurationRevision string           `gorm:"column:configuration_revision"`
	}
	var rows []row
	if err := listed.
		Select(
			"post.id::text AS id",
			PostSourceTitleSQL+" AS title",
			"post.slug",
			"post.source_locale",
			"post.status",
			"post.updated_at",
			"post.configuration_revision::text AS configuration_revision",
		).
		Order("post.updated_at DESC, post.id ASC").
		Limit(query.Limit).
		Offset(query.Offset).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	items := make([]post.AIDocumentListItem, len(rows))
	for index, item := range rows {
		items[index] = post.AIDocumentListItem{
			ID: item.ID, Title: item.Title, Slug: item.Slug,
			SourceLocale: item.SourceLocale, Status: string(item.Status), UpdatedAt: item.UpdatedAt,
			ConfigurationRevision: item.ConfigurationRevision,
		}
	}
	return items, total, nil
}
