package release

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// GetReleaseQuery names a Release.
type GetReleaseQuery struct {
	ID string
}

// ListReleasesQuery pages through every Release. A Limit of zero or less
// takes the default page of 20.
type ListReleasesQuery struct {
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
	Limit   int32
	Offset  int32
}

// ReleasePage is one page of Releases with their Track and credit counts.
type ReleasePage struct {
	Releases []*managev1.ReleaseWithStats
	Total    int64
	Limit    int32
	Offset   int32
}

// CheckReleaseSlugQuery asks whether Slug is free, ignoring the Release
// ExcludeReleaseID names.
type CheckReleaseSlugQuery struct {
	Slug             string
	ExcludeReleaseID *string
}

// GetRelease retrieves a release by ID for a Site Admin.
func (s *Service) GetRelease(ctx context.Context, query GetReleaseQuery) (*managev1.Release, error) {
	if err := requireReleaseAction(ctx, s.spiceDB, query.ID, releaseActionView); err != nil {
		return nil, err
	}
	var release model.Release
	if err := s.db.WithContext(ctx).Where("id = ?", query.ID).First(&release).Error; err != nil {
		return nil, missingAs(err, releaseNotFound(query.ID))
	}
	document, err := s.overlayReleaseSourceLocaleDocument(ctx, &release)
	if err != nil {
		return nil, err
	}
	artworkAsset, err := s.getReleaseArtworkAsset(ctx, release.ID)
	if err != nil {
		return nil, err
	}
	return toProtoRelease(&release, document, artworkAsset), nil
}

// ListReleasesAdmin returns a page of all releases with stats.
func (s *Service) ListReleasesAdmin(ctx context.Context, query ListReleasesQuery) (ReleasePage, error) {
	if err := requireReleaseList(ctx, s.spiceDB); err != nil {
		return ReleasePage{}, err
	}
	filtered, err := ReleaseAdminFilterConfig.ApplyFilters(
		applyReleaseLabelFilters(s.db.WithContext(ctx).Model(&model.Release{}), query.Filters),
		withoutReleaseLabelFilters(query.Filters),
	)
	if err != nil {
		return ReleasePage{}, err
	}
	page := ReleasePage{Limit: 20, Offset: query.Offset}
	if query.Limit > 0 {
		page.Limit = query.Limit
	}
	if err := filtered.Count(&page.Total).Error; err != nil {
		return ReleasePage{}, err
	}
	sorted, err := releaseSortConfig.ApplySort(filtered, query.Sorts)
	if err != nil {
		return ReleasePage{}, err
	}
	var releases []model.Release
	if err := sorted.Order("release.id ASC").Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&releases).Error; err != nil {
		return ReleasePage{}, err
	}
	page.Releases, err = s.releasesWithStats(ctx, releases)
	return page, err
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

func (s *Service) releasesWithStats(ctx context.Context, releases []model.Release) ([]*managev1.ReleaseWithStats, error) {
	documents, err := s.overlayReleaseSourceLocaleDocuments(ctx, releases)
	if err != nil {
		return nil, err
	}
	artworkAssets, err := s.loadReadyReleaseArtworkAssets(ctx, releases)
	if err != nil {
		return nil, err
	}
	releaseIDs := collectManageReleaseIDs(releases)
	trackCounts, err := s.countPerRelease(ctx, "track", releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get track counts: %w", err)
	}
	creditCounts, err := s.countPerRelease(ctx, "release_credit", releaseIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get credit counts: %w", err)
	}
	result := make([]*managev1.ReleaseWithStats, len(releases))
	for i := range releases {
		release := &releases[i]
		result[i] = &managev1.ReleaseWithStats{
			Release:     toProtoRelease(release, documents[i], artworkAssets[release.ID]),
			TrackCount:  trackCounts[release.ID],
			CreditCount: creditCounts[release.ID],
		}
	}
	return result, nil
}

