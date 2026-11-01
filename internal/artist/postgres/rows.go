package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/artist"
	"github.com/echovisionlab/geul-api/internal/entityparticipant"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func artistSourceLocaleColumnSQL(tableAlias string, column string) string {
	alias := strings.TrimSpace(tableAlias)
	if alias == "" {
		alias = "artist"
	}
	return fmt.Sprintf(
		"(SELECT at.%s FROM artist_translation AS at WHERE at.entity_id = %s.id AND at.locale = %s.source_locale LIMIT 1)",
		column,
		alias,
		alias,
	)
}

// artistSourceTitleSQL is the source-locale title of the Artist aliased
// tableAlias.
func artistSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf("COALESCE(%s, '')", artistSourceLocaleColumnSQL(tableAlias, "title"))
}

var artistFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search":       {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{artistSourceTitleSQL("artist"), "real_name"}},
	"country_code": {Column: "country_code", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(model.ArtistStatusDraft), string(model.ArtistStatusPublished),
	}},
}}

var artistSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         artistSourceTitleSQL("artist"),
		"slug":         "slug",
		"status":       "status",
		"country_code": "country_code",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
	},
	DefaultSort: artistSourceTitleSQL("artist") + " ASC",
}

func lockStrength(lock artist.RowLock) string {
	switch lock {
	case artist.KeyShareLock:
		return "KEY SHARE"
	case artist.ShareLock:
		return clause.LockingStrengthShare
	case artist.UpdateLock:
		return clause.LockingStrengthUpdate
	default:
		return ""
	}
}

func withLock(query *gorm.DB, lock artist.RowLock) *gorm.DB {
	if strength := lockStrength(lock); strength != "" {
		return query.Clauses(clause.Locking{Strength: strength})
	}
	return query
}

