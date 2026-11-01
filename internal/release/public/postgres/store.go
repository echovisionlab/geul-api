// Package releasepublicpostgres is the PostgreSQL public.Store of the
// public Release reads, bound to one read snapshot.
package releasepublicpostgres

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/publiccontent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/release"
	releasepostgres "github.com/echovisionlab/geul-api/internal/release/postgres"
	"github.com/echovisionlab/geul-api/internal/release/public"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// Media resolves the ready public assets and Track derivatives the public
// reads deliver; the Release adapter implements it.
type Media interface {
	ReadySourceAssets(ctx context.Context, db *gorm.DB, fileIDs []string, kinds ...string) (map[string]*commonv1.AssetRef, error)
	TrackWaveforms(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]*commonv1.AssetRef, error)
	TrackSpectrograms(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]*commonv1.AssetRef, error)
	TrackHLS(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]*commonv1.HlsMediaRef, error)
}

// Ports are the adapters the public Store reads through.
type Ports struct {
	ContentBlocks *contentblock.Store
	Media         Media
	Artists       releasepostgres.ArtistSummaries
	Labels        releasepostgres.LabelSummaries
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"ContentBlocks": p.ContentBlocks == nil, "Media": p.Media == nil,
		"Artists": p.Artists == nil, "Labels": p.Labels == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		slices.Sort(missing)
		return errors.New("public release store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

// NewUnits returns the read-only units of the public Release reads.
func NewUnits(db *gorm.DB, ports Ports) (mutation.UnitOfWork[public.Store], error) {
	if db == nil {
		return nil, errors.New("public release store database is required")
	}
	if err := ports.validate(); err != nil {
		return nil, err
	}
	return mutation.Postgres(db, func(tx *gorm.DB) public.Store { return NewStore(tx, ports) }), nil
}

// NewStore returns the public Store over tx.
func NewStore(tx *gorm.DB, ports Ports) public.Store {
	return store{tx: tx, ports: ports, release: releasepostgres.NewStore(tx, releasepostgres.Ports{
		ContentBlocks: ports.ContentBlocks, Artists: ports.Artists, Labels: ports.Labels,
	})}
}

type store struct {
	tx      *gorm.DB
	ports   Ports
	release release.Store
}

var _ public.Store = store{}

func (s store) FindRelease(ctx context.Context, slugOrID string, byID bool) (release.Release, error) {
	if byID {
		return s.release.FindRelease(ctx, slugOrID)
	}
	var row model.Release
	if err := s.tx.WithContext(ctx).First(&row, "slug = ?", slugOrID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return release.Release{}, release.ErrReleaseNotFound
		}
		return release.Release{}, err
	}
	return releasepostgres.ReleaseFromRow(row), nil
}

func (s store) FindShareLink(ctx context.Context, token, releaseID string, now time.Time) (public.ShareLink, error) {
	var link model.ShareLink
	if err := s.tx.WithContext(ctx).Where(
		"token = ? AND entity_type = ? AND entity_id = ? AND expires_at > ?",
		token, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_RELEASE.String(), releaseID, now,
	).Take(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return public.ShareLink{}, public.ErrShareLinkNotFound
		}
		return public.ShareLink{}, err
	}
	return public.ShareLink{
		ID: link.ID, EntityType: link.EntityType, EntityID: link.EntityID,
		PasswordHash: link.PasswordHash, ExpiresAt: link.ExpiresAt,
	}, nil
}

var releaseFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{releasepostgres.ReleaseSourceTitleSQL("release")}},
		"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			string(release.StatusPublished),
		}},
		"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
			string(release.TypeAlbum), string(release.TypeEP),
			string(release.TypeSingle), string(release.TypeCompilation),
		}},
		"release_date": {Column: "release_date", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
		"published_at": {Column: "published_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps},
	},
}

var releaseSortConfig = &queryutil.SortConfig{
	AllowedFields: map[string]string{"title": releasepostgres.ReleaseSourceTitleSQL("release"), "release_date": "release_date", "published_at": "published_at"},
	DefaultSort:   "release_date DESC NULLS LAST",
}

// relationTables are the relation table and column of each relation a list
// filters on.
var relationTables = map[public.Relation]struct{ table, column string }{
	public.RelationLabel:    {"release_label", "label_id"},
	public.RelationArtist:   {"release_artist", "artist_id"},
	public.RelationCategory: {"release_category", "category_id"},
}

