// Package sitemapadapter provides PostgreSQL persistence for the Sitemap read model.
package sitemapadapter

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	sitemapdomain "github.com/echovisionlab/geul-api/internal/sitemap"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// PostgresStore serves the Sitemap read model directly from authoritative
// domain tables and persists the last successfully rendered snapshot.
type PostgresStore struct {
	db *gorm.DB
}

var _ sitemapdomain.Store = (*PostgresStore)(nil)

// NewPostgresStore creates the PostgreSQL-backed Sitemap store.
func NewPostgresStore(db *gorm.DB) *PostgresStore {
	if db == nil {
		panic("sitemap postgres database is required")
	}
	return &PostgresStore{db: db}
}

func (s *PostgresStore) LoadSiteContext(ctx context.Context) (sitemapdomain.SiteContext, error) {
	var row struct {
		HomepagePageID *string `gorm:"column:homepage_page_id"`
	}
	result := s.db.WithContext(ctx).
		Table("site_settings").
		Select("homepage_page_id").
		Where("id = ?", 1).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return sitemapdomain.SiteContext{}, nil
	}
	if result.Error != nil {
		return sitemapdomain.SiteContext{}, result.Error
	}
	return sitemapdomain.SiteContext{HomepagePageID: row.HomepagePageID}, nil
}

func (s *PostgresStore) LoadHomepage(ctx context.Context, pageID string) (*sitemapdomain.Entry, error) {
	var row entryRow
	err := s.db.WithContext(ctx).
		Table("page").
		Select("id, published_at, updated_at").
		Where("id = ?", pageID).
		Where("status = ?", managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()).
		Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entry := row.entry()
	return &entry, nil
}

func (s *PostgresStore) ListPages(ctx context.Context) ([]sitemapdomain.Entry, error) {
	return s.listEntries(
		ctx,
		"page",
		[]string{managev1.PageStatus_PAGE_STATUS_PUBLISHED.String()},
		"published_at DESC NULLS LAST, updated_at DESC",
		true,
	)
}

func (s *PostgresStore) ListPrivacyHistory(ctx context.Context) ([]sitemapdomain.Entry, error) {
	return s.listLegalHistory(ctx, "privacy_history")
}

func (s *PostgresStore) ListTermsHistory(ctx context.Context) ([]sitemapdomain.Entry, error) {
	return s.listLegalHistory(ctx, "terms_history")
}

func (s *PostgresStore) ListPosts(ctx context.Context) ([]sitemapdomain.Entry, error) {
	return s.listEntries(
		ctx,
		"post",
		[]string{
			managev1.PostStatus_POST_STATUS_PUBLISHED.String(),
			managev1.PostStatus_POST_STATUS_ARCHIVED.String(),
		},
		"published_at DESC NULLS LAST, updated_at DESC",
		false,
	)
}

func (s *PostgresStore) ListWorks(ctx context.Context) ([]sitemapdomain.Entry, error) {
	return s.listEntries(
		ctx,
		"work",
		[]string{
			managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
			managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
		},
		"published_at DESC NULLS LAST, updated_at DESC",
		false,
	)
}

func (s *PostgresStore) ListCategories(ctx context.Context) ([]sitemapdomain.Entry, error) {
	var rows []entryRow
	if err := s.db.WithContext(ctx).
		Table("category").
		Select("slug, updated_at, created_at").
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return entries(rows), nil
}

