package pagepostgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/mutation"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/translation"
)

// slugTakenError answers a unique violation of a Page write as
// page.ErrSlugTaken and keeps the driver error in the chain.
type slugTakenError struct{ error }

func (err slugTakenError) Is(target error) bool { return target == page.ErrSlugTaken }
func (err slugTakenError) Unwrap() error        { return err.error }

func slugTaken(err error) error {
	if dberrors.IsUniqueViolation(err) {
		return slugTakenError{err}
	}
	return err
}

func lockClause(query *gorm.DB, lock page.RowLock) *gorm.DB {
	switch lock {
	case page.ShareLock:
		return query.Clauses(clause.Locking{Strength: clause.LockingStrengthShare})
	case page.UpdateLock:
		return query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	default:
		return query
	}
}

func pageFromRow(row model.Page) page.Page {
	return page.Page{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale, Slug: row.Slug,
		DocumentLayout: layoutFromRow(row.DocumentLayout), AccessPolicy: row.AccessPolicy, Status: page.Status(row.Status),
		ShowTitle: row.ShowTitle, FeaturedImageFileID: row.FeaturedImageFileID, OgAssetID: row.OgAssetID,
		PublishedAt: row.PublishedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func pageRow(value page.Page) model.Page {
	return model.Page{
		ID: value.ID, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale, Slug: value.Slug,
		DocumentLayout: layoutRow(value.DocumentLayout), AccessPolicy: value.AccessPolicy, Status: model.PageStatus(value.Status),
		ShowTitle: value.ShowTitle, FeaturedImageFileID: value.FeaturedImageFileID, OgAssetID: value.OgAssetID,
		PublishedAt: value.PublishedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func layoutFromRow(layout model.DocumentLayout) page.DocumentLayout {
	return page.DocumentLayout{
		ContentHeight: page.ContentHeight(layout.ContentHeight), PageChrome: page.RegionPlacement(layout.PageChrome),
		Footer: page.RegionPlacement(layout.Footer), HideTableOfContents: layout.HideTableOfContents,
	}
}

func layoutRow(layout page.DocumentLayout) model.DocumentLayout {
	return model.DocumentLayout{
		ContentHeight: model.DocumentContentHeight(layout.ContentHeight), PageChrome: model.DocumentRegionPlacement(layout.PageChrome),
		Footer: model.DocumentRegionPlacement(layout.Footer), HideTableOfContents: layout.HideTableOfContents,
	}
}

func (s *store) ListPages(ctx context.Context, list page.ListPage) ([]page.Page, int64, error) {
	var rows []model.Page
	var total int64
	stored, err := pageFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.Page{}), list.Filters)
	if err != nil {
		return nil, 0, err
	}
	if err := stored.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	stored, err = pageSortConfig.ApplySort(stored, list.Sorts)
	if err != nil {
		return nil, 0, err
	}
	if err := stored.Order("page.id ASC").Limit(int(list.Limit)).Offset(int(list.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	pages := make([]page.Page, len(rows))
	for i, row := range rows {
		pages[i] = pageFromRow(row)
	}
	return pages, total, nil
}

func (s *store) FindPage(ctx context.Context, locator page.Locator, value string, lock page.RowLock) (page.Page, error) {
	column := "id"
	if locator == page.BySlug {
		column = "slug"
	}
	var row model.Page
	if err := lockClause(s.tx.WithContext(ctx), lock).Where(column+" = ?", value).Take(&row).Error; err != nil {
		return page.Page{}, notFound(err, page.ErrPageNotFound)
	}
	return pageFromRow(row), nil
}

func (s *store) DocumentRoot(ctx context.Context, pageID string, lock page.RowLock) (page.DocumentRoot, error) {
	var root struct {
		ID                string     `gorm:"column:id"`
		ContentDocumentID *uuid.UUID `gorm:"column:content_document_id;type:uuid"`
		SourceLocale      string     `gorm:"column:source_locale"`
	}
	if err := lockClause(s.tx.WithContext(ctx).Table("page"), lock).
		Select("id", "content_document_id", "source_locale").Where("id = ?", pageID).Take(&root).Error; err != nil {
		return page.DocumentRoot{}, notFound(err, page.ErrPageNotFound)
	}
	return page.DocumentRoot{ID: root.ID, ContentDocumentID: root.ContentDocumentID, SourceLocale: root.SourceLocale}, nil
}

func (s *store) AccessPolicy(ctx context.Context, pageID string) (json.RawMessage, error) {
	var row struct {
		AccessPolicy json.RawMessage `gorm:"column:access_policy"`
	}
	if err := s.tx.WithContext(ctx).Table("page").Select("access_policy").Where("id = ?", pageID).Take(&row).Error; err != nil {
		return nil, notFound(err, page.ErrPageNotFound)
	}
	return row.AccessPolicy, nil
}

func (s *store) DocumentLayout(ctx context.Context, pageID string) (page.DocumentLayout, error) {
	var row struct {
		DocumentLayout model.DocumentLayout `gorm:"column:document_layout"`
	}
	if err := s.tx.WithContext(ctx).Table("page").Select("document_layout").Where("id = ?", pageID).Take(&row).Error; err != nil {
		return page.DocumentLayout{}, notFound(err, page.ErrPageNotFound)
	}
	return layoutFromRow(row.DocumentLayout), nil
}

func (s *store) LockSettings(ctx context.Context, pageID string) (page.Settings, error) {
	var state struct {
		Slug              *string         `gorm:"column:slug"`
		ShowTitle         bool            `gorm:"column:show_title"`
		ContentDocumentID *uuid.UUID      `gorm:"column:content_document_id;type:uuid"`
		UpdatedAt         time.Time       `gorm:"column:updated_at"`
		AccessPolicy      json.RawMessage `gorm:"column:access_policy"`
	}
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Table("page").
		Select("slug", "show_title", "content_document_id", "updated_at", "access_policy").
		Where("id = ?", pageID).
		Take(&state).Error; err != nil {
		return page.Settings{}, notFound(err, page.ErrPageNotFound)
	}
	return page.Settings{
		Slug: state.Slug, ShowTitle: state.ShowTitle, ContentDocumentID: state.ContentDocumentID,
		UpdatedAt: state.UpdatedAt, AccessPolicy: state.AccessPolicy,
	}, nil
}

func (s *store) SlugTaken(ctx context.Context, slug, excludeID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Page{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) RouteOccupied(ctx context.Context, slug string) (bool, error) {
	return routeregistry.IsPageRouteOccupiedByResource(ctx, s.tx, slug)
}

func (s *store) LockRoute(ctx context.Context, slug string) error {
	return routeregistry.LockPageRouteConflict(ctx, s.tx, slug)
}

func (s *store) InsertPage(ctx context.Context, value *page.Page) error {
	row := pageRow(*value)
	if err := s.tx.WithContext(ctx).Omit("ID").Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return slugTaken(err)
	}
	*value = pageFromRow(row)
	return nil
}

func (s *store) UpdateSettings(ctx context.Context, pageID string, change page.SettingsChange, now time.Time) error {
	columns := structured.Fields{"updated_at": now}
	if change.SetSlug {
		columns["slug"] = nil
		if change.Slug != nil {
			columns["slug"] = *change.Slug
		}
	}
	if change.ShowTitle != nil {
		columns["show_title"] = *change.ShowTitle
	}
	if change.AccessPolicy != nil {
		columns["access_policy"] = change.AccessPolicy
	}
	return slugTaken(s.tx.WithContext(ctx).Model(&model.Page{ID: pageID}).Updates(columns).Error)
}

func (s *store) SetStatus(ctx context.Context, pageID string, status page.Status, publishedAt *time.Time, now time.Time) error {
	updates := structured.Fields{"status": string(status), "updated_at": now}
	if publishedAt != nil {
		updates["published_at"] = *publishedAt
	}
	return s.tx.WithContext(ctx).Model(&model.Page{ID: pageID}).Updates(updates).Error
}

func (s *store) SetFeaturedImage(ctx context.Context, pageID string, fileID *string, now time.Time) error {
	var value any
	if fileID != nil {
		value = *fileID
	}
	return s.tx.WithContext(ctx).Model(&model.Page{ID: pageID}).Updates(structured.Fields{
		"featured_image_file_id": value,
		"updated_at":             now,
	}).Error
}

func (s *store) SetDocumentLayout(ctx context.Context, pageID string, layout page.DocumentLayout, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Page{}).
		Where("id = ?", pageID).
		Updates(structured.Fields{
			"document_layout": layoutRow(layout),
			"updated_at":      now,
		}).Error
}

func (s *store) SwitchSourceLocale(ctx context.Context, pageID, from, to string, now time.Time) (bool, error) {
	updated := s.tx.WithContext(ctx).Exec(
		"UPDATE page SET source_locale = ?, updated_at = ? WHERE id = ? AND source_locale = ?",
		to, now, pageID, from,
	)
	if updated.Error != nil {
		return false, updated.Error
	}
	return updated.RowsAffected == 1, nil
}

func (s *store) TouchPage(ctx context.Context, pageID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Page{}).Where("id = ?", pageID).UpdateColumn("updated_at", now).Error
}

func (s *store) DeletePage(ctx context.Context, pageID string) error {
	return s.tx.WithContext(ctx).Delete(&model.Page{ID: pageID}).Error
}

func (s *store) SourceMetadata(ctx context.Context, pageIDs []string) (map[string]page.SourceMetadata, error) {
	normalized := sliceutil.UniqueTrimmed(pageIDs)
	if len(normalized) == 0 {
		return map[string]page.SourceMetadata{}, nil
	}
	var rows []struct {
		EntityID  string         `gorm:"column:entity_id"`
		Locale    string         `gorm:"column:locale"`
		Title     sql.NullString `gorm:"column:title"`
		Summary   sql.NullString `gorm:"column:summary"`
		OgAssetID sql.NullString `gorm:"column:og_asset_id"`
	}
	if err := s.tx.WithContext(ctx).Raw(
		`SELECT CAST(pt.entity_id AS text) AS entity_id,
		        p.source_locale AS locale,
		        pt.title,
		        pt.summary,
		        pt.og_asset_id
		 FROM page AS p
		 JOIN page_translation AS pt
		   ON pt.entity_id = p.id
		  AND pt.locale = p.source_locale
		 WHERE CAST(p.id AS text) IN ?
		`,
		normalized,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	states := make(map[string]page.SourceMetadata, len(rows))
	for _, row := range rows {
		if strings.TrimSpace(row.Locale) == "" {
			return nil, fmt.Errorf("page %s has an empty source locale", row.EntityID)
		}
		state := page.SourceMetadata{Locale: row.Locale}
		if row.Title.Valid {
			state.Title = &row.Title.String
		}
		if row.Summary.Valid {
			state.Summary = &row.Summary.String
		}
		if row.OgAssetID.Valid {
			state.OgAssetID = &row.OgAssetID.String
		}
		states[row.EntityID] = state
	}
	return states, nil
}

func (s *store) Locale(ctx context.Context, pageID, locale string, lock page.RowLock) (page.Locale, bool, error) {
	var row struct {
		Locale        string    `gorm:"column:locale"`
		Title         *string   `gorm:"column:title"`
		Summary       *string   `gorm:"column:summary"`
		IncarnationID uuid.UUID `gorm:"column:incarnation_id"`
		UpdatedAt     time.Time `gorm:"column:updated_at"`
	}
	query := lockClause(s.tx.WithContext(ctx).Table("page_translation"), lock).
		Select("locale", "title", "summary", "incarnation_id", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", pageID, locale)
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return page.Locale{}, false, nil
		}
		return page.Locale{}, false, err
	}
	return page.Locale{
		Locale: row.Locale, Title: row.Title, Summary: row.Summary, IncarnationID: row.IncarnationID, UpdatedAt: row.UpdatedAt,
	}, true, nil
}

func (s *store) InsertLocale(ctx context.Context, pageID, locale string, title, summary *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO page_translation (
			entity_id,
			locale,
			title,
			summary,
			created_at,
			updated_at
			) VALUES (?, ?, ?, ?, ?, ?)
	`,
		pageID,
		locale,
		title,
		summary,
		now,
		now,
	).Error
}

func (s *store) CreateTargetLocale(ctx context.Context, pageID, locale string, title, summary *string, now time.Time) (uuid.UUID, error) {
	var inserted struct {
		IncarnationID uuid.UUID `gorm:"column:incarnation_id"`
	}
	created := s.tx.WithContext(ctx).Raw(
		"INSERT INTO page_translation (entity_id, locale, title, summary, created_at, updated_at) VALUES (?::uuid, ?, ?, ?, ?, ?) RETURNING incarnation_id",
		pageID, locale, title, summary, now, now,
	).Scan(&inserted)
	if created.Error != nil {
		return uuid.Nil, created.Error
	}
	if created.RowsAffected != 1 {
		return uuid.Nil, nil
	}
	return inserted.IncarnationID, nil
}

func (s *store) UpsertLocale(ctx context.Context, pageID, locale string, title, summary *string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(`
		INSERT INTO page_translation (entity_id, locale, title, summary, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (entity_id, locale) DO UPDATE SET
			title = EXCLUDED.title, summary = EXCLUDED.summary, updated_at = EXCLUDED.updated_at
	`, pageID, locale, title, summary, now, now).Error
}

func (s *store) UpdateLocale(ctx context.Context, pageID, locale string, change page.LocaleChange) (bool, error) {
	updates := structured.Fields{"updated_at": change.UpdatedAt}
	if change.SetTitle {
		updates["title"] = change.Title
	}
	if change.SetSummary {
		updates["summary"] = change.Summary
	}
	updated := s.tx.WithContext(ctx).Table("page_translation").
		Where("entity_id = ? AND locale = ?", pageID, locale).Updates(updates)
	if updated.Error != nil {
		return false, updated.Error
	}
	return updated.RowsAffected == 1, nil
}

func (s *store) DeleteLocale(ctx context.Context, pageID, locale string) (bool, error) {
	deleted := s.tx.WithContext(ctx).Exec("DELETE FROM page_translation WHERE entity_id = ?::uuid AND locale = ?", pageID, locale)
	if deleted.Error != nil {
		return false, deleted.Error
	}
	return deleted.RowsAffected == 1, nil
}

func versionFromRow(row model.PageVersion) page.Version {
	return page.Version{
		ID: row.ID, PageID: row.PageID, Version: row.Version, Title: row.Title, Summary: row.Summary,
		ContentSnapshot: row.ContentSnapshot, ContributorMemberIDs: []string(row.ContributorMemberIDs), CreatedAt: row.CreatedAt,
	}
}

func (s *store) ListVersions(ctx context.Context, pageID string, limit, offset int32) ([]page.Version, int64, error) {
	var total int64
	if err := s.tx.WithContext(ctx).Model(&model.PageVersion{}).Where("page_id = ?", pageID).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []model.PageVersion
	if err := s.tx.WithContext(ctx).
		Where("page_id = ?", pageID).
		Order("version DESC").
		Limit(int(limit)).
		Offset(int(offset)).
		Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	versions := make([]page.Version, len(rows))
	for i, row := range rows {
		versions[i] = versionFromRow(row)
	}
	return versions, total, nil
}

func (s *store) FindVersion(ctx context.Context, versionID string) (page.Version, error) {
	var row model.PageVersion
	if err := s.tx.WithContext(ctx).First(&row, "id = ?", versionID).Error; err != nil {
		return page.Version{}, notFound(err, page.ErrVersionNotFound)
	}
	return versionFromRow(row), nil
}

func (s *store) LockLatestVersion(ctx context.Context, pageID string) (page.Version, bool, error) {
	var row model.PageVersion
	err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("page_id = ?", pageID).
		Order("version DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return page.Version{}, false, nil
	}
	if err != nil {
		return page.Version{}, false, err
	}
	return versionFromRow(row), true, nil
}

func (s *store) InsertVersion(ctx context.Context, version *page.Version) error {
	row := model.PageVersion{
		PageID: version.PageID, Version: version.Version, Title: version.Title, Summary: version.Summary,
		ContentSnapshot: version.ContentSnapshot, ContributorMemberIDs: pq.StringArray(version.ContributorMemberIDs),
		CreatedAt: version.CreatedAt,
	}
	if err := s.tx.WithContext(ctx).Create(&row).Error; err != nil {
		return err
	}
	version.ID = row.ID
	return nil
}

func (s *store) RuntimeSettings(ctx context.Context) (translation.RuntimeSettings, error) {
	return mutation.BestEffortRead(ctx, s.tx, func(tx *gorm.DB) (translation.RuntimeSettings, error) {
		return translation.LoadRuntimeSettings(ctx, tx)
	})
}
