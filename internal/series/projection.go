package series

import (
	"context"
	"log/slog"
	"time"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SeriesService implements the SeriesService Connect handler
func (s *SeriesService) setSeriesFeaturedImageAsset(ctx context.Context, series *managev1.Series) {
	if series == nil {
		return
	}

	var result struct {
		FileID *string `gorm:"column:file_id"`
	}
	if err := s.db.WithContext(ctx).Table("series").
		Select("featured_image_file_id AS file_id").
		Where("id = ?", series.Id).
		Scan(&result).Error; err != nil || result.FileID == nil || *result.FileID == "" {
		if err != nil {
			slog.ErrorContext(ctx, "Operation failed", "domain", "series", "operation", "setSeriesFeaturedImageAsset", failurecause.Attr(err), "series_id", series.Id)
		}
		return
	}
	imageAsset, err := s.media.ReadyAssetForSourceFile(ctx, *result.FileID, "image")
	if err != nil {
		slog.ErrorContext(ctx, "failed to resolve Series featured image asset", "seriesId", series.Id, "fileId", *result.FileID, failurecause.Attr(err))
		return
	}
	if imageAsset != nil {
		series.FeaturedImageAsset = imageAsset
	}
}

func (s *SeriesService) loadReadySeriesOgAssets(
	ctx context.Context,
	seriesList []model.Series,
) (map[string]*commonv1.AssetRef, error) {
	candidates := make([]*string, 0, len(seriesList))
	for i := range seriesList {
		candidates = append(candidates, seriesList[i].OgAssetID)
	}
	return s.media.ReadyAssets(ctx, candidates...)
}

func (s *SeriesService) overlaySeriesSourceLocaleDocument(ctx context.Context, series *model.Series) error {
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

func (s *SeriesService) overlaySeriesSourceLocaleDocuments(ctx context.Context, series []model.Series) error {
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
			return errs.NotFound("series_translation", series[i].ID)
		}
		OverlaySourceLocaleDocument(&series[i], state)
	}
	return nil
}

// toProtoSeries converts a model.Series to protobuf Series
func (s *SeriesService) toProtoSeries(
	seriesModel *model.Series,
	ogAsset *commonv1.AssetRef,
) *managev1.Series {
	series := &managev1.Series{
		Id:           seriesModel.ID,
		Title:        seriesModel.Title,
		Slug:         seriesModel.Slug,
		Status:       seriesModel.Status,
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

// seriesPostRow retains each Post's source locale independently of its Series.
type seriesPostRow struct {
	ID           string
	SourceLocale string
	Title        string
	Slug         *string
	Status       string
	SeriesOrder  *int
	PublishedAt  *time.Time
}

func toProtoSeriesPost(post *seriesPostRow) *managev1.SeriesPost {
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