func (s store) ListPublishedReleases(ctx context.Context, page public.ListPage) ([]release.Release, int64, error) {
	query := s.tx.WithContext(ctx).Model(&model.Release{}).Where("status = ?", model.ReleaseStatusPublished)
	for _, filter := range page.RelationFilters {
		relation := relationTables[filter.Relation]
		subquery := "id IN (SELECT release_id FROM " + relation.table + " WHERE " + relation.column
		if filter.One {
			query = query.Where(subquery+" = ?)", filter.IDs[0])
		} else {
			query = query.Where(subquery+" IN ?)", filter.IDs)
		}
	}
	filtered, err := releaseFilterConfig.ApplyFilters(query, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	sorted, err := releaseSortConfig.ApplySort(filtered, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.Release
	pagination := queryutil.PaginationParams{Limit: page.Limit, Offset: page.Offset}
	if err := queryutil.ApplyPagination(sorted, pagination).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	releases := make([]release.Release, len(rows))
	for i, row := range rows {
		releases[i] = releasepostgres.ReleaseFromRow(row)
	}
	return releases, total, nil
}

func (s store) SourceTitles(ctx context.Context, releaseIDs []string) (map[string]string, error) {
	result := make(map[string]string, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}
	var rows []struct {
		EntityID string `gorm:"column:entity_id"`
		Title    string `gorm:"column:title"`
	}
	if err := s.tx.WithContext(ctx).Table("release_translation AS localized").
		Select("localized.entity_id, localized.title").
		Joins(`JOIN release AS owner ON owner.id = localized.entity_id AND owner.source_locale = localized.locale`).
		Where("localized.entity_id IN ?", releaseIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, value := range rows {
		result[value.EntityID] = value.Title
	}
	return result, nil
}

var releaseLocalizationSpec = publiccontent.Spec{
	EntityType:   "release",
	TableName:    "release_translation",
	SelectClause: "locale, NULL::text AS title, NULL::text AS summary, NULL::jsonb AS content_json, NULL::text AS content_html, NULL::text AS content_text, NULL::uuid AS og_asset_id",
}

func (s store) ResolveLocalization(ctx context.Context, releaseID, acceptLanguage string) (publiccontent.Selection, error) {
	return publiccontent.Resolve(ctx, s.tx, releaseLocalizationSpec, releaseID, acceptLanguage)
}

func (s store) ContentDocument(ctx context.Context, releaseID, locale string) (release.PublicContentDocument, error) {
	return release.LoadPublicContentDocument(ctx, s.release, releaseID, locale)
}

func (s store) ArtworkFiles(ctx context.Context, releaseIDs []string) ([]public.ArtworkFile, error) {
	var rows []struct {
		ReleaseID string `gorm:"column:release_id"`
		FileID    string `gorm:"column:file_id"`
	}
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (release_id) release_id, file_id
		FROM release_file
		WHERE release_id IN ?
		ORDER BY release_id, sort_order ASC, created_at ASC
	`, releaseIDs).Scan(&rows).Error; err != nil {
		return nil, err
	}
	files := make([]public.ArtworkFile, len(rows))
	for i, row := range rows {
		files[i] = public.ArtworkFile{ReleaseID: row.ReleaseID, FileID: row.FileID}
	}
	return files, nil
}

func (s store) ReadySourceAssets(ctx context.Context, fileIDs []string, kinds ...string) (map[string]*commonv1.AssetRef, error) {
	return s.ports.Media.ReadySourceAssets(ctx, s.tx, fileIDs, kinds...)
}

func (s store) ReleaseArtists(ctx context.Context, releaseIDs []string) ([]public.ArtistRow, error) {
	var rows []struct {
		ReleaseID   string  `gorm:"column:release_id"`
		ArtistID    string  `gorm:"column:artist_id"`
		ImageFileID *string `gorm:"column:image_file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_artist").
		Select(`
			release_artist.release_id,
			release_artist.artist_id,
			artist_image.file_id AS image_file_id
		`).
		Joins(`LEFT JOIN LATERAL (
			SELECT af.file_id
			FROM artist_file AS af
			WHERE af.artist_id = release_artist.artist_id
			ORDER BY af.sort_order ASC, af.created_at ASC
			LIMIT 1
		) AS artist_image ON TRUE`).
		Where("release_artist.release_id IN ?", releaseIDs).
		Order("release_artist.release_id ASC, release_artist.sort_order ASC, release_artist.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	artists := make([]public.ArtistRow, len(rows))
	for i, row := range rows {
		artists[i] = public.ArtistRow{ReleaseID: row.ReleaseID, ArtistID: row.ArtistID, ImageFileID: row.ImageFileID}
	}
	return artists, nil
}

func (s store) ArtistSummaries(ctx context.Context, artistIDs []string) (map[string]release.ArtistSummary, error) {
	return s.release.ArtistSummaries(ctx, artistIDs)
}

func (s store) LabelSummaries(ctx context.Context, labelIDs []string) (map[string]release.LabelSummary, error) {
	return s.release.LabelSummaries(ctx, labelIDs)
}

func (s store) ReleaseLabels(ctx context.Context, releaseID string) ([]public.LabelRow, error) {
	var rows []struct {
		ID            string  `gorm:"column:id"`
		CatalogNumber *string `gorm:"column:catalog_number"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_label").
		Select(`
			release_label.label_id as id,
			release_label.catalog_number as catalog_number
		`).
		Where("release_label.release_id = ?", releaseID).
		Order("release_label.sort_order ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	labels := make([]public.LabelRow, len(rows))
	for i, row := range rows {
		labels[i] = public.LabelRow{ID: row.ID, CatalogNumber: row.CatalogNumber}
	}
	return labels, nil
}

func (s store) Genres(ctx context.Context, releaseID string) ([]release.Reference, error) {
	var rows []referenceRow
	if err := s.tx.WithContext(ctx).
		Table("release_genre").
		Select("genre.id as id, genre.name as name, genre.slug as slug").
		Joins("JOIN genre ON genre.id = release_genre.genre_id").
		Where("release_genre.release_id = ?", releaseID).
		Order("genre.name ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return references(rows), nil
}

func (s store) Styles(ctx context.Context, releaseID string) ([]release.Reference, error) {
	var rows []referenceRow
	if err := s.tx.WithContext(ctx).
		Table("release_style").
		Select("style.id as id, style.name as name, style.slug as slug").
		Joins("JOIN style ON style.id = release_style.style_id").
		Where("release_style.release_id = ?", releaseID).
		Order("style.name ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	return references(rows), nil
}

type referenceRow struct {
	ID   string `gorm:"column:id"`
	Name string `gorm:"column:name"`
	Slug string `gorm:"column:slug"`
}

func references(rows []referenceRow) []release.Reference {
	references := make([]release.Reference, len(rows))
	for i, row := range rows {
		references[i] = release.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug}
	}
	return references
}

func (s store) Formats(ctx context.Context, releaseID string) ([]release.FormatReference, error) {
	var rows []struct {
		ID          string  `gorm:"column:id"`
		Name        string  `gorm:"column:name"`
		Slug        string  `gorm:"column:slug"`
		Description *string `gorm:"column:description"`
	}
	if err := s.tx.WithContext(ctx).
		Table("release_format").
		Select(`
			format.id as id,
			format.name as name,
			format.slug as slug,
			release_format.format_description as description
		`).
		Joins("JOIN format ON format.id = release_format.format_id").
		Where("release_format.release_id = ?", releaseID).
		Order("format.name ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	formats := make([]release.FormatReference, len(rows))
	for i, row := range rows {
		formats[i] = release.FormatReference{
			Reference: release.Reference{ID: row.ID, Name: row.Name, Slug: row.Slug}, Description: row.Description,
		}
	}
	return formats, nil
}

func (s store) Credits(ctx context.Context, releaseID string) ([]public.CreditRow, error) {
	var rows []struct {
		ID           string  `gorm:"column:id"`
		CreditedName *string `gorm:"column:credited_name"`
		CreditRole   *string `gorm:"column:credit_role"`
		ArtistID     *string `gorm:"column:artist_id"`
		MemberID     *string `gorm:"column:member_id"`
		ImageFileID  *string `gorm:"column:image_file_id"`
	}
	if err := s.tx.WithContext(ctx).Raw(`
			SELECT
			rc.id,
			rc.credited_name,
			rc.credit_role,
			rc.artist_id,
			rc.member_id,
			afi.file_id AS image_file_id
		FROM release_credit rc
		LEFT JOIN (
			SELECT DISTINCT ON (artist_id) artist_id, file_id
			FROM artist_file
			ORDER BY artist_id, sort_order ASC, created_at ASC
		) afi ON afi.artist_id = rc.artist_id
		WHERE rc.release_id = ?
		ORDER BY rc.sort_order ASC, rc.created_at ASC
		`, releaseID).Scan(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]public.CreditRow, len(rows))
	for i, row := range rows {
		credits[i] = public.CreditRow{
			ID: row.ID, CreditedName: row.CreditedName, CreditRole: row.CreditRole,
			ArtistID: row.ArtistID, MemberID: row.MemberID, ImageFileID: row.ImageFileID,
		}
	}
	return credits, nil
}

func (s store) CreditNotes(ctx context.Context, releaseID, locale string) (map[string]string, error) {
	return s.release.CreditNotes(ctx, releaseID, locale)
}

func (s store) Tracks(ctx context.Context, releaseID string) ([]release.Track, error) {
	return s.release.Tracks(ctx, releaseID)
}

func (s store) TrackFiles(ctx context.Context, fileIDs []string) (map[string]public.MediaFile, error) {
	result := make(map[string]public.MediaFile)
	ids := sliceutil.UniqueNonZero(fileIDs)
	if len(ids) == 0 {
		return result, nil
	}
	var rows []struct {
		ID        string  `gorm:"column:id"`
		Extension string  `gorm:"column:extension"`
		MimeType  string  `gorm:"column:mime_type"`
		FileSize  int64   `gorm:"column:file_size"`
		FileName  *string `gorm:"column:file_name"`
	}
	if err := s.tx.WithContext(ctx).
		Table("file").
		Select("id", "extension", "mime_type", "file_size", "file_name").
		Where("id IN ? AND delete_requested_at IS NULL", ids).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		if extension := strings.ToLower(strings.TrimSpace(row.Extension)); extension != "" {
			result[row.ID] = public.MediaFile{
				ID: row.ID, Extension: extension, MimeType: row.MimeType, FileSize: row.FileSize, FileName: row.FileName,
			}
		}
	}
	return result, nil
}

func (s store) TrackWaveforms(ctx context.Context, fileIDs []string) (map[string]*commonv1.AssetRef, error) {
	return s.ports.Media.TrackWaveforms(ctx, s.tx, fileIDs)
}

func (s store) TrackSpectrograms(ctx context.Context, fileIDs []string) (map[string]*commonv1.AssetRef, error) {
	return s.ports.Media.TrackSpectrograms(ctx, s.tx, fileIDs)
}

func (s store) TrackHLS(ctx context.Context, fileIDs []string) (map[string]*commonv1.HlsMediaRef, error) {
	return s.ports.Media.TrackHLS(ctx, s.tx, fileIDs)
}

func (s store) TrackCredits(ctx context.Context, trackIDs []string) ([]public.TrackCreditRow, error) {
	var rows []struct {
		TrackID      string  `gorm:"column:track_id"`
		ID           string  `gorm:"column:id"`
		CreditedName *string `gorm:"column:credited_name"`
		RoleName     *string `gorm:"column:role_name"`
		ArtistID     *string `gorm:"column:artist_id"`
		ImageFileID  *string `gorm:"column:image_file_id"`
	}
	if err := s.tx.WithContext(ctx).
		Table("track_credit").
		Select(`
			track_credit.track_id,
			track_credit.id,
			track_credit.credited_name,
			track_credit.credit_role as role_name,
			track_credit.artist_id,
			artist_image.file_id AS image_file_id
		`).
		Joins(`LEFT JOIN LATERAL (
			SELECT af.file_id
			FROM artist_file AS af
			WHERE af.artist_id = track_credit.artist_id
			ORDER BY af.sort_order ASC, af.created_at ASC
			LIMIT 1
		) AS artist_image ON TRUE`).
		Where("track_credit.track_id IN ?", trackIDs).
		Order("track_credit.track_id ASC, track_credit.sort_order ASC, track_credit.created_at ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	credits := make([]public.TrackCreditRow, len(rows))
	for i, row := range rows {
		credits[i] = public.TrackCreditRow{
			TrackID: row.TrackID, ID: row.ID, CreditedName: row.CreditedName, RoleName: row.RoleName,
			ArtistID: row.ArtistID, ImageFileID: row.ImageFileID,
		}
	}
	return credits, nil
}
