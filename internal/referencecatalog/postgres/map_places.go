package referencecatalogpostgres

import (
	"context"
	"strings"

	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/referencecatalog"
)

var mapPlaceFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name", "address"},
		},
	},
}

var mapPlaceSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "name",
		"created_at": "created_at",
		"updated_at": "updated_at",
	},
	DefaultSort: "updated_at DESC",
}

func escapePgroongaQuery(q string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `''`,
		`*`, `\*`,
		`+`, `\+`,
		`(`, `\(`,
		`)`, `\)`,
		`[`, `\[`,
		`]`, `\]`,
	)
	return replacer.Replace(q)
}

// MapPlaceFromRow maps a map_place row, for the public catalog store too.
func MapPlaceFromRow(row model.MapPlace) referencecatalog.MapPlace {
	place := referencecatalog.MapPlace{
		ID: row.ID, Name: row.Name, Address: row.Address, Lat: row.Lat, Lng: row.Lng,
		GooglePlaceID: row.GooglePlaceID, ImageFileID: row.ImageFileID,
		CreatedByMemberID: row.CreatedByMemberID, UpdatedByMemberID: row.UpdatedByMemberID,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if components := row.AddressComponents; components != nil {
		place.AddressComponents = &referencecatalog.AddressComponents{
			Street: components.Street, City: components.City, Region: components.Region,
			Country: components.Country, PostalCode: components.PostalCode,
		}
	}
	return place
}

func rowFromMapPlace(place referencecatalog.MapPlace) model.MapPlace {
	row := model.MapPlace{
		ID: place.ID, Name: place.Name, Address: place.Address, Lat: place.Lat, Lng: place.Lng,
		GooglePlaceID: place.GooglePlaceID, ImageFileID: place.ImageFileID,
		CreatedByMemberID: place.CreatedByMemberID, UpdatedByMemberID: place.UpdatedByMemberID,
		CreatedAt: place.CreatedAt, UpdatedAt: place.UpdatedAt,
	}
	if components := place.AddressComponents; components != nil {
		row.AddressComponents = &model.AddressComponents{
			Street: components.Street, City: components.City, Region: components.Region,
			Country: components.Country, PostalCode: components.PostalCode,
		}
	}
	return row
}

func mapPlacesFromRows(rows []model.MapPlace) []referencecatalog.MapPlace {
	places := make([]referencecatalog.MapPlace, len(rows))
	for i := range rows {
		places[i] = MapPlaceFromRow(rows[i])
	}
	return places
}

func (s store) MapPlace(ctx context.Context, id string) (referencecatalog.MapPlace, error) {
	var place model.MapPlace
	if err := s.tx.WithContext(ctx).Where("id = ?", id).First(&place).Error; err != nil {
		return referencecatalog.MapPlace{}, notFound(err, referencecatalog.ErrMapPlaceNotFound)
	}
	return MapPlaceFromRow(place), nil
}

func (s store) MapPlacesByID(ctx context.Context, ids []string) ([]referencecatalog.MapPlace, error) {
	var places []model.MapPlace
	if err := s.tx.WithContext(ctx).Where("id IN ?", ids).Find(&places).Error; err != nil {
		return nil, err
	}
	return mapPlacesFromRows(places), nil
}

// SearchMapPlaces matches the name with pgroonga.
func (s store) SearchMapPlaces(ctx context.Context, text string, limit int) ([]referencecatalog.MapPlace, error) {
	var places []model.MapPlace
	if err := s.tx.WithContext(ctx).
		Where("name &@~ ?", escapePgroongaQuery(text)).
		Order("name ASC").
		Limit(limit).
		Find(&places).Error; err != nil {
		return nil, err
	}
	return mapPlacesFromRows(places), nil
}

func (s store) ListMapPlaces(ctx context.Context, page referencecatalog.ListPage) ([]referencecatalog.MapPlace, int64, error) {
	scoped, err := mapPlaceFilterConfig.ApplyFilters(s.tx.WithContext(ctx).Model(&model.MapPlace{}), page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := scoped.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	scoped, err = mapPlaceSortConfig.ApplySort(scoped, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var places []model.MapPlace
	if err := scoped.Offset(int(page.Offset)).Limit(int(page.Limit)).Find(&places).Error; err != nil {
		return nil, 0, err
	}
	return mapPlacesFromRows(places), total, nil
}

func (s store) LockMapPlace(ctx context.Context, id string) (referencecatalog.MapPlace, error) {
	var place model.MapPlace
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		First(&place, "id = ?", id).Error; err != nil {
		return referencecatalog.MapPlace{}, notFound(err, referencecatalog.ErrMapPlaceNotFound)
	}
	return MapPlaceFromRow(place), nil
}

func (s store) InsertMapPlace(_ context.Context, place referencecatalog.MapPlace) (referencecatalog.MapPlace, error) {
	row := rowFromMapPlace(place)
	if err := s.tx.Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return referencecatalog.MapPlace{}, err
	}
	return MapPlaceFromRow(row), nil
}

func (s store) SaveMapPlace(_ context.Context, place referencecatalog.MapPlace) (referencecatalog.MapPlace, error) {
	row := rowFromMapPlace(place)
	if err := s.tx.Save(&row).Error; err != nil {
		return referencecatalog.MapPlace{}, err
	}
	return MapPlaceFromRow(row), nil
}

// MapPlaceInUse relies on the caller's root row lock, as ReferenceInUse
// does.
func (s store) MapPlaceInUse(ctx context.Context, id string) (bool, error) {
	for _, table := range []string{"post", "work", "program_event"} {
		var count int64
		if err := s.tx.WithContext(ctx).Table(table).Where("map_place_id = ?", id).Limit(1).Count(&count).Error; err != nil {
			return false, err
		}
		if count > 0 {
			return true, nil
		}
	}
	return false, nil
}

func (s store) DeleteMapPlace(_ context.Context, id string) error {
	return s.tx.Delete(&model.MapPlace{ID: id}).Error
}
