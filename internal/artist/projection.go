package artist

import (
	"context"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ArtistService implements the ArtistService Connect handler
var artistSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         ArtistSourceTitleSQL("artist"),
		"slug":         "slug",
		"status":       "status",
		"country_code": "country_code",
		"created_at":   "created_at",
		"updated_at":   "updated_at",
		"published_at": "published_at",
	},
	DefaultSort: ArtistSourceTitleSQL("artist") + " ASC",
}

// checkSlugAvailable checks if an artist slug is available for use
// excludeID can be empty for new artists, or set to the current artist ID when updating
func (s *ArtistService) checkSlugAvailable(ctx context.Context, slug string, excludeID string) error {
	if err := validateSlugWithoutSlash(slug); err != nil {
		return err
	}
	if slug == "" {
		return nil // Empty slug is always valid
	}

	var count int64
	query := s.db.WithContext(ctx).Model(&model.Artist{}).Where("slug = ?", slug)
	if excludeID != "" {
		query = query.Where("id != ?", excludeID)
	}

	if err := query.Count(&count).Error; err != nil {
		return errs.Internal(err)
	}

	if count > 0 {
		return errs.SlugAlreadyExists("artist", slug)
	}

	return nil
}

// CheckArtistSlugAvailable checks if a slug is available
// If excludeArtistId is provided, requires edit permission on that artist
func (s *ArtistService) CheckArtistSlugAvailable(
	ctx context.Context,
	req *connect.Request[managev1.CheckArtistSlugAvailableRequest],
) (*connect.Response[managev1.CheckArtistSlugAvailableResponse], error) {
	// If excludeArtistId is provided, check edit permission on that artist
	if req.Msg.ExcludeArtistId != nil && *req.Msg.ExcludeArtistId != "" {
		if err := requireArtistPermission(ctx, s.spiceDB, *req.Msg.ExcludeArtistId, policyv1.Artist.Edit); err != nil {
			return nil, err
		}
	} else {
		// No artist specified - require admin role (for creating new artists)
		if err := requireArtistCreate(ctx, s.spiceDB); err != nil {
			return nil, err
		}
	}
	if err := validateSlugWithoutSlash(req.Msg.Slug); err != nil {
		return nil, err
	}

	query := s.db.WithContext(ctx).
		Model(&model.Artist{}).
		Where("slug = ?", req.Msg.Slug)

	if req.Msg.ExcludeArtistId != nil && *req.Msg.ExcludeArtistId != "" {
		query = query.Where("id != ?", *req.Msg.ExcludeArtistId)
	}

	var count int64
	if err := query.Count(&count).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if count == 0 {
		available, err := s.runtime.IsResourceRouteAvailable(ctx, s.db, "artists", req.Msg.Slug)
		if err != nil {
			return nil, err
		}
		if !available {
			count = 1
		}
	}

	return connect.NewResponse(&managev1.CheckArtistSlugAvailableResponse{
		Available: count == 0,
	}), nil
}

// getArtistLabelIDs gets associated label IDs excluding draft labels.
func (s *ArtistService) getArtistLabelIDs(ctx context.Context, artistID string) ([]string, error) {
	var rows []struct {
		LabelID string `gorm:"column:label_id"`
	}
	if err := s.db.WithContext(ctx).
		Table("artist_label").
		Select("artist_label.label_id").
		Joins("JOIN label ON label.id = artist_label.label_id").
		Where("artist_label.artist_id = ? AND label.status != ?", artistID, managev1.LabelStatus_LABEL_STATUS_DRAFT.String()).
		Order("artist_label.sort_order ASC NULLS LAST, artist_label.label_id ASC").
		Scan(&rows).Error; err != nil {
		return nil, err
	}

	labelIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		labelIDs = append(labelIDs, row.LabelID)
	}
	return labelIDs, nil
}

func (s *ArtistService) overlayArtistSourceLocaleDocument(
	ctx context.Context,
	artist *model.Artist,
) error {
	if artist == nil {
		return nil
	}
	artistID := artist.ID
	projection, err := loadCreativeManageContentProjection(
		ctx, s.db, s.contentBlocks, artistContentEntity, artistID,
		func(ctx context.Context, tx *gorm.DB) error {
			var current model.Artist
			if err := tx.WithContext(ctx).
				Clauses(clause.Locking{Strength: "SHARE"}).
				Where("id = ?", artistID).
				Take(&current).Error; err != nil {
				return err
			}
			*artist = current
			return nil
		},
	)
	if err != nil {
		return err
	}
	artist.Name = projection.SourceTitle
	artist.ContentDocument = projection.Document
	artist.ContentRevision = projection.Revision
	artist.OgAssetID = projection.SourceOgAssetID
	return nil
}

func (s *ArtistService) overlayArtistSourceLocaleDocuments(
	ctx context.Context,
	artists []model.Artist,
) error {
	for i := range artists {
		if err := s.overlayArtistSourceLocaleDocument(ctx, &artists[i]); err != nil {
			return err
		}
	}
	return nil
}

func (s *ArtistService) loadReadyArtistOgAssets(
	ctx context.Context,
	artists []model.Artist,
) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(artists))
	for i := range artists {
		candidates = append(candidates, artists[i].OgAssetID)
	}
	return loadReadyManageOgAssetRefs(ctx, s.runtime, s.db, candidates...)
}

func (s *ArtistService) artistResponseWithReadyOg(
	ctx context.Context,
	artist *model.Artist,
	imageAsset *commonv1.AssetRef,
) (*connect.Response[managev1.Artist], error) {
	ogAsset, err := readyManageOgAssetRef(ctx, s.runtime, s.db, artist.OgAssetID)
	if err != nil {
		return nil, err
	}
	images, err := loadArtistImages(ctx, s.runtime, s.db, artist.ID)
	if err != nil {
		return nil, errs.Internal(err)
	}
	protoArtist := s.toProtoArtist(artist, imageAsset, ogAsset)
	applyManageArtistImages(protoArtist, images)
	return connect.NewResponse(protoArtist), nil
}

// toProtoArtist converts a model.Artist to protobuf Artist
func (s *ArtistService) toProtoArtist(
	a *model.Artist,
	imageAsset *commonv1.AssetRef,
	ogAsset *commonv1.AssetRef,
) *managev1.Artist {
	artist := &managev1.Artist{
		Id:        a.ID,
		Name:      a.Name,
		Document:  a.ContentDocument,
		Status:    a.Status,
		CreatedAt: timestamppb.New(a.CreatedAt),
		OgAsset:   ogAsset,
		Revision:  a.ContentRevision,
	}

	if a.Slug != nil {
		artist.Slug = a.Slug
	}
	if a.RealName != nil {
		artist.RealName = a.RealName
	}
	if a.ParentArtistID != nil {
		artist.ParentArtistId = a.ParentArtistID
	}
	if a.CountryCode != nil {
		artist.CountryCode = a.CountryCode
	}
	if a.Website != nil {
		artist.Website = a.Website
	}
	if imageAsset != nil {
		artist.ImageAsset = imageAsset
	}
	if a.PublishedAt != nil {
		artist.PublishedAt = timestamppb.New(*a.PublishedAt)
	}
	if a.UpdatedAt != nil {
		artist.UpdatedAt = timestamppb.New(*a.UpdatedAt)
	}
	// Convert social links
	if a.SocialLinks != nil {
		artist.SocialLinks = a.SocialLinks
	}

	return artist
}
