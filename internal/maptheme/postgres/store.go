// Package postgres is the PostgreSQL Store of the Map Theme use cases: the
// map_theme rows, the site default it reads and sets in site_settings, and
// the contributor locks of a collaboration save.
package postgres

import (
	"context"
	stderrors "errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/maptheme"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
)

// NewUnits is the production unit of work of the Map Theme use cases.
func NewUnits(db *gorm.DB) mutation.UnitOfWork[maptheme.Store] {
	return mutation.Postgres(db, func(tx *gorm.DB) maptheme.Store { return store{tx: tx} })
}

// store is the Map Theme Store bound to one transaction.
type store struct{ tx *gorm.DB }

// defaultThemeError is a delete the site default's foreign key refused.
type defaultThemeError struct{ error } // the *pgconn.PgError

func (err defaultThemeError) Is(target error) bool { return target == maptheme.ErrDefaultTheme }
func (err defaultThemeError) Unwrap() error        { return err.error }

func isDefaultDeleteViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !stderrors.As(err, &pgErr) || pgErr.ConstraintName != "site_settings_default_map_theme_id_fkey" {
		return false
	}
	return pgErr.Code == "23001" || pgErr.Code == "23503"
}

func notFound(err error) error {
	if stderrors.Is(err, gorm.ErrRecordNotFound) {
		return maptheme.ErrThemeNotFound
	}
	return err
}

func (s store) DefaultThemeID(ctx context.Context) (string, error) {
	var settings model.SiteSettings
	if err := s.tx.WithContext(ctx).Select("default_map_theme_id").Where("id = ?", 1).Take(&settings).Error; err != nil {
		return "", err
	}
	if strings.TrimSpace(settings.DefaultMapThemeID) == "" {
		return "", fmt.Errorf("site settings default map theme is empty")
	}
	return settings.DefaultMapThemeID, nil
}

func (s store) LockDefaultThemeID(context.Context) (string, error) {
	var settings model.SiteSettings
	if err := s.tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&settings, "id = 1").Error; err != nil {
		return "", err
	}
	return settings.DefaultMapThemeID, nil
}

func (s store) SetDefaultThemeID(_ context.Context, themeID string) error {
	settings := model.SiteSettings{ID: 1}
	return s.tx.Model(settings).Updates(structured.Fields{
		"default_map_theme_id": themeID,
		"updated_at":           time.Now(),
	}).Error
}

func (s store) Themes(context.Context) ([]maptheme.Theme, error) {
	var rows []model.MapTheme
	if err := s.tx.Order("name ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return themesFromRows(rows), nil
}

func (s store) ThemesByID(_ context.Context, ids []string) ([]maptheme.Theme, error) {
	var rows []model.MapTheme
	if err := s.tx.Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	return themesFromRows(rows), nil
}

func (s store) Theme(_ context.Context, id string) (maptheme.Theme, error) {
	var row model.MapTheme
	if err := s.tx.Where("id = ?", id).Take(&row).Error; err != nil {
		return maptheme.Theme{}, notFound(err)
	}
	return themeFromRow(row), nil
}

func (s store) LockTheme(ctx context.Context, id string) (maptheme.Theme, error) {
	var row model.MapTheme
	if err := s.tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", id).
		Take(&row).Error; err != nil {
		return maptheme.Theme{}, notFound(err)
	}
	return themeFromRow(row), nil
}

func (s store) InsertTheme(_ context.Context, snapshot maptheme.Snapshot) (maptheme.Theme, error) {
	row := rowFromSnapshot(snapshot)
	row.EditVersion = 1
	if err := s.tx.Clauses(clause.Returning{}).Create(&row).Error; err != nil {
		return maptheme.Theme{}, err
	}
	return themeFromRow(row), nil
}

func (s store) UpdateSnapshot(_ context.Context, id string, expectedRevision int64, snapshot maptheme.Snapshot) error {
	updates := snapshotUpdates(snapshot)
	updates["edit_version"] = gorm.Expr("edit_version + 1")
	updates["updated_at"] = time.Now()
	result := s.tx.Model(&model.MapTheme{}).Where("id = ? AND edit_version = ?", id, expectedRevision).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return maptheme.ErrRevisionChanged
	}
	return nil
}