func artistFromRow(row model.Artist) artist.Artist {
	return artist.Artist{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale,
		Slug: row.Slug, RealName: row.RealName, ParentArtistID: row.ParentArtistID,
		CountryCode: row.CountryCode, Website: row.Website, SocialLinks: row.SocialLinks,
		Status: artist.Status(row.Status), PublishedAt: row.PublishedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func artistToRow(value artist.Artist) model.Artist {
	return model.Artist{
		ID: value.ID, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale,
		Slug: value.Slug, RealName: value.RealName, ParentArtistID: value.ParentArtistID,
		CountryCode: value.CountryCode, Website: value.Website, SocialLinks: value.SocialLinks,
		Status: model.ArtistStatus(value.Status), PublishedAt: value.PublishedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func (s *store) FindArtist(ctx context.Context, artistID string) (artist.Artist, error) {
	var row model.Artist
	if err := s.tx.WithContext(ctx).Where("id = ?", artistID).First(&row).Error; err != nil {
		return artist.Artist{}, notFound(err, artist.ErrArtistNotFound)
	}
	return artistFromRow(row), nil
}

func (s *store) ShareLockArtist(ctx context.Context, artistID string) (artist.Artist, error) {
	var row model.Artist
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", artistID).
		Take(&row).Error; err != nil {
		return artist.Artist{}, notFound(err, artist.ErrArtistNotFound)
	}
	return artistFromRow(row), nil
}

func (s *store) LockArtistID(_ context.Context, artistID string) (string, error) {
	var row model.Artist
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").First(&row, "id = ?", artistID).Error; err != nil {
		return "", notFound(err, artist.ErrArtistNotFound)
	}
	return row.ID, nil
}

func (s *store) ListArtists(ctx context.Context, page artist.ListPage) ([]artist.Artist, int64, error) {
	base := s.tx.WithContext(ctx).Model(&model.Artist{})
	if page.PublishedOnly {
		base = base.Where("status = ?", model.ArtistStatusPublished)
	}
	if page.IDs != nil {
		base = base.Where("id IN ?", page.IDs)
	}
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range page.Filters {
		if f == nil {
			continue
		}
		if f.GetField() == "label_id" && f.GetValue() != "" {
			base = base.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
		} else {
			remainingFilters = append(remainingFilters, f)
		}
	}
	filtered, err := artistFilterConfig.ApplyFilters(base, remainingFilters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	sorted, err := artistSortConfig.ApplySort(filtered, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	if page.TieByID {
		sorted = sorted.Order("artist.id ASC")
	}
	var rows []model.Artist
	if err := sorted.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	artists := make([]artist.Artist, len(rows))
	for i, row := range rows {
		artists[i] = artistFromRow(row)
	}
	return artists, total, nil
}

func (s *store) SlugTaken(ctx context.Context, slug, excludeID string) (bool, error) {
	query := s.tx.WithContext(ctx).Model(&model.Artist{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) InsertArtist(_ context.Context, value *artist.Artist) error {
	row := artistToRow(*value)
	if err := s.tx.Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}}}).
		Create(&row).Error; err != nil {
		return err
	}
	value.ID, value.CreatedAt = row.ID, row.CreatedAt
	return nil
}

func (s *store) InsertCreatorOwner(_ context.Context, artistID, memberID string, now time.Time) error {
	return s.tx.Exec(`
		INSERT INTO artist_owner (artist_id, member_id, created_at)
		VALUES (?::uuid, ?::uuid, ?)
	`, artistID, memberID, now).Error
}

func (s *store) SetArtistStatus(_ context.Context, artistID string, status artist.Status, publishedAt *time.Time, now time.Time) error {
	fields := structured.Fields{"status": model.ArtistStatus(status), "updated_at": now}
	if publishedAt != nil {
		fields["published_at"] = *publishedAt
	}
	return s.tx.Model(&model.Artist{ID: artistID}).Updates(fields).Error
}

func (s *store) TouchArtist(ctx context.Context, artistID string, now time.Time) error {
	result := s.tx.WithContext(ctx).
		Model(&model.Artist{}).
		Where("id = ?", artistID).
		Updates(structured.Fields{"updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return notFound(gorm.ErrRecordNotFound, artist.ErrArtistNotFound)
	}
	return nil
}

func (s *store) StampArtist(ctx context.Context, artistID string, now time.Time) error {
	return s.tx.WithContext(ctx).Model(&model.Artist{}).Where("id = ?", artistID).UpdateColumn("updated_at", now).Error
}

func (s *store) DeleteArtist(_ context.Context, artistID string) error {
	result := s.tx.Delete(&model.Artist{}, "id = ?", artistID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return notFound(gorm.ErrRecordNotFound, artist.ErrArtistNotFound)
	}
	return nil
}

func (s *store) ContentDocumentID(ctx context.Context, artistID string, lock artist.RowLock) (*string, error) {
	var root struct {
		ID                string         `gorm:"column:id"`
		ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
	}
	query := withLock(s.tx.WithContext(ctx).
		Table("artist").
		Select("id", "content_document_id").
		Where("id = ?", artistID), lock)
	if err := query.Take(&root).Error; err != nil {
		return nil, notFound(err, artist.ErrArtistNotFound)
	}
	if !root.ContentDocumentID.Valid {
		return nil, nil
	}
	return &root.ContentDocumentID.String, nil
}

func (s *store) SourceLocale(ctx context.Context, artistID string, lock artist.RowLock) (string, error) {
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	query := withLock(s.tx.WithContext(ctx).Table("artist").Select("source_locale").Where("id = ?", artistID), lock)
	if err := query.Take(&state).Error; err != nil {
		return "", notFound(err, artist.ErrArtistNotFound)
	}
	return state.SourceLocale, nil
}

func (s *store) DocumentRoot(ctx context.Context, artistID string, lock artist.RowLock) (artist.DocumentRoot, error) {
	var root struct {
		ID         string             `gorm:"column:id"`
		Status     model.ArtistStatus `gorm:"column:status"`
		DocumentID *uuid.UUID         `gorm:"column:content_document_id"`
	}
	query := withLock(s.tx.WithContext(ctx).
		Table("artist").
		Select("id::text AS id", "status", "content_document_id").
		Where("id = ?::uuid", artistID), lock)
	if err := query.Take(&root).Error; err != nil {
		return artist.DocumentRoot{}, notFound(err, artist.ErrArtistNotFound)
	}
	return artist.DocumentRoot{ID: root.ID, Status: artist.Status(root.Status), DocumentID: root.DocumentID}, nil
}

func (s *store) DocumentMetadata(ctx context.Context, artistID string) (artist.DocumentMetadata, error) {
	var row model.Artist
	if err := s.tx.WithContext(ctx).
		Select("id", "real_name", "country_code", "website", "social_links", "slug", "parent_artist_id").
		Where("id = ?", artistID).
		Take(&row).Error; err != nil {
		return artist.DocumentMetadata{}, notFound(err, artist.ErrArtistNotFound)
	}
	return artist.DocumentMetadata{
		RealName: row.RealName, CountryCode: row.CountryCode, Website: row.Website,
		SocialLinks: row.SocialLinks, Slug: row.Slug, ParentArtistID: row.ParentArtistID,
	}, nil
}

// assign adds a changed nullable column to fields.
func assign(fields structured.Fields, column string, assignment entityparticipant.Assignment) {
	if !assignment.Changed {
		return
	}
	if assignment.Value == nil {
		fields[column] = nil
	} else {
		fields[column] = *assignment.Value
	}
}

func (s *store) UpdateDocumentMetadata(ctx context.Context, artistID string, change artist.MetadataChange, now time.Time) error {
	fields := structured.Fields{}
	assign(fields, "real_name", change.RealName)
	assign(fields, "slug", change.Slug)
	assign(fields, "country_code", change.CountryCode)
	assign(fields, "website", change.Website)
	assign(fields, artistTables.ParentColumn, change.Parent)
	if change.SetSocialLinks {
		fields["social_links"] = change.SocialLinks
	}
	fields["updated_at"] = now
	return s.tx.WithContext(ctx).Model(&model.Artist{}).Where("id = ?", artistID).Updates(fields).Error
}

func (s *store) LabelIDs(ctx context.Context, artistID string) ([]string, error) {
	var labelIDs []string
	if err := s.tx.WithContext(ctx).
		Table("artist_label").
		Select("label_id::text").
		Where("artist_id = ?", artistID).
		Order("label_id ASC").
		Scan(&labelIDs).Error; err != nil {
		return nil, err
	}
	return labelIDs, nil
}

func (s *store) LabelIDsInLinkOrder(ctx context.Context, artistID string) ([]string, error) {
	var linked []string
	if err := s.tx.WithContext(ctx).
		Table("artist_label").
		Select("label_id::text").
		Where("artist_id = ?", artistID).
		Order("sort_order ASC NULLS LAST, label_id ASC").
		Scan(&linked).Error; err != nil {
		return nil, err
	}
	return linked, nil
}

func (s *store) ReplaceLabelIDs(ctx context.Context, artistID string, labelIDs []string) error {
	if err := s.tx.WithContext(ctx).
		Where("artist_id = ?", artistID).
		Delete(&model.ArtistLabel{}).Error; err != nil {
		return err
	}
	for index, labelID := range labelIDs {
		if err := s.tx.WithContext(ctx).Exec(`
			INSERT INTO artist_label (artist_id, label_id, sort_order)
			VALUES (?::uuid, ?::uuid, ?)
		`, artistID, labelID, index).Error; err != nil {
			return err
		}
	}
	return nil
}

func (s *store) CheckResourceRoute(ctx context.Context, slug string) error {
	return s.deps.Runtime.EnsureResourceRouteAvailable(ctx, s.tx, "artist", "artists", slug)
}

func (s *store) LockResourceRoute(ctx context.Context, slug string) error {
	return s.deps.Runtime.EnsureResourceRouteAvailableInTx(ctx, s.tx, "artist", "artists", slug)
}

func (s *store) ResourceRouteAvailable(ctx context.Context, slug string) (bool, error) {
	return s.deps.Runtime.IsResourceRouteAvailable(ctx, s.tx, "artists", slug)
}

func (s *store) LocaleTitle(ctx context.Context, artistID, locale string, forUpdate bool) (artist.LocaleTitle, bool, error) {
	query := s.tx.WithContext(ctx).Table("artist_translation").
		Select("locale", "title", "updated_at").
		Where("entity_id = ?::uuid AND locale = ?", artistID, locale)
	if forUpdate {
		query = query.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})
	}
	var row struct {
		Locale    string    `gorm:"column:locale"`
		Title     *string   `gorm:"column:title"`
		UpdatedAt time.Time `gorm:"column:updated_at"`
	}
	if err := query.Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return artist.LocaleTitle{}, false, nil
		}
		return artist.LocaleTitle{}, false, err
	}
	return artist.LocaleTitle{Locale: row.Locale, Title: row.Title, UpdatedAt: row.UpdatedAt}, true, nil
}

