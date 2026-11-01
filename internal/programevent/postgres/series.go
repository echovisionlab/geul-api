package programeventpostgres

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
)

const programEventSeriesSourceTitleSQL = "program_event_series.title"

var seriesSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"updated_at": "updated_at",
		"created_at": "created_at",
		"title":      programEventSeriesSourceTitleSQL,
	},
	DefaultSort: "updated_at DESC",
}

var seriesFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:       queryutil.TypeText,
			AllowedOps: queryutil.SearchOps,
			SearchColumns: []string{
				programEventSeriesSourceTitleSQL,
			},
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				string(model.ProgramEventStatusDraft),
				string(model.ProgramEventStatusPublished),
			},
		},
	},
}

func seriesNotFound(err error) error { return notFound(err, programevent.ErrSeriesNotFound) }

func seriesFromRow(row model.ProgramEventSeries) programevent.Series {
	return programevent.Series{
		ID: row.ID, Slug: row.Slug, Status: programevent.Status(row.Status), Title: row.Title,
		Summary: row.Summary, Description: row.Description, PosterFileID: row.PosterFileID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *store) InsertSeries(ctx context.Context, series *programevent.Series) error {
	row := &model.ProgramEventSeries{
		Slug: series.Slug, Status: model.ProgramEventStatus(series.Status), Title: series.Title,
		Summary: series.Summary, Description: series.Description, PosterFileID: series.PosterFileID,
		CreatedAt: series.CreatedAt, UpdatedAt: series.UpdatedAt,
	}
	if err := s.tx.Omit("ID").Clauses(clause.Returning{}).Create(row).Error; err != nil {
		return slugTaken(err)
	}
	*series = seriesFromRow(*row)
	return nil
}

func (s *store) Series(ctx context.Context, seriesID string, lock programevent.RowLock) (programevent.Series, error) {
	var row model.ProgramEventSeries
	if err := withLock(s.tx.WithContext(ctx), lock).First(&row, "id = ?", seriesID).Error; err != nil {
		return programevent.Series{}, seriesNotFound(err)
	}
	return seriesFromRow(row), nil
}

func (s *store) SeriesBySlug(ctx context.Context, slug string) (programevent.Series, error) {
	var row model.ProgramEventSeries
	if err := s.tx.WithContext(ctx).First(&row, "slug = ?", slug).Error; err != nil {
		return programevent.Series{}, seriesNotFound(err)
	}
	return seriesFromRow(row), nil
}

func (s *store) LockSeriesID(ctx context.Context, seriesID string) error {
	var series model.ProgramEventSeries
	return seriesNotFound(s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").First(&series, "id = ?", seriesID).Error)
}

// seriesColumns is the program_event_series row update for the fields
// change sets.
func seriesColumns(change programevent.SeriesChange, updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	for column, value := range map[string]*string{
		"title":       change.Title,
		"summary":     change.Summary,
		"description": change.Description,
		"slug":        change.Slug,
	} {
		if value != nil {
			columns[column] = *value
		}
	}
	if change.Status != nil {
		columns["status"] = model.ProgramEventStatus(*change.Status)
	}
	if change.PosterChanged {
		columns["poster_file_id"] = nil
		if change.PosterFileID != nil {
			columns["poster_file_id"] = *change.PosterFileID
		}
	}
	return columns
}

func (s *store) UpdateSeries(ctx context.Context, seriesID string, change programevent.SeriesChange, updatedAt time.Time) error {
	return slugTaken(s.tx.Model(&model.ProgramEventSeries{ID: seriesID}).Updates(seriesColumns(change, updatedAt)).Error)
}

func (s *store) DeleteSeries(ctx context.Context, seriesID string) (bool, error) {
	result := s.tx.Delete(&model.ProgramEventSeries{}, "id = ?", seriesID)
	return result.RowsAffected > 0, result.Error
}

func (s *store) ListSeries(ctx context.Context, page programevent.ListPage) ([]programevent.Series, int64, error) {
	rows := s.tx.WithContext(ctx).Model(&model.ProgramEventSeries{})
	rows, err := seriesFilterConfig.ApplyFilters(rows, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows, err = seriesSortConfig.ApplySort(rows, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var series []model.ProgramEventSeries
	if err := rows.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&series).Error; err != nil {
		return nil, 0, err
	}
	result := make([]programevent.Series, 0, len(series))
	for _, row := range series {
		result = append(result, seriesFromRow(row))
	}
	return result, total, nil
}