func (s store) DeleteTheme(_ context.Context, id string) error {
	result := s.tx.Where("id = ?", id).Delete(&model.MapTheme{})
	if result.Error != nil {
		if isDefaultDeleteViolation(result.Error) {
			return defaultThemeError{result.Error}
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return maptheme.ErrThemeNotFound
	}
	return nil
}

func (s store) LockContributor(ctx context.Context, memberID string) (string, error) {
	target, err := authorizationtarget.RequireLocked(ctx, s.tx, memberID)
	if err != nil {
		return "", err
	}
	return target.IdentityID, nil
}

func themesFromRows(rows []model.MapTheme) []maptheme.Theme {
	themes := make([]maptheme.Theme, 0, len(rows))
	for _, row := range rows {
		themes = append(themes, themeFromRow(row))
	}
	return themes
}

func themeFromRow(row model.MapTheme) maptheme.Theme {
	return maptheme.Theme{
		ID: row.ID, Revision: row.EditVersion, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		Snapshot: maptheme.Snapshot{
			Name: row.Name,
			Settings: maptheme.Settings{
				CalloutScale: float64(row.CalloutScale), CalloutOffsetX: int32(row.CalloutOffsetX),
				CalloutOffsetY: int32(row.CalloutOffsetY), CalloutFields: slices.Clone([]string(row.CalloutFields)),
				AttributionFontSize: int32(row.AttributionFontSize), ShowAreaLabels: row.ShowAreaLabels,
				ShowPoiLabels: row.ShowPoiLabels,
			},
			LightVariant: variantFromRow(row.LightVariant),
			DarkVariant:  variantFromRow(row.DarkVariant),
		},
	}
}

func rowFromSnapshot(snapshot maptheme.Snapshot) model.MapTheme {
	return model.MapTheme{
		Name:                snapshot.Name,
		CalloutScale:        float32(snapshot.Settings.CalloutScale),
		CalloutOffsetX:      int(snapshot.Settings.CalloutOffsetX),
		CalloutOffsetY:      int(snapshot.Settings.CalloutOffsetY),
		CalloutFields:       append([]string(nil), snapshot.Settings.CalloutFields...),
		AttributionFontSize: int(snapshot.Settings.AttributionFontSize),
		ShowAreaLabels:      snapshot.Settings.ShowAreaLabels,
		ShowPoiLabels:       snapshot.Settings.ShowPoiLabels,
		LightVariant:        rowFromVariant(snapshot.LightVariant),
		DarkVariant:         rowFromVariant(snapshot.DarkVariant),
	}
}

func variantFromRow(input model.MapThemeVariant) maptheme.Variant {
	return maptheme.Variant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor,
		LandColor: input.LandColor, RoadColor: input.RoadColor,
		BuildingFillColor: input.BuildingFillColor, BuildingStrokeEnabled: input.BuildingStrokeEnabled,
		BuildingStrokeColor: input.BuildingStrokeColor, CalloutLineColor: input.CalloutLineColor,
		CalloutTextColor: input.CalloutTextColor, CalloutBackgroundColor: input.CalloutBackgroundColor,
		CalloutDescriptionColor: input.CalloutDescriptionColor, AttributionColor: input.AttributionColor,
		LabelTextColor: input.LabelTextColor, ClusterColor: input.ClusterColor,
		ClusterHoverColor: input.ClusterHoverColor, ClusterTextColor: input.ClusterTextColor,
		ClusterTextHoverColor: input.ClusterTextHoverColor, CalloutHoverLineColor: input.CalloutHoverLineColor,
		CalloutHoverTextColor:        input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func rowFromVariant(input maptheme.Variant) model.MapThemeVariant {
	return model.MapThemeVariant{
		BackgroundColor: input.BackgroundColor, WaterColor: input.WaterColor,
		LandColor: input.LandColor, RoadColor: input.RoadColor,
		BuildingFillColor: input.BuildingFillColor, BuildingStrokeEnabled: input.BuildingStrokeEnabled,
		BuildingStrokeColor: input.BuildingStrokeColor, CalloutLineColor: input.CalloutLineColor,
		CalloutTextColor: input.CalloutTextColor, CalloutBackgroundColor: input.CalloutBackgroundColor,
		CalloutDescriptionColor: input.CalloutDescriptionColor, AttributionColor: input.AttributionColor,
		LabelTextColor: input.LabelTextColor, ClusterColor: input.ClusterColor,
		ClusterHoverColor: input.ClusterHoverColor, ClusterTextColor: input.ClusterTextColor,
		ClusterTextHoverColor: input.ClusterTextHoverColor, CalloutHoverLineColor: input.CalloutHoverLineColor,
		CalloutHoverTextColor:        input.CalloutHoverTextColor,
		CalloutHoverDescriptionColor: input.CalloutHoverDescriptionColor,
		CalloutHoverBackgroundColor:  input.CalloutHoverBackgroundColor,
	}
}

func variantUpdates(prefix string, input maptheme.Variant) structured.Fields {
	return structured.Fields{
		prefix + "background_color": input.BackgroundColor, prefix + "water_color": input.WaterColor,
		prefix + "land_color": input.LandColor, prefix + "road_color": input.RoadColor,
		prefix + "building_fill_color":     input.BuildingFillColor,
		prefix + "building_stroke_enabled": input.BuildingStrokeEnabled,
		prefix + "building_stroke_color":   input.BuildingStrokeColor,
		prefix + "callout_line_color":      input.CalloutLineColor, prefix + "callout_text_color": input.CalloutTextColor,
		prefix + "callout_background_color":  input.CalloutBackgroundColor,
		prefix + "callout_description_color": input.CalloutDescriptionColor,
		prefix + "attribution_color":         input.AttributionColor, prefix + "label_text_color": input.LabelTextColor,
		prefix + "cluster_color": input.ClusterColor, prefix + "cluster_hover_color": input.ClusterHoverColor,
		prefix + "cluster_text_color":              input.ClusterTextColor,
		prefix + "cluster_text_hover_color":        input.ClusterTextHoverColor,
		prefix + "callout_hover_line_color":        input.CalloutHoverLineColor,
		prefix + "callout_hover_text_color":        input.CalloutHoverTextColor,
		prefix + "callout_hover_description_color": input.CalloutHoverDescriptionColor,
		prefix + "callout_hover_background_color":  input.CalloutHoverBackgroundColor,
	}
}

func snapshotUpdates(snapshot maptheme.Snapshot) structured.Fields {
	updates := structured.Fields{
		"name": snapshot.Name, "callout_scale": float32(snapshot.Settings.CalloutScale),
		"callout_offset_x":      int(snapshot.Settings.CalloutOffsetX),
		"callout_offset_y":      int(snapshot.Settings.CalloutOffsetY),
		"callout_fields":        pq.StringArray(snapshot.Settings.CalloutFields),
		"attribution_font_size": int(snapshot.Settings.AttributionFontSize),
		"show_area_labels":      snapshot.Settings.ShowAreaLabels,
		"show_poi_labels":       snapshot.Settings.ShowPoiLabels,
	}
	maps.Copy(updates, variantUpdates("light_", snapshot.LightVariant))
	maps.Copy(updates, variantUpdates("dark_", snapshot.DarkVariant))
	return updates
}