func (s *store) LocaleTitleText(ctx context.Context, artistID, locale string) (string, error) {
	var row struct {
		Title string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).
		Table("artist_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", artistID, locale).
		Take(&row).Error; err != nil {
		return "", notFound(err, artist.ErrLocaleNotFound)
	}
	return row.Title, nil
}

func (s *store) LocaleTitleValue(ctx context.Context, artistID, locale string) (*string, bool, error) {
	var row struct {
		Title *string `gorm:"column:title"`
	}
	result := s.tx.WithContext(ctx).
		Table("artist_translation").
		Select("title").
		Where("entity_id = ? AND locale = ?", artistID, locale).
		Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if result.Error != nil {
		return nil, false, result.Error
	}
	return row.Title, true, nil
}

func (s *store) LocaleExists(ctx context.Context, artistID, locale string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).Table("artist_translation").Where("entity_id = ?::uuid AND locale = ?", artistID, locale).Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) LocaleOgAssetID(ctx context.Context, artistID, locale string) (*string, error) {
	var row struct {
		OgAssetID *string `gorm:"column:og_asset_id"`
	}
	if err := s.tx.WithContext(ctx).Table("artist_translation").
		Select("og_asset_id").
		Where("entity_id = ? AND locale = ?", artistID, locale).
		Take(&row).Error; err != nil {
		return nil, notFound(err, artist.ErrLocaleNotFound)
	}
	return row.OgAssetID, nil
}

