// Package postgres is the PostgreSQL store of the public Label reads.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/echovisionlab/geul-api/internal/label"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	labelpublic "github.com/echovisionlab/geul-api/internal/label/public"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"

	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

// ContentProjection reads a Label's source titles and its content document
// in a locale.
type ContentProjection interface {
	LoadSourceTitles(context.Context, *gorm.DB, []string) (map[string]string, error)
	LoadDocument(context.Context, *gorm.DB, string, string) (labelpublic.ContentDocument, error)
}

// MediaHydrator resolves ready public assets.
type MediaHydrator interface {
	ReadyForSourceFiles(context.Context, *gorm.DB, []string, ...string) (map[string]*mediaasset.Reference, error)
	ReadyForAssetIDs(context.Context, *gorm.DB, []string) (map[string]*mediaasset.Reference, error)
	ResolveOg(context.Context, *gorm.DB, *string) (*mediaasset.Reference, error)
}

// NewUnits returns the read snapshots of the public Label reads.
func NewUnits(db *gorm.DB, content ContentProjection, media MediaHydrator) (mutation.UnitOfWork[labelpublic.Store], error) {
	if db == nil || content == nil || media == nil {
		return nil, errors.New("public label store database, content and media are required")
	}
	return mutation.Postgres(db, func(tx *gorm.DB) labelpublic.Store {
		return store{tx: tx, content: content, media: media}
	}), nil
}

type store struct {
	tx      *gorm.DB
	content ContentProjection
	media   MediaHydrator
}

var _ labelpublic.Store = store{}

// notFoundError keeps the driver's not-found error readable and in the
// chain while it answers label.ErrLabelNotFound.
type notFoundError struct{ error }

func (err notFoundError) Is(target error) bool { return target == label.ErrLabelNotFound }
func (err notFoundError) Unwrap() error        { return err.error }

func labelNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return notFoundError{err}
	}
	return err
}

