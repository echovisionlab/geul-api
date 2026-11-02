// Package postgres is the PostgreSQL store of the public Artist reads.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	translationpostgres "github.com/echovisionlab/geul-api/internal/translation/postgres"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/artist"
	artistpostgres "github.com/echovisionlab/geul-api/internal/artist/postgres"
	artistpublic "github.com/echovisionlab/geul-api/internal/artist/public"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// NewUnits returns the read snapshots of the public Artist reads. The
// content document reads through the Artist store over the same
// transaction.
func NewUnits(db *gorm.DB, contentBlocks *contentblock.Store) (mutation.UnitOfWork[artistpublic.Store], error) {
	if db == nil || contentBlocks == nil {
		return nil, errors.New("public artist store database and content blocks are required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) artistpublic.Store {
		return store{tx: tx, artists: artistpostgres.NewStore(tx, artistpostgres.Ports{ContentBlocks: contentBlocks})}
	}), nil
}

type store struct {
	tx      *gorm.DB
	artists artist.Store
}

var _ artistpublic.Store = store{}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers artist.ErrArtistNotFound.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == artist.ErrArtistNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func artistNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{err}
	}
	return err
}

func artistFromRow(row model.Artist) artist.Artist {
	return artist.Artist{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale,
		Slug: row.Slug, RealName: row.RealName, ParentArtistID: row.ParentArtistID,
		CountryCode: row.CountryCode, Website: row.Website, SocialLinks: row.SocialLinks,
		Status: artist.Status(row.Status), PublishedAt: row.PublishedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s store) ListPublishedArtists(ctx context.Context, page artistpublic.ListPage) ([]artist.Artist, int64, error) {
	artists := s.tx.WithContext(ctx).Model(&model.Artist{}).Where("status = ?", model.ArtistStatusPublished)

	// 1. 조인 필터 직접 처리
	var remainingFilters []*commonv1.FilterSpec
	for _, f := range page.Filters {
		switch f.GetField() {
		case "label_id":
			switch f.GetOp() {
			case commonv1.FilterOp_FILTER_OP_EQ:
				artists = artists.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id = ?)", f.GetValue())
			case commonv1.FilterOp_FILTER_OP_IN:
				artists = artists.Where("id IN (SELECT artist_id FROM artist_label WHERE label_id IN ?)", f.GetValues())
			default:
				return nil, 0, domainerr.New(domainerr.Invalid, "", fmt.Sprintf("invalid filter operation '%s' for field '%s'", f.GetOp().String(), f.GetField()))
			}
		default:
			remainingFilters = append(remainingFilters, f)
		}
	}

	// 2. 나머지 필터 적용
	filtered, err := ArtistFilterConfig.ApplyFilters(artists, remainingFilters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	sorted, err := ArtistSortConfig.ApplySort(filtered, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.Artist
	if err := queryutil.ApplyPagination(sorted, queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset}).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	values := make([]artist.Artist, len(rows))
	for i, row := range rows {
		values[i] = artistFromRow(row)
	}
	return values, total, nil
}

func (s store) FindArtist(ctx context.Context, slugOrID string, byID bool) (artist.Artist, error) {
	var row model.Artist
	var err error
	if byID {
		err = s.tx.WithContext(ctx).First(&row, "id = ?", slugOrID).Error
	} else {
		err = s.tx.WithContext(ctx).First(&row, "slug = ?", slugOrID).Error
	}
	if err != nil {
		return artist.Artist{}, artistNotFound(err)
	}
	return artistFromRow(row), nil
}

func (s store) ShareLockArtist(ctx context.Context, artistID string) (artist.Artist, error) {
	var row model.Artist
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", artistID).
		Take(&row).Error; err != nil {
		return artist.Artist{}, artistNotFound(err)
	}
	return artistFromRow(row), nil
}

func (s store) SourceTitles(ctx context.Context, artistIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(artistIDs))
	if len(artistIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EntityID string `gorm:"column:entity_id"`
		Title    string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).Table("artist_translation AS localized").Select("localized.entity_id, localized.title").Joins(`JOIN artist ON artist.id = localized.entity_id AND artist.source_locale = localized.locale`).Where("localized.entity_id IN ?", artistIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.EntityID] = row.Title
	}
	return result, nil
}

