package programeventpostgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/programevent"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/structured"
)

var typeSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"sort_order": "sort_order",
		"slug":       "slug",
		"updated_at": "updated_at",
		"created_at": "created_at",
	},
	DefaultSort: "sort_order ASC, slug ASC",
}

var typeFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"id": {
			Column:     "id",
			Type:       queryutil.TypeID,
			AllowedOps: queryutil.IDOps,
		},
		"status": {
			Column:     "status",
			Type:       queryutil.TypeEnum,
			AllowedOps: queryutil.EnumOps,
			EnumValues: []string{
				string(model.ProgramEventTypeStatusActive),
				string(model.ProgramEventTypeStatusInactive),
			},
		},
	},
}

func typeFromRow(row model.ProgramEventType) programevent.Type {
	return programevent.Type{
		ID: row.ID, Slug: row.Slug, Status: programevent.TypeStatus(row.Status), SortOrder: row.SortOrder,
		RequiresPlace: row.RequiresPlace, RequiresStreamURL: row.RequiresStreamURL,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func typeLocaleFromRow(row model.ProgramEventTypeLocale) programevent.TypeLocale {
	return programevent.TypeLocale{
		TypeID: row.TypeID, Locale: row.Locale, Name: row.Name, Description: row.Description,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func (s *store) InsertType(ctx context.Context, eventType *programevent.Type) error {
	row := &model.ProgramEventType{
		Slug: eventType.Slug, Status: model.ProgramEventTypeStatus(eventType.Status), SortOrder: eventType.SortOrder,
		RequiresPlace: eventType.RequiresPlace, RequiresStreamURL: eventType.RequiresStreamURL,
		CreatedAt: eventType.CreatedAt, UpdatedAt: eventType.UpdatedAt,
	}
	if err := s.tx.Omit("ID").Clauses(clause.Returning{}).Create(row).Error; err != nil {
		return slugTaken(err)
	}
	*eventType = typeFromRow(*row)
	return nil
}

func (s *store) InsertTypeLocale(ctx context.Context, locale programevent.TypeLocale) error {
	return s.tx.Create(&model.ProgramEventTypeLocale{
		TypeID: locale.TypeID, Locale: locale.Locale, Name: locale.Name, Description: locale.Description,
		CreatedAt: locale.CreatedAt, UpdatedAt: locale.UpdatedAt,
	}).Error
}

func (s *store) Type(ctx context.Context, typeID string, lock programevent.RowLock) (programevent.Type, error) {
	var row model.ProgramEventType
	if err := withLock(s.tx.WithContext(ctx), lock).First(&row, "id = ?", typeID).Error; err != nil {
		return programevent.Type{}, notFound(err, programevent.ErrTypeNotFound)
	}
	return typeFromRow(row), nil
}

// typeColumns is the program_event_type row update for the fields change
// sets.
func typeColumns(change programevent.TypeChange, updatedAt time.Time) structured.Fields {
	columns := structured.Fields{"updated_at": updatedAt}
	if change.Slug != nil {
		columns["slug"] = *change.Slug
	}
	if change.Status != nil {
		columns["status"] = model.ProgramEventTypeStatus(*change.Status)
	}
	if change.SortOrder != nil {
		columns["sort_order"] = *change.SortOrder
	}
	if change.RequiresPlace != nil {
		columns["requires_place"] = *change.RequiresPlace
	}
	if change.RequiresStreamURL != nil {
		columns["requires_stream_url"] = *change.RequiresStreamURL
	}
	return columns
}

func (s *store) UpdateType(ctx context.Context, typeID string, change programevent.TypeChange, updatedAt time.Time) error {
	return slugTaken(s.tx.Model(&model.ProgramEventType{ID: typeID}).Updates(typeColumns(change, updatedAt)).Error)
}

func (s *store) TypeLocale(ctx context.Context, typeID, locale string) (programevent.TypeLocale, bool, error) {
	var row model.ProgramEventTypeLocale
	if err := s.tx.First(&row, "type_id = ? AND locale = ?", typeID, locale).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return programevent.TypeLocale{}, false, nil
		}
		return programevent.TypeLocale{}, false, err
	}
	return typeLocaleFromRow(row), true, nil
}

func (s *store) UpdateTypeLocale(ctx context.Context, typeID, locale string, change programevent.TypeLocaleChange, updatedAt time.Time) error {
	updates := structured.Fields{}
	if change.Name != nil {
		updates["name"] = *change.Name
	}
	if change.Description.Set {
		updates["description"] = change.Description.Value
	}
	updates["updated_at"] = updatedAt
	return s.tx.Model(&model.ProgramEventTypeLocale{TypeID: typeID, Locale: locale}).Updates(updates).Error
}

func (s *store) DeleteType(ctx context.Context, typeID string) error {
	return typeInUse(s.tx.Delete(&model.ProgramEventType{ID: typeID}).Error)
}

func (s *store) TypeLocales(ctx context.Context, typeID string) ([]programevent.TypeLocale, error) {
	var rows []model.ProgramEventTypeLocale
	if err := s.tx.WithContext(ctx).Where("type_id = ?", typeID).Order("locale ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	locales := make([]programevent.TypeLocale, 0, len(rows))
	for _, row := range rows {
		locales = append(locales, typeLocaleFromRow(row))
	}
	return locales, nil
}

func (s *store) ListTypes(ctx context.Context, page programevent.ListPage) ([]programevent.Type, int64, error) {
	rows := s.tx.WithContext(ctx).Model(&model.ProgramEventType{})
	rows, err := typeFilterConfig.ApplyFilters(rows, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := rows.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	rows, err = typeSortConfig.ApplySort(rows, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var types []model.ProgramEventType
	if err := rows.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&types).Error; err != nil {
		return nil, 0, err
	}
	result := make([]programevent.Type, 0, len(types))
	for _, row := range types {
		result = append(result, typeFromRow(row))
	}
	return result, total, nil
}