func (s *PostgresStore) ListTags(ctx context.Context) ([]sitemapdomain.Entry, error) {
	var rows []entryRow
	if err := s.db.WithContext(ctx).
		Table("tag").
		Select("slug, created_at").
		Order("name ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return entries(rows), nil
}

func (s *PostgresStore) listEntries(
	ctx context.Context,
	table string,
	statuses []string,
	order string,
	requireSlug bool,
) ([]sitemapdomain.Entry, error) {
	var rows []entryRow
	query := s.db.WithContext(ctx).
		Table(table).
		Select("id, slug, published_at, updated_at, created_at").
		Where("status IN ?", statuses)
	if requireSlug {
		query = query.Where("slug IS NOT NULL AND slug <> ''")
	}
	if err := query.Order(order).Find(&rows).Error; err != nil {
		return nil, err
	}
	return entries(rows), nil
}

func (s *PostgresStore) listLegalHistory(ctx context.Context, table string) ([]sitemapdomain.Entry, error) {
	var rows []entryRow
	if err := s.db.WithContext(ctx).
		Table(table).
		Select("id, effective_from AS published_at, updated_at").
		Where("effective_from IS NOT NULL").
		Order("effective_from DESC NULLS LAST, updated_at DESC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return entries(rows), nil
}

type entryRow struct {
	ID          string     `gorm:"column:id"`
	Slug        *string    `gorm:"column:slug"`
	PublishedAt *time.Time `gorm:"column:published_at"`
	UpdatedAt   *time.Time `gorm:"column:updated_at"`
	CreatedAt   *time.Time `gorm:"column:created_at"`
}

func (r entryRow) entry() sitemapdomain.Entry {
	return sitemapdomain.Entry{
		ID:          r.ID,
		Slug:        r.Slug,
		PublishedAt: r.PublishedAt,
		UpdatedAt:   r.UpdatedAt,
		CreatedAt:   r.CreatedAt,
	}
}

func entries(rows []entryRow) []sitemapdomain.Entry {
	result := make([]sitemapdomain.Entry, 0, len(rows))
	for _, row := range rows {
		result = append(result, row.entry())
	}
	return result
}

func (s *PostgresStore) LoadSnapshot(ctx context.Context, key string) (*sitemapdomain.Snapshot, error) {
	var snapshot sitemapdomain.Snapshot
	result := s.db.WithContext(ctx).Raw(`
		SELECT content, content_type, generated_at
		FROM public.sitemap_snapshot
		WHERE key = ?`, key).Scan(&snapshot)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		return nil, nil
	}
	snapshot.GeneratedAt = snapshot.GeneratedAt.UTC()
	return &snapshot, nil
}

func (s *PostgresStore) SaveSnapshot(
	ctx context.Context,
	key string,
	snapshot *sitemapdomain.Snapshot,
) (bool, error) {
	if snapshot == nil {
		return false, nil
	}
	result := executeSnapshotUpsert(s.db.WithContext(ctx), key, snapshot)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func executeSnapshotUpsert(
	db *gorm.DB,
	key string,
	snapshot *sitemapdomain.Snapshot,
) *gorm.DB {
	// GeneratedAt belongs to the materialized content version. A later request
	// that renders identical content must remain a read-only statement. The
	// conflict predicate closes the concurrent-insert race without rewriting it.
	return db.Exec(`
		INSERT INTO public.sitemap_snapshot (
			key, content, content_type, generated_at, updated_at
		)
		SELECT ?, ?, ?, ?, CURRENT_TIMESTAMP
		WHERE NOT EXISTS (
			SELECT 1
			FROM public.sitemap_snapshot
			WHERE key = ?
				AND content IS NOT DISTINCT FROM ?
				AND content_type IS NOT DISTINCT FROM ?
		)
		ON CONFLICT (key) DO UPDATE SET
			content = EXCLUDED.content,
			content_type = EXCLUDED.content_type,
			generated_at = EXCLUDED.generated_at,
			updated_at = CURRENT_TIMESTAMP
		WHERE sitemap_snapshot.content IS DISTINCT FROM EXCLUDED.content
			OR sitemap_snapshot.content_type IS DISTINCT FROM EXCLUDED.content_type`,
		key,
		snapshot.Content,
		snapshot.ContentType,
		snapshot.GeneratedAt.UTC(),
		key,
		snapshot.Content,
		snapshot.ContentType,
	)
}