func (s store) SourceOgAssetIDs(ctx context.Context, artistIDs []string) (map[string]*string, error) {
	result := make(map[string]*string, len(artistIDs))
	if len(artistIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EntityID  string  `gorm:"column:entity_id"`
		OgAssetID *string `gorm:"column:og_asset_id"`
	}
	if err := s.tx.WithContext(ctx).Table("artist_translation AS localized").Select("localized.entity_id, localized.og_asset_id").Joins(`JOIN artist ON artist.id = localized.entity_id AND artist.source_locale = localized.locale`).Where("localized.entity_id IN ?", artistIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.EntityID] = row.OgAssetID
	}
	return result, nil
}

func (s store) ResolveLocalization(ctx context.Context, artistID, acceptLanguage string) (publiccontent.Selection, error) {
	return publiccontent.Resolve(ctx, s.tx, artistLocalizationSpec, artistID, acceptLanguage, translationpostgres.NewRuntimeSettingsReader(s.tx))
}

func (s store) ResolveOGConsistency(ctx context.Context, artistID string, selection publiccontent.Selection, ready publiccontent.ReadyAsset) (publiccontent.Selection, error) {
	return publiccontent.ResolveOGConsistency(ctx, s.tx, artistLocalizationSpec, artistID, selection, ready)
}

func (s store) Document(ctx context.Context, artistID, locale string) (artist.PublicCreativeContentDocument, error) {
	return artist.LoadCreativeContentDocumentForPublic(ctx, artist.Joined{Store: s.artists}, artistID, locale)
}

func (s store) IsGroup(ctx context.Context, artistID string) (bool, error) {
	var count int64
	if err := s.tx.WithContext(ctx).
		Model(&model.Artist{}).
		Where("parent_artist_id = ?", artistID).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s store) PublishedReleaseCount(ctx context.Context, artistID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT release.id)
		FROM release_artist
		JOIN release ON release.id = release_artist.release_id
		WHERE release_artist.artist_id = ? AND release.status = ?
	`, artistID, model.ReleaseStatusPublished).Scan(&count).Error
	return count, err
}

func (s store) VisibleWorkCount(ctx context.Context, artistID string) (int64, error) {
	var count int64
	err := s.tx.WithContext(ctx).Raw(`
		SELECT COUNT(DISTINCT work.id)
		FROM work_credit
		JOIN work ON work.id = work_credit.work_id
		WHERE work_credit.artist_id = ? AND work.status IN (?, ?)
	`, artistID, model.WorkStatusPublished, model.WorkStatusArchived).Scan(&count).Error
	return count, err
}

func (s store) PublishedLabels(ctx context.Context, artistID string) ([]artistpublic.LabelRow, error) {
	var labels []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        *string `gorm:"column:slug"`
		ImageFileID *string `gorm:"column:image_file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("artist_label").
		Select("label.id, "+labelSourceTitleSQL("label")+" AS name, label.slug, label.logo_light_file_id AS image_file_id").
		Joins("JOIN label ON label.id = artist_label.label_id").
		Where("artist_label.artist_id = ? AND label.status = ?", artistID, model.LabelStatusPublished).
		Order(labelSourceTitleSQL("label") + " ASC").
		Limit(12).
		Scan(&labels).Error; err != nil {
		return nil, err
	}
	rows := make([]artistpublic.LabelRow, 0, len(labels))
	for _, label := range labels {
		rows = append(rows, artistpublic.LabelRow{ID: label.ID, Name: label.Name, Slug: label.Slug, ImageFileID: label.ImageFileID})
	}
	return rows, nil
}

// artistReleasesSortColumns are the columns of the Artist Releases sort
// fields.
var artistReleasesSortColumns = map[string]string{
	"release_date": "release.release_date",
	"published_at": "release.published_at",
	"title":        releaseSourceTitleSQL("release"),
}

// orderBy is the ORDER BY clause of sorts over columns, fallback when
// sorts is empty.
func orderBy(sorts []artistpublic.Sort, column func(string) string, fallback string) string {
	if len(sorts) == 0 {
		return fallback
	}
	orderParts := make([]string, 0, len(sorts))
	for _, sort := range sorts {
		order := "ASC"
		if sort.Descending {
			order = "DESC"
		}
		orderParts = append(orderParts, fmt.Sprintf("%s %s NULLS LAST", column(sort.Field), order))
	}
	return strings.Join(orderParts, ", ")
}