func (s *store) SaveLocaleTitle(ctx context.Context, artistID, locale, title string, now time.Time) error {
	return s.tx.WithContext(ctx).Exec(
		`INSERT INTO artist_translation (
			 entity_id, locale, title, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (entity_id, locale) DO UPDATE SET
			 title = EXCLUDED.title,
			 updated_at = EXCLUDED.updated_at`,
		artistID, locale, title, now, now,
	).Error
}

func (s *store) CreateTargetLocale(ctx context.Context, artistID, locale string, now time.Time) (bool, error) {
	created := s.tx.WithContext(ctx).Exec(
		"INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, NULL, ?, ?)",
		artistID, locale, now, now,
	)
	return created.RowsAffected == 1, created.Error
}

func (s *store) EnsureTargetLocale(ctx context.Context, artistID, locale string) (bool, error) {
	result := s.tx.WithContext(ctx).Exec("INSERT INTO artist_translation (entity_id, locale, title, created_at, updated_at) VALUES (?::uuid, ?, NULL, now(), now()) ON CONFLICT (entity_id, locale) DO NOTHING", artistID, locale)
	return result.RowsAffected != 0, result.Error
}

func (s *store) TouchTargetLocale(ctx context.Context, artistID, locale string, updatedAt time.Time) (bool, error) {
	updated := s.tx.WithContext(ctx).Table("artist_translation").
		Where("entity_id = ?::uuid AND locale = ?", artistID, locale).
		UpdateColumn("updated_at", updatedAt)
	return updated.RowsAffected == 1, updated.Error
}

func (s *store) SetTargetLocaleTitle(ctx context.Context, artistID, locale, title string, updatedAt time.Time) (bool, error) {
	updated := s.tx.WithContext(ctx).Table("artist_translation").
		Where("entity_id = ?::uuid AND locale = ?", artistID, locale).
		Updates(map[string]any{"title": title, "updated_at": updatedAt})
	return updated.RowsAffected == 1, updated.Error
}

func (s *store) DeleteTargetLocale(ctx context.Context, artistID, locale string) (bool, error) {
	deleted := s.tx.WithContext(ctx).Exec(
		"DELETE FROM artist_translation WHERE entity_id = ?::uuid AND locale = ?", artistID, locale,
	)
	return deleted.RowsAffected == 1, deleted.Error
}