// countPerRelease counts the rows of a Release-owned table per Release.
func (s *Service) countPerRelease(ctx context.Context, table string, releaseIDs []string) (map[string]int32, error) {
	counts := make(map[string]int32)
	if len(releaseIDs) == 0 {
		return counts, nil
	}
	var stats []struct {
		ReleaseID string
		Count     int32
	}
	if err := s.db.WithContext(ctx).
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

// CheckReleaseSlugAvailable checks if a slug is available (Site Admin only).
func (s *Service) CheckReleaseSlugAvailable(ctx context.Context, query CheckReleaseSlugQuery) (bool, error) {
	// Checking for a specific release requires edit permission on it; checking
	// for a new release requires the create permission.
	if query.ExcludeReleaseID != nil && *query.ExcludeReleaseID != "" {
		if err := requireReleaseAction(ctx, s.spiceDB, *query.ExcludeReleaseID, releaseActionEdit); err != nil {
			return false, err
		}
	} else if err := requireReleaseCreate(ctx, s.spiceDB); err != nil {
		return false, err
	}
	if err := pathslug.Validate(query.Slug); err != nil {
		return false, err
	}
	taken := s.db.WithContext(ctx).Model(&model.Release{}).Where("slug = ?", query.Slug)
	if query.ExcludeReleaseID != nil {
		taken = taken.Where("id != ?", *query.ExcludeReleaseID)
	}
	var count int64
	if err := taken.Count(&count).Error; err != nil {
		return false, err
	}
	if count != 0 {
		return false, nil
	}
	return routeregistry.IsResourceRouteAvailable(ctx, s.db, "releases", query.Slug)
}

func (s *Service) getReleaseArtworkAsset(ctx context.Context, releaseID string) (*commonv1.AssetRef, error) {
	var result struct {
		FileID string `gorm:"column:file_id"`
	}
	err := s.db.WithContext(ctx).
		Table("release_file rf").
		Select("rf.file_id").
		Where("rf.release_id = ?", releaseID).
		Order("rf.sort_order").
		Take(&result).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.assets.ReadyArtwork(ctx, s.db, result.FileID)
}

// releaseSortConfig defines allowed sort fields for releases
var releaseSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"title":          ReleaseSourceTitleSQL("release"),
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

func (s *Service) loadReadyReleaseArtworkAssets(
	ctx context.Context,
	releases []model.Release,
) (map[string]*commonv1.AssetRef, error) {
	type row struct {
		ReleaseID string `gorm:"column:release_id"`
		AssetID   string `gorm:"column:asset_id"`
	}

	releaseIDs := collectManageReleaseIDs(releases)
	result := make(map[string]*commonv1.AssetRef, len(releaseIDs))
	if len(releaseIDs) == 0 {
		return result, nil
	}

	var rows []row
	if err := s.db.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (rf.release_id)
			rf.release_id,
			pa.id AS asset_id
		FROM release_file AS rf
		JOIN public_asset AS pa
		  ON pa.source_file_id = rf.file_id
		 AND pa.kind = 'artwork'
		 AND pa.status = 'ready'
		WHERE rf.release_id IN ?
		ORDER BY rf.release_id, rf.sort_order ASC, pa.created_at DESC, pa.id DESC
	`, releaseIDs).Find(&rows).Error; err != nil {
		return nil, err
	}

	candidates := make([]*string, 0, len(rows))
	for i := range rows {
		candidates = append(candidates, &rows[i].AssetID)
	}
	assets, err := s.assets.ReadyAssets(ctx, s.db, candidates...)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.ReleaseID] = assets[row.AssetID]
	}
	return result, nil
}

// toProtoRelease converts a model.Release and its content document to
// protobuf Release. The artwork is the Release's Open Graph image as well.
func toProtoRelease(r *model.Release, document *contentv1.RichTextDocument, artworkAsset *commonv1.AssetRef) *managev1.Release {
	release := &managev1.Release{
		Id:              r.ID,
		Title:           r.Title,
		Type:            releaseTypeToProto(r.Type),
		Document:        document,
		SourceLocale:    r.SourceLocale,
		Status:          string(r.Status),
		CreatedAt:       timestamppb.New(r.CreatedAt),
		UpdatedAt:       timestamppb.New(r.UpdatedAt),
		OgAsset:         artworkAsset,
		ArtworkAsset:    artworkAsset,
		Revision:        r.ContentRevision,
		Slug:            r.Slug,
		ReleaseDate:     timestampProtoPtr(r.ReleaseDate),
		PublishedAt:     timestampProtoPtr(r.PublishedAt),
		SpotifyUrl:      r.SpotifyURL,
		AppleMusicUrl:   r.AppleMusicURL,
		BandcampUrl:     r.BandcampURL,
		YoutubeMusicUrl: r.YoutubeMusicURL,
		CatalogNumber:   r.CatalogNumber,
	}
	return release
}

// overlayReleaseSourceLocaleDocument rereads release with its source title
// and revision in one snapshot and returns its content document.
func (s *Service) overlayReleaseSourceLocaleDocument(
	ctx context.Context,
	release *model.Release,
) (*contentv1.RichTextDocument, error) {
	releaseID := release.ID
	projection, err := loadReleaseManageContentProjection(
		ctx, s.db, s.contentBlocks, releaseID, s.og.LocaleAware(),
		func(ctx context.Context, tx *gorm.DB) error {
			var current model.Release
			if err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: clause.LockingStrengthShare}).
				Where("id = ?", releaseID).
				Take(&current).Error; err != nil {
				return err
			}
			*release = current
			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	release.Title = projection.SourceTitle
	release.ContentRevision = projection.Revision
	return projection.Document, nil
}

func (s *Service) overlayReleaseSourceLocaleDocuments(
	ctx context.Context,
	releases []model.Release,
) ([]*contentv1.RichTextDocument, error) {
	documents := make([]*contentv1.RichTextDocument, len(releases))
	for i := range releases {
		var err error
		if documents[i], err = s.overlayReleaseSourceLocaleDocument(ctx, &releases[i]); err != nil {
			return nil, err
		}
	}
	return documents, nil
}
