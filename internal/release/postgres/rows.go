package releasepostgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// ReleaseFromRow maps a release row to the domain Release, for the public
// Release reads.
func ReleaseFromRow(row model.Release) release.Release {
	return release.Release{
		ID: row.ID, ContentDocumentID: row.ContentDocumentID, SourceLocale: row.SourceLocale, Slug: row.Slug,
		Type: release.Type(row.Type), CatalogNumber: row.CatalogNumber, ReleaseDate: row.ReleaseDate,
		SpotifyURL: row.SpotifyURL, AppleMusicURL: row.AppleMusicURL, BandcampURL: row.BandcampURL,
		YoutubeMusicURL: row.YoutubeMusicURL, OgAssetID: row.OgAssetID, Status: release.Status(row.Status),
		PublishedAt: row.PublishedAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func releaseRow(value release.Release) model.Release {
	return model.Release{
		ID: value.ID, ContentDocumentID: value.ContentDocumentID, SourceLocale: value.SourceLocale, Slug: value.Slug,
		Type: model.ReleaseType(value.Type), CatalogNumber: value.CatalogNumber, ReleaseDate: value.ReleaseDate,
		SpotifyURL: value.SpotifyURL, AppleMusicURL: value.AppleMusicURL, BandcampURL: value.BandcampURL,
		YoutubeMusicURL: value.YoutubeMusicURL, OgAssetID: value.OgAssetID, Status: model.ReleaseStatus(value.Status),
		PublishedAt: value.PublishedAt, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func (s *store) FindRelease(ctx context.Context, releaseID string) (release.Release, error) {
	var row model.Release
	if err := s.tx.WithContext(ctx).Where("id = ?", releaseID).First(&row).Error; err != nil {
		return release.Release{}, notFoundAs(err, release.ErrReleaseNotFound)
	}
	return ReleaseFromRow(row), nil
}

func (s *store) LockRelease(ctx context.Context, releaseID string, lock release.RowLock) (release.Release, error) {
	var row model.Release
	if err := withLock(s.tx.WithContext(ctx), lock).First(&row, "id = ?", releaseID).Error; err != nil {
		return release.Release{}, notFoundAs(err, release.ErrReleaseNotFound)
	}
	return ReleaseFromRow(row), nil
}

func (s *store) ReleaseExists(ctx context.Context, releaseID string) error {
	var row model.Release
	err := s.tx.WithContext(ctx).Select("id").First(&row, "id = ?", releaseID).Error
	return notFoundAs(err, release.ErrReleaseNotFound)
}

// releaseSourceTitleSQL is the source-locale title of the Release aliased
// tableAlias, empty when it has none.
func releaseSourceTitleSQL(tableAlias string) string {
	return fmt.Sprintf(
		"COALESCE((SELECT rt.title FROM release_translation AS rt JOIN release AS owner ON owner.id = rt.entity_id AND owner.source_locale = rt.locale WHERE rt.entity_id = %s.id LIMIT 1), '')",
		tableAlias,
	)
}

// ReleaseSourceTitleSQL is releaseSourceTitleSQL for the public Release
// reads.
func ReleaseSourceTitleSQL(tableAlias string) string {
	return releaseSourceTitleSQL(tableAlias)
}

var releaseAdminFilterConfig = &queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{releaseSourceTitleSQL("release")}},
	"type": {Column: "type", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(release.TypeAlbum), string(release.TypeSingle),
		string(release.TypeEP), string(release.TypeCompilation),
	}},
	"status": {Column: "status", Type: queryutil.TypeEnum, AllowedOps: queryutil.EnumOps, EnumValues: []string{
		string(release.StatusDraft), string(release.StatusPublished),
	}},
}}

// releaseSortConfig defines allowed sort fields for releases
var releaseSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":          releaseSourceTitleSQL("release"),
		"slug":           "slug",
		"type":           "type",
		"status":         "status",
		"release_date":   "release_date",
		"catalog_number": "catalog_number",
		"created_at":     "created_at",
		"updated_at":     "updated_at",
		"published_at":   "published_at",
	},
	DefaultSort: "created_at DESC",
}

func (s *store) ListReleases(ctx context.Context, page release.ListPage) ([]release.Release, int64, error) {
	filtered, err := releaseAdminFilterConfig.ApplyFilters(
		applyReleaseLabelFilters(s.tx.WithContext(ctx).Model(&model.Release{}), page.Filters),
		withoutReleaseLabelFilters(page.Filters),
	)
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
	if err := sorted.Order("release.id ASC").Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	releases := make([]release.Release, len(rows))
	for i, row := range rows {
		releases[i] = ReleaseFromRow(row)
	}
	return releases, total, nil
}