func labelFromRow(row model.Label) label.Label {
	return label.Label{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale,
		Slug: row.Slug, CountryCode: row.CountryCode, Website: row.Website,
		LogoLightFileID: row.LogoLightFileID, LogoDarkFileID: row.LogoDarkFileID,
		SocialLinks: row.SocialLinks, ParentLabelID: row.ParentLabelID, OgAssetID: row.OgAssetID,
		Status: label.Status(row.Status), PublishedAt: row.PublishedAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s store) ListPublishedLabels(ctx context.Context, page label.ListPage) ([]label.Label, int64, error) {
	labels := s.tx.WithContext(ctx).Model(&model.Label{}).Where("status = ?", model.LabelStatusPublished)
	labels, err := LabelFilterConfig.ApplyFilters(labels, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := labels.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	labels, err = LabelSortConfig.ApplySort(labels, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.Label
	if err := queryutil.ApplyPagination(labels, queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset}).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	values := make([]label.Label, len(rows))
	for i, row := range rows {
		values[i] = labelFromRow(row)
	}
	return values, total, nil
}

func (s store) FindLabel(ctx context.Context, slugOrID string, byID bool) (label.Label, error) {
	var row model.Label
	var err error
	if byID {
		err = s.tx.WithContext(ctx).First(&row, "id = ?", slugOrID).Error
	} else {
		err = s.tx.WithContext(ctx).First(&row, "slug = ?", slugOrID).Error
	}
	if err != nil {
		return label.Label{}, labelNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s store) ShareLockLabel(ctx context.Context, labelID string) (label.Label, error) {
	var row model.Label
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
		Where("id = ?", labelID).
		Take(&row).Error; err != nil {
		return label.Label{}, labelNotFound(err)
	}
	return labelFromRow(row), nil
}

func (s store) ResolveLocalization(ctx context.Context, labelID, acceptLanguage string) (publiccontent.Selection, error) {
	return publiccontent.Resolve(ctx, s.tx, labelLocalizationSpec, labelID, acceptLanguage)
}

func (s store) SourceTitles(ctx context.Context, labelIDs []string) (map[string]string, error) {
	return s.content.LoadSourceTitles(ctx, s.tx, labelIDs)
}

func (s store) Document(ctx context.Context, labelID, locale string) (labelpublic.ContentDocument, error) {
	return s.content.LoadDocument(ctx, s.tx, labelID, locale)
}

func (s store) ReadySourceFiles(ctx context.Context, fileIDs []string, kinds ...string) (map[string]*mediaasset.Reference, error) {
	return s.media.ReadyForSourceFiles(ctx, s.tx, fileIDs, kinds...)
}

func (s store) ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	return s.media.ReadyForAssetIDs(ctx, s.tx, assetIDs)
}

func (s store) ResolveOg(ctx context.Context, assetID *string) (*mediaasset.Reference, error) {
	return s.media.ResolveOg(ctx, s.tx, assetID)
}

func (s store) RelationCounts(ctx context.Context, labelID string) (int32, int32, error) {
	var counts struct {
		ReleaseCount int32 `gorm:"column:release_count"`
		ArtistCount  int32 `gorm:"column:artist_count"`
	}
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT
			(SELECT COUNT(*) FROM release_label JOIN release ON release.id = release_label.release_id
			 WHERE release_label.label_id = ?::uuid AND release.status = ?)::integer AS release_count,
			(SELECT COUNT(*) FROM artist_label JOIN artist ON artist.id = artist_label.artist_id
			 WHERE artist_label.label_id = ?::uuid AND artist.status = ?)::integer AS artist_count
	`, labelID, model.ReleaseStatusPublished, labelID, model.ArtistStatusPublished).Scan(&counts).Error; err != nil {
		return 0, 0, err
	}
	return counts.ReleaseCount, counts.ArtistCount, nil
}

func (s store) PublishedParent(ctx context.Context, labelID string) (labelpublic.ParentRow, bool, error) {
	var parent struct {
		ID              string  `gorm:"column:id"`
		Name            string  `gorm:"column:name"`
		Slug            *string `gorm:"column:slug"`
		LogoLightFileID *string `gorm:"column:logo_light_file_id"`
		LogoDarkFileID  *string `gorm:"column:logo_dark_file_id"`
		OgAssetID       *string `gorm:"column:og_asset_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("label").
		Select("id, "+labelSourceTitleSQL("label")+" AS name, slug, logo_light_file_id, logo_dark_file_id, og_asset_id").
		Where("id = ? AND status = ?", labelID, model.LabelStatusPublished).
		Scan(&parent).Error; err != nil {
		return labelpublic.ParentRow{}, false, err
	}
	return labelpublic.ParentRow{
		ID: parent.ID, Name: parent.Name, Slug: parent.Slug,
		LogoLightFileID: parent.LogoLightFileID, LogoDarkFileID: parent.LogoDarkFileID, OgAssetID: parent.OgAssetID,
	}, parent.ID != "", nil
}

func (s store) PublishedArtists(ctx context.Context, labelID string) ([]labelpublic.ArtistRow, error) {
	var rows []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        *string `gorm:"column:slug"`
		ImageFileID *string `gorm:"column:image_file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("artist_label").
		Select("artist.id, "+artistSourceTitleSQL("artist")+" AS name, artist.slug, artist_image.file_id AS image_file_id").
		Joins("JOIN artist ON artist.id = artist_label.artist_id").
		Joins(`
			LEFT JOIN LATERAL (
				SELECT artist_file.file_id
				FROM artist_file
				WHERE artist_file.artist_id = artist.id
				ORDER BY artist_file.sort_order ASC, artist_file.created_at ASC
				LIMIT 1
			) artist_image ON TRUE
		`).
		Where("artist_label.label_id = ? AND artist.status = ?", labelID, model.ArtistStatusPublished).
		Order(artistSourceTitleSQL("artist") + " ASC").
		Limit(12).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	artists := make([]labelpublic.ArtistRow, 0, len(rows))
	for _, row := range rows {
		artists = append(artists, labelpublic.ArtistRow{ID: row.ID, Name: row.Name, Slug: row.Slug, ImageFileID: row.ImageFileID})
	}
	return artists, nil
}

func (s store) PublishedReleases(ctx context.Context, labelID string) ([]labelpublic.ReleaseRow, error) {
	var rows []struct {
		ID            string     `gorm:"column:id"`
		Title         string     `gorm:"column:title"`
		Slug          *string    `gorm:"column:slug"`
		Type          string     `gorm:"column:type"`
		ReleaseDate   *time.Time `gorm:"column:release_date"`
		PublishedAt   *time.Time `gorm:"column:published_at"`
		ArtworkFileID *string    `gorm:"column:artwork_file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_label").
		Select("release.id, "+releaseSourceTitleSQL("release")+" AS title, release.slug, release.type, release.release_date, release.published_at, artwork.file_id AS artwork_file_id").
		Joins("JOIN release ON release.id = release_label.release_id").
		Joins(`LEFT JOIN LATERAL (
			SELECT release_file.file_id
			FROM release_file
			WHERE release_file.release_id = release.id
			ORDER BY release_file.sort_order ASC
			LIMIT 1
		) artwork ON TRUE`).
		Where("release_label.label_id = ? AND release.status = ?", labelID, model.ReleaseStatusPublished).
		Order("release.release_date DESC NULLS LAST").
		Order("release.published_at DESC").
		Limit(12).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	releases := make([]labelpublic.ReleaseRow, 0, len(rows))
	for _, row := range rows {
		releases = append(releases, labelpublic.ReleaseRow{
			ID: row.ID, Title: row.Title, Slug: row.Slug, Type: row.Type,
			ReleaseDate: row.ReleaseDate, PublishedAt: row.PublishedAt, ArtworkFileID: row.ArtworkFileID,
		})
	}
	return releases, nil
}

func (s store) ReleaseArtists(ctx context.Context, releaseIDs []string) ([]labelpublic.ReleaseArtistRow, error) {
	var rows []struct {
		ReleaseID  string  `gorm:"column:release_id"`
		ArtistID   string  `gorm:"column:artist_id"`
		ArtistName string  `gorm:"column:artist_name"`
		ArtistSlug *string `gorm:"column:artist_slug"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_artist").
		Select("release_artist.release_id, artist.id AS artist_id, "+artistSourceTitleSQL("artist")+" AS artist_name, artist.slug AS artist_slug").
		Joins("JOIN artist ON artist.id = release_artist.artist_id").
		Where("release_artist.release_id IN ?", releaseIDs).
		Order("release_artist.release_id, release_artist.sort_order ASC, release_artist.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	artists := make([]labelpublic.ReleaseArtistRow, 0, len(rows))
	for _, row := range rows {
		artists = append(artists, labelpublic.ReleaseArtistRow{ReleaseID: row.ReleaseID, ArtistID: row.ArtistID, ArtistName: row.ArtistName, ArtistSlug: row.ArtistSlug})
	}
	return artists, nil
}
