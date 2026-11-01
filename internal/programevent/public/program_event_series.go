package public

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/programevent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// GetProgramEventSeriesQuery names a published series by slug or ID.
type GetProgramEventSeriesQuery struct {
	SlugOrID string
}

// GetProgramEventSeries returns a published series.
func (s *Service) GetProgramEventSeries(ctx context.Context, query GetProgramEventSeriesQuery) (SeriesEntry, error) {
	slugOrID := strings.TrimSpace(query.SlugOrID)
	if slugOrID == "" {
		return SeriesEntry{}, domainerr.Required("slug")
	}
	var series programevent.Series
	if err := s.units.Read(ctx, func(store Store) error {
		_, parseErr := uuid.Parse(slugOrID)
		var err error
		series, err = store.FindPublishedSeries(ctx, slugOrID, parseErr == nil)
		return err
	}); err != nil {
		if errors.Is(err, programevent.ErrSeriesNotFound) {
			return SeriesEntry{}, domainerr.New(domainerr.NotFound, "", "program event series not found")
		}
		return SeriesEntry{}, err
	}
	return seriesEntry(ctx, s.assets, series), nil
}

// ListProgramEventSeriesQuery is one page of published series.
type ListProgramEventSeriesQuery struct {
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
}

// SeriesList is one page of published series.
type SeriesList struct {
	Series     []SeriesEntry
	Pagination programevent.Pagination
}

// ListProgramEventSeries returns one page of published series, most
// recently updated first.
func (s *Service) ListProgramEventSeries(ctx context.Context, query ListProgramEventSeriesQuery) (SeriesList, error) {
	pagination := queryutil.ExtractPaginationRaw(query.Limit, query.Offset, queryutil.DefaultPaginationConfig)
	var series []programevent.Series
	var total int64
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		series, total, err = store.ListPublishedSeries(ctx, programevent.ListPage{
			Filters: query.Filters, Limit: pagination.Limit, Offset: pagination.Offset,
		})
		return err
	}); err != nil {
		return SeriesList{}, err
	}
	result, err := s.seriesEntries(ctx, series)
	if err != nil {
		return SeriesList{}, err
	}
	return SeriesList{Series: result, Pagination: page(pagination, total)}, nil
}

func seriesEntry(ctx context.Context, assets Assets, series programevent.Series) SeriesEntry {
	return SeriesEntry{Series: series, PosterAsset: loadProgramEventFileAsset(ctx, assets, series.PosterFileID)}
}

func (s *Service) seriesEntries(ctx context.Context, series []programevent.Series) ([]SeriesEntry, error) {
	fileIDs := make([]string, 0, len(series))
	for i := range series {
		if series[i].PosterFileID != nil {
			fileIDs = append(fileIDs, *series[i].PosterFileID)
		}
	}
	assetsByFile, err := s.assets.ResolveReadyAssetsForSourceFiles(ctx, fileIDs, "image", "poster", "map_image")
	if err != nil {
		return nil, err
	}
	result := make([]SeriesEntry, 0, len(series))
	for i := range series {
		item := SeriesEntry{Series: series[i]}
		if series[i].PosterFileID != nil {
			item.PosterAsset = assetsByFile[*series[i].PosterFileID]
		}
		result = append(result, item)
	}
	return result, nil
}