// applyReleaseLabelFilters narrows query to Releases of the filtered Labels;
// label_id joins the relation, which FilterConfig cannot express.
func applyReleaseLabelFilters(query *gorm.DB, filters []*commonv1.FilterSpec) *gorm.DB {
	for _, filter := range filters {
		if filter == nil || filter.GetField() != "label_id" {
			continue
		}
		if filter.GetOp() == commonv1.FilterOp_FILTER_OP_EQ && filter.GetValue() != "" {
			query = query.Where("id IN (SELECT release_id FROM release_label WHERE label_id = ?)", filter.GetValue())
		} else if filter.GetOp() == commonv1.FilterOp_FILTER_OP_IN && len(filter.GetValues()) > 0 {
			query = query.Where("id IN (SELECT release_id FROM release_label WHERE label_id IN ?)", filter.GetValues())
		}
	}
	return query
}

func withoutReleaseLabelFilters(filters []*commonv1.FilterSpec) []*commonv1.FilterSpec {
	var remaining []*commonv1.FilterSpec
	for _, filter := range filters {
		if filter != nil && filter.GetField() != "label_id" {
			remaining = append(remaining, filter)
		}
	}
	return remaining
}

func (s *store) SlugTaken(ctx context.Context, slug string, excludeID *string) (bool, error) {
	taken := s.tx.WithContext(ctx).Model(&model.Release{}).Where("slug = ?", slug)
	if excludeID != nil {
		taken = taken.Where("id != ?", *excludeID)
	}
	var count int64
	if err := taken.Count(&count).Error; err != nil {
		return false, err
	}
	return count != 0, nil
}

func (s *store) InsertRelease(ctx context.Context, value *release.Release) error {
	row := releaseRow(*value)
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Returning{Columns: []clause.Column{{Name: "id"}, {Name: "created_at"}, {Name: "updated_at"}}}).
		Create(&row).Error; err != nil {
		return slugTaken(err)
	}
	value.ID, value.CreatedAt, value.UpdatedAt = row.ID, row.CreatedAt, row.UpdatedAt
	return nil
}

func (s *store) UpdateReleaseSettings(ctx context.Context, releaseID string, settings release.Settings, now time.Time) error {
	result := s.tx.WithContext(ctx).Model(&model.Release{}).Where("id = ?", releaseID).Updates(settingsColumns(settings, now))
	if result.Error != nil {
		return slugTaken(result.Error)
	}
	if result.RowsAffected == 0 {
		return sentinelError{error: gorm.ErrRecordNotFound, sentinel: release.ErrReleaseNotFound}
	}
	return nil
}

// settingsColumns is the Release row update for the fields the settings
// set.
func settingsColumns(u release.Settings, updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	if u.Slug != nil {
		columns["slug"] = *u.Slug
	}
	if u.Type != nil {
		columns["type"] = string(*u.Type)
	}
	if u.ReleaseDateSet {
		columns["release_date"] = nil
		if u.ReleaseDate != nil {
			columns["release_date"] = *u.ReleaseDate
		}
	}
	for column, value := range map[string]*string{
		"spotify_url":       u.SpotifyURL,
		"apple_music_url":   u.AppleMusicURL,
		"bandcamp_url":      u.BandcampURL,
		"youtube_music_url": u.YoutubeMusicURL,
		"catalog_number":    u.CatalogNumber,
	} {
		if value != nil {
			columns[column] = *value
		}
	}
	return columns
}

func (s *store) SetReleaseStatus(ctx context.Context, releaseID string, status release.Status, publishedAt *time.Time, now time.Time) error {
	columns := structured.Fields{"status": string(status), "updated_at": now}
	if publishedAt != nil {
		columns["published_at"] = *publishedAt
	}
	return s.tx.WithContext(ctx).Model(&model.Release{}).Where("id = ?", releaseID).Updates(columns).Error
}