func (s store) PublishedReleases(ctx context.Context, artistID string, sorts []artistpublic.Sort, limit, offset int32) ([]artistpublic.ReleaseRow, error) {
	var releases []struct {
		ID          string     `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Slug        *string    `gorm:"column:slug"`
		Type        string     `gorm:"column:type"`
		ReleaseDate *time.Time `gorm:"column:release_date"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}
	order := orderBy(sorts, func(field string) string { return artistReleasesSortColumns[field] }, "release.release_date DESC NULLS LAST")
	if err := s.tx.WithContext(ctx).
		Table("release_artist").
		Select("DISTINCT release.id, "+releaseSourceTitleSQL("release")+" AS title, release.slug, release.type, release.release_date, release.published_at").
		Joins("JOIN release ON release.id = release_artist.release_id").
		Where("release_artist.artist_id = ? AND release.status = ?", artistID, model.ReleaseStatusPublished).
		Order(order).
		Limit(int(limit)).
		Offset(int(offset)).
		Scan(&releases).Error; err != nil {
		return nil, err
	}
	rows := make([]artistpublic.ReleaseRow, 0, len(releases))
	for _, release := range releases {
		rows = append(rows, artistpublic.ReleaseRow{
			ID: release.ID, Title: release.Title, Slug: release.Slug, Type: release.Type,
			ReleaseDate: release.ReleaseDate, PublishedAt: release.PublishedAt,
		})
	}
	return rows, nil
}

func (s store) ReleaseArtists(ctx context.Context, releaseIDs []string, excludeArtistID string) ([]artistpublic.ReleaseArtistRow, error) {
	var rows []artistpublic.ReleaseArtistRow
	if err := s.tx.WithContext(ctx).
		Table("release_artist").
		Select("release_artist.release_id::text, artist.id::text AS artist_id, "+artistSourceTitleSQL()+" AS artist_name, artist.slug AS artist_slug").
		Joins("JOIN artist ON artist.id = release_artist.artist_id").
		Where("release_artist.release_id IN ? AND release_artist.artist_id != ?::uuid", releaseIDs, excludeArtistID).
		Order("release_artist.release_id, release_artist.sort_order, release_artist.created_at, release_artist.artist_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (s store) VisibleWorks(ctx context.Context, artistID string, sorts []artistpublic.Sort, limit, offset int32) ([]artistpublic.WorkRow, error) {
	var works []struct {
		ID          string     `gorm:"column:id"`
		Title       string     `gorm:"column:title"`
		Slug        *string    `gorm:"column:slug"`
		Summary     *string    `gorm:"column:summary"`
		Type        string     `gorm:"column:type"`
		ImageFileID *string    `gorm:"column:image_file_id"`
		PublishedAt *time.Time `gorm:"column:published_at"`
	}
	order := orderBy(sorts, func(field string) string {
		if field == "title" {
			return workSourceTitleSQL("work")
		}
		return "work." + field
	}, "work.published_at DESC NULLS LAST")
	if err := s.tx.WithContext(ctx).
		Table("work_credit").
		Select("DISTINCT work.id, "+workSourceTitleSQL("work")+" AS title, "+workSourceSummarySQL("work")+" AS summary, work.slug, work.type, work.featured_image_file_id AS image_file_id, work.published_at").
		Joins("JOIN work ON work.id = work_credit.work_id").
		Where("work_credit.artist_id = ? AND work.status IN ?", artistID, []string{
			string(model.WorkStatusPublished), string(model.WorkStatusArchived),
		}).
		Order(order).
		Limit(int(limit)).
		Offset(int(offset)).
		Scan(&works).Error; err != nil {
		return nil, err
	}
	rows := make([]artistpublic.WorkRow, 0, len(works))
	for _, work := range works {
		rows = append(rows, artistpublic.WorkRow{
			ID: work.ID, Title: work.Title, Slug: work.Slug, Summary: work.Summary, Type: work.Type,
			ImageFileID: work.ImageFileID, PublishedAt: work.PublishedAt,
		})
	}
	return rows, nil
}

func (s store) WorkLocalizations(ctx context.Context, workIDs []string, acceptLanguage string) (map[string]publiccontent.Selection, error) {
	return publiccontent.ResolveBatch(ctx, s.tx, artistWorkLocalizationSpec, workIDs, acceptLanguage, translationpostgres.NewRuntimeSettingsReader(s.tx))
}

func (s store) ShareLink(ctx context.Context, token, artistID string, now time.Time) (*string, bool, error) {
	var link model.ShareLink
	if err := s.tx.WithContext(ctx).
		Where("token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?", token, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST.String(), artistID, now).
		Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return link.PasswordHash, true, nil
}
