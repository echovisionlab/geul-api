package release

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/routeregistry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ReleaseService implements the ReleaseService Connect handler
func (s *ReleaseService) CheckReleaseSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckReleaseSlugAvailableRequest],
) (*connect.Response[managev1.CheckReleaseSlugAvailableResponse], error) {
	// Require edit permission if checking for a specific release
	if req.Msg.ExcludeReleaseId != nil && *req.Msg.ExcludeReleaseId != "" {
		if err := requireReleaseAction(ctx, s.spiceDB, *req.Msg.ExcludeReleaseId, releaseActionEdit); err != nil {
			return nil, err
		}
	} else {
		// For new releases, require admin role
		if err := requireReleaseCreate(ctx, s.spiceDB); err != nil {
			return nil, err
		}
	}
	if err := validateSlugWithoutSlash(req.Msg.Slug); err != nil {
		return nil, err
	}

	var count int64

	query := s.db.WithContext(ctx).Model(&model.Release{}).Where("slug = ?", req.Msg.Slug)

	if req.Msg.ExcludeReleaseId != nil {
		query = query.Where("id != ?", *req.Msg.ExcludeReleaseId)
	}

	if err := query.Count(&count).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if count == 0 {
		available, err := routeregistry.IsResourceRouteAvailable(ctx, s.db, "releases", req.Msg.Slug)
		if err != nil {
			return nil, err
		}
		if !available {
			count = 1
		}
	}

	return connect.NewResponse(&managev1.CheckReleaseSlugAvailableResponse{
		Available: count == 0,
	}), nil
}

// =============================================================================
// Helper Methods
// =============================================================================

func (s *ReleaseService) getReleaseArtworkAsset(ctx context.Context, releaseID string) (*commonv1.AssetRef, error) {
	var result struct {
		FileID string `gorm:"column:file_id"`
	}

	err := s.db.WithContext(ctx).
		Table("release_file rf").
		Select("rf.file_id").
		Where("rf.release_id = ?", releaseID).
		Order("rf.sort_order").
		Take(&result).Error

	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errs.Internal(err)
	}

	asset, err := s.assets.ReadyArtwork(ctx, s.db, result.FileID)
	if err != nil {
		return nil, err
	}
	return asset, nil
}

func releaseTypeToString(t managev1.ReleaseType) string {
	switch t {
	case managev1.ReleaseType_RELEASE_TYPE_ALBUM:
		return "RELEASE_TYPE_ALBUM"
	case managev1.ReleaseType_RELEASE_TYPE_EP:
		return "RELEASE_TYPE_EP"
	case managev1.ReleaseType_RELEASE_TYPE_SINGLE:
		return "RELEASE_TYPE_SINGLE"
	case managev1.ReleaseType_RELEASE_TYPE_COMPILATION:
		return "RELEASE_TYPE_COMPILATION"
	default:
		return "RELEASE_TYPE_ALBUM"
	}
}

func stringToReleaseType(t string) managev1.ReleaseType {
	switch t {
	case "RELEASE_TYPE_ALBUM":
		return managev1.ReleaseType_RELEASE_TYPE_ALBUM
	case "RELEASE_TYPE_EP":
		return managev1.ReleaseType_RELEASE_TYPE_EP
	case "RELEASE_TYPE_SINGLE":
		return managev1.ReleaseType_RELEASE_TYPE_SINGLE
	case "RELEASE_TYPE_COMPILATION":
		return managev1.ReleaseType_RELEASE_TYPE_COMPILATION
	default:
		return managev1.ReleaseType_RELEASE_TYPE_ALBUM
	}
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

func (s *ReleaseService) loadReadyReleaseArtworkAssets(
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
		return nil, errs.Internal(err)
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

func (s *ReleaseService) releaseResponseWithArtworkOg(
	release *model.Release,
	artworkAsset *commonv1.AssetRef,
) (*connect.Response[managev1.Release], error) {
	return connect.NewResponse(s.toProtoRelease(release, artworkAsset)), nil
}

// toProtoRelease converts a model.Release to protobuf Release
func (s *ReleaseService) toProtoRelease(
	r *model.Release,
	artworkAsset *commonv1.AssetRef,
) *managev1.Release {
	release := &managev1.Release{
		Id:        r.ID,
		Title:     r.Title,
		Type:      stringToReleaseType(r.Type),
		Document:  r.ContentDocument,
		Status:    r.Status,
		CreatedAt: timestamppb.New(r.CreatedAt),
		UpdatedAt: timestamppb.New(r.UpdatedAt),
		OgAsset:   artworkAsset,
		Revision:  r.ContentRevision,
	}

	if r.Slug != nil {
		release.Slug = r.Slug
	}
	if artworkAsset != nil {
		release.ArtworkAsset = artworkAsset
	}
	if r.ReleaseDate != nil {
		release.ReleaseDate = timestamppb.New(*r.ReleaseDate)
	}
	if r.PublishedAt != nil {
		release.PublishedAt = timestamppb.New(*r.PublishedAt)
	}
	if r.SpotifyURL != nil {
		release.SpotifyUrl = r.SpotifyURL
	}
	if r.AppleMusicURL != nil {
		release.AppleMusicUrl = r.AppleMusicURL
	}
	if r.BandcampURL != nil {
		release.BandcampUrl = r.BandcampURL
	}
	if r.YoutubeMusicURL != nil {
		release.YoutubeMusicUrl = r.YoutubeMusicURL
	}
	if r.CatalogNumber != nil {
		release.CatalogNumber = r.CatalogNumber
	}
	return release
}

func (s *ReleaseService) overlayReleaseSourceLocaleDocument(
	ctx context.Context,
	release *model.Release,
) error {
	if release == nil {
		return nil
	}
	releaseID := release.ID
	projection, err := loadReleaseManageContentProjection(
		ctx, s.db, s.contentBlocks, releaseID, s.og.LocaleAware(),
		func(ctx context.Context, tx *gorm.DB) error {
			var current model.Release
			if err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: "SHARE"}).
				Where("id = ?", releaseID).
				Take(&current).Error; err != nil {
				return err
			}
			*release = current
			return nil
		},
	)
	if err != nil {
		return err
	}
	release.Title = projection.SourceTitle
	release.ContentDocument = projection.Document
	release.ContentRevision = projection.Revision
	return nil
}

func (s *ReleaseService) overlayReleaseSourceLocaleDocuments(
	ctx context.Context,
	releases []model.Release,
) error {
	for i := range releases {
		if err := s.overlayReleaseSourceLocaleDocument(ctx, &releases[i]); err != nil {
			return err
		}
	}
	return nil
}

// =============================================================================
// Metadata Management (Site Admin only)
// =============================================================================

// SetReleaseArtists replaces all actual artists for a release.
