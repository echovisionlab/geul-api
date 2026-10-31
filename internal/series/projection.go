package series

import (
	"context"
	"log/slog"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SeriesService implements the SeriesService Connect handler
func (s *Service) setSeriesFeaturedImageAsset(ctx context.Context, series *managev1.Series) {
	if series == nil {
		return
	}

	var result struct {
		FileID *string `gorm:"column:file_id"`
	}
	if err := s.db.WithContext(ctx).Table("series").
		Select("featured_image_file_id AS file_id").
		Where("id = ?", series.Id).
		Scan(&result).Error; err != nil {
		slog.ErrorContext(ctx, "Series featured image lookup failed; serving Series without featured image", "domain", "series", "operation", "load_featured_image", failurecause.Attr(err), "series_id", series.Id)
		return
	}
	if result.FileID == nil || *result.FileID == "" {
		return
	}
	imageAsset, err := s.media.ReadyAssetForSourceFile(ctx, *result.FileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "Series featured image asset resolution failed; serving Series without featured image", "domain", "series", "operation", "resolve_featured_image_asset", failurecause.Attr(err), "series_id", series.Id, "file_id", *result.FileID)
		return
	}
	if imageAsset != nil {
		series.FeaturedImageAsset = imageAsset
	}
}

func (s *Service) loadReadySeriesOgAssets(
	ctx context.Context,
	seriesList []model.Series,
) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(seriesList))
	for i := range seriesList {
		candidates = append(candidates, seriesList[i].OgAssetID)
	}
	return s.media.ReadyAssets(ctx, candidates...)
}

func (s *Service) overlaySeriesSourceLocaleDocument(ctx context.Context, series *model.Series) error {
	if series == nil {
		return nil
	}
	state, err := LoadRequiredSourceLocaleDocument(ctx, s.db, series.ID)
	if err != nil {
		return err
	}
	OverlaySourceLocaleDocument(series, state)
	return nil
}

func (s *Service) overlaySeriesSourceLocaleDocuments(ctx context.Context, series []model.Series) error {
	ids := make([]string, 0, len(series))
	for i := range series {
		ids = append(ids, series[i].ID)
	}
	states, err := LoadSourceLocaleDocumentStates(ctx, s.db, ids)
	if err != nil {
		return err
	}
	for i := range series {
		state := states[series[i].ID]
		if state == nil {
			return domainerr.ResourceNotFound("series_translation", series[i].ID)
		}
		OverlaySourceLocaleDocument(&series[i], state)
	}
	return nil
}

// toProtoSeries converts a model.Series to protobuf Series
func (s *Service) toProtoSeries(
	seriesModel *model.Series,
	ogAsset *commonv1.AssetRef,
) *managev1.Series {
	series := &managev1.Series{
		Id:           seriesModel.ID,
		Title:        seriesModel.Title,
		Slug:         seriesModel.Slug,
		Status:       string(seriesModel.Status),
		CreatedAt:    timestamppb.New(seriesModel.CreatedAt),
		OgAsset:      ogAsset,
		SourceLocale: seriesModel.SourceLocale,
	}

	if seriesModel.Description != nil {
		series.Description = seriesModel.Description
	}
	if seriesModel.UpdatedAt != nil {
		series.UpdatedAt = timestamppb.New(*seriesModel.UpdatedAt)
	}

	return series
}

// seriesSortConfig defines allowed sort fields for series
var seriesSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"created_at": "created_at",
		"updated_at": "updated_at",
		"title":      SourceTitleSQL("series"),
		"status":     "status",
	},
	DefaultSort: "created_at DESC",
}

func toProtoSeriesPost(post *SeriesPost) *managev1.SeriesPost {
	response := &managev1.SeriesPost{Id: post.ID, SourceLocale: post.SourceLocale, Title: post.Title, Status: post.Status}
	if post.Slug != nil {
		response.Slug = *post.Slug
	}
	if post.SeriesOrder != nil {
		response.SeriesOrder = int32(*post.SeriesOrder)
	}
	if post.PublishedAt != nil {
		response.PublishedAt = timestamppb.New(*post.PublishedAt)
	}
	return response
}

func toProtoSeriesSimple(series *model.Series) *managev1.SeriesSimple {
	return &managev1.SeriesSimple{Id: series.ID, SourceLocale: series.SourceLocale, Title: series.Title, Slug: series.Slug}
}