func (s *store) TouchRelease(ctx context.Context, releaseID string, now time.Time) error {
	result := s.tx.WithContext(ctx).Model(&model.Release{}).Where("id = ?", releaseID).Updates(structured.Fields{"updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sentinelError{error: gorm.ErrRecordNotFound, sentinel: release.ErrReleaseNotFound}
	}
	return nil
}

func (s *store) DeleteRelease(ctx context.Context, releaseID string) error {
	result := s.tx.WithContext(ctx).Delete(&model.Release{}, "id = ?", releaseID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return sentinelError{error: gorm.ErrRecordNotFound, sentinel: release.ErrReleaseNotFound}
	}
	return nil
}

func (s *store) ContentDocumentID(ctx context.Context, releaseID string, lock release.RowLock) (*uuid.UUID, error) {
	var root struct {
		ID                string         `gorm:"column:id"`
		ContentDocumentID sql.NullString `gorm:"column:content_document_id"`
	}
	query := withLock(s.tx.WithContext(ctx).Table("release").Select("id", "content_document_id").Where("id = ?", releaseID), lock)
	if err := query.Take(&root).Error; err != nil {
		return nil, notFoundAs(err, release.ErrReleaseNotFound)
	}
	if !root.ContentDocumentID.Valid || root.ContentDocumentID.String == "" {
		return nil, nil
	}
	documentID, err := uuid.Parse(root.ContentDocumentID.String)
	if err != nil {
		return nil, fmt.Errorf("invalid release content_document_id: %w", err)
	}
	return &documentID, nil
}

func (s *store) SourceLocale(ctx context.Context, releaseID string, lock release.RowLock) (string, error) {
	var state struct {
		SourceLocale string `gorm:"column:source_locale"`
	}
	query := withLock(s.tx.WithContext(ctx).Table("release").Select("source_locale").Where("id = ?", releaseID), lock)
	if err := query.Take(&state).Error; err != nil {
		return "", notFoundAs(err, release.ErrReleaseNotFound)
	}
	return state.SourceLocale, nil
}

func (s *store) DocumentRoot(ctx context.Context, releaseID string, lock release.RowLock) (release.DocumentRoot, error) {
	var root struct {
		ID           string     `gorm:"column:id"`
		Status       string     `gorm:"column:status"`
		SourceLocale string     `gorm:"column:source_locale"`
		DocumentID   *uuid.UUID `gorm:"column:content_document_id"`
	}
	query := withLock(s.tx.WithContext(ctx).
		Table("release").
		Select("id::text AS id", "status", "source_locale", "content_document_id").
		Where("id = ?::uuid", releaseID), lock)
	if err := query.Take(&root).Error; err != nil {
		return release.DocumentRoot{}, notFoundAs(err, release.ErrReleaseNotFound)
	}
	return release.DocumentRoot{
		ID: root.ID, Status: release.Status(root.Status), SourceLocale: root.SourceLocale, DocumentID: root.DocumentID,
	}, nil
}

func (s *store) CheckResourceRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailable(ctx, s.tx, "release", "releases", slug)
}

func (s *store) LockResourceRoute(ctx context.Context, slug string) error {
	return routeregistry.EnsureResourceRouteAvailableInTx(ctx, s.tx, "release", "releases", slug)
}

func (s *store) ResourceRouteAvailable(ctx context.Context, slug string) (bool, error) {
	return routeregistry.IsResourceRouteAvailable(ctx, s.tx, "releases", slug)
}

func (s *store) DeleteShareLinks(ctx context.Context, releaseID string) error {
	return s.ports.ShareLinks.DeleteEntityShareLinks(ctx, s.tx, releaseID)
}

func (s *store) CountTracksPerRelease(ctx context.Context, releaseIDs []string) (map[string]int32, error) {
	return s.countPerRelease(ctx, "track", releaseIDs)
}

func (s *store) CountCreditsPerRelease(ctx context.Context, releaseIDs []string) (map[string]int32, error) {
	return s.countPerRelease(ctx, "release_credit", releaseIDs)
}

// countPerRelease counts the rows of a Release-owned table per Release.
func (s *store) countPerRelease(ctx context.Context, table string, releaseIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32)
	if len(releaseIDs) == 0 {
		return counts, nil
	}
	var stats []struct {
		ReleaseID string
		Count     int32
	}
	if err := s.tx.WithContext(ctx).
		Table(table).
		Select("release_id, COUNT(*) as count").
		Where("release_id IN ?", releaseIDs).
		Group("release_id").
		Scan(&stats).Error; err != nil {
		return nil, err
	}
	for _, stat := range stats {
		counts[stat.ReleaseID] = stat.Count
	}
	return counts, nil
}

func (s *store) RequestOG(ctx context.Context, releaseID, reason string) error {
	return s.ports.ContentOG.RequestCurrent(ctx, s.tx, releaseID, reason)
}

var errInvalidRelation = errors.New("unsupported Release reference relation")
