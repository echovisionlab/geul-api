package label

import (
	"context"
	"strings"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

// Requests reads canonical Label snapshots for OG generation.
type Requests struct{}

func NewRequests() *Requests { return &Requests{} }

func (*Requests) Handles(entityType string) bool { return entityType == "label" }

func (r *Requests) Resolve(
	ctx context.Context,
	db *gorm.DB,
	entityType, entityID string,
	selection *managev1.OgTargetSelection,
) ([]og.Request, error) {
	if !r.Handles(entityType) {
		return nil, errs.InvalidEntityType(entityType)
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, errs.Required("entity_id")
	}
	if selection == nil || selection.GetPrimary() == nil {
		return nil, errs.InvalidArgument("selection", "base-only OG entities require primary target")
	}
	request, err := loadLabelOGRequest(ctx, db, entityID)
	if err != nil {
		return nil, err
	}
	if request.FeaturedImageFileID == nil {
		return nil, errs.FailedPrecondition("label OG generation requires a light or dark logo")
	}
	return []og.Request{request}, nil
}

func (r *Requests) All(ctx context.Context, db *gorm.DB) ([]og.Request, error) {
	rows, err := loadLabelOGRows(ctx, db)
	if err != nil {
		return nil, err
	}
	requests := make([]og.Request, 0, len(rows))
	for _, row := range rows {
		featured := labelOGOptionalString(row.FeaturedImageFileID)
		if featured == nil {
			continue
		}
		requests = append(requests, og.Request{
			Target: og.Target{EntityType: "label", EntityID: row.ID, Kind: "entity"},
			Title:  row.Title, FeaturedImageFileID: featured,
		})
	}
	return requests, nil
}

type labelOGRow struct {
	ID                  string  `gorm:"column:id"`
	Title               string  `gorm:"column:title"`
	FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
}

func loadLabelOGRequest(ctx context.Context, db *gorm.DB, entityID string) (og.Request, error) {
	var row labelOGRow
	query := db.WithContext(ctx).Table("label").Select(labelOGSelect).
		Where("label.id = ?", entityID).Take(&row)
	if query.Error != nil {
		return og.Request{}, query.Error
	}
	return og.Request{
		Target: og.Target{EntityType: "label", EntityID: entityID, Kind: "entity"},
		Title:  row.Title, FeaturedImageFileID: labelOGOptionalString(row.FeaturedImageFileID),
	}, nil
}

func loadLabelOGRows(ctx context.Context, db *gorm.DB) ([]labelOGRow, error) {
	var rows []labelOGRow
	if err := db.WithContext(ctx).Table("label").Select(labelOGSelect).
		Order("label.id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

const labelOGSelect = `label.id,
	COALESCE(NULLIF((SELECT lt.title FROM label_translation AS lt
		JOIN label AS source ON source.id = lt.entity_id
			AND source.source_locale = lt.locale
		WHERE lt.entity_id = label.id LIMIT 1), ''), '') AS title,
	COALESCE(label.logo_light_file_id, label.logo_dark_file_id) AS featured_image_file_id`

func labelOGOptionalString(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}
