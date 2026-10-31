package og

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// EntityRequestSpec declares a non-localized entity family: one OG image per
// entity row, rendered only when the row has a featured image.
type EntityRequestSpec struct {
	EntityType              string
	Table                   string
	TitleExpression         string
	FeaturedImageExpression string
	// MissingImage is the client message for a row without a featured image.
	MissingImage string
}

// EntitySource is the RequestSource of one non-localized entity family.
type EntitySource struct {
	spec EntityRequestSpec
}

func NewEntitySource(spec EntityRequestSpec) *EntitySource {
	return &EntitySource{spec: spec}
}

func (s *EntitySource) Handles(entityType string) bool { return entityType == s.spec.EntityType }

func (s *EntitySource) Resolve(
	ctx context.Context,
	db *gorm.DB,
	entityType, entityID string,
	selection *managev1.OgTargetSelection,
) ([]Request, error) {
	if !s.Handles(entityType) {
		return nil, errs.InvalidEntityType(entityType)
	}
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return nil, errs.Required("entity_id")
	}
	if selection == nil || selection.GetPrimary() == nil {
		return nil, errs.InvalidArgument("selection", "base-only OG entities require primary target")
	}
	var row entityRow
	if err := db.WithContext(ctx).Table(s.spec.Table).Select(s.selectClause()).
		Where(s.spec.Table+".id = ?", entityID).Take(&row).Error; err != nil {
		return nil, err
	}
	request := s.request(entityID, row)
	if request.FeaturedImageFileID == nil {
		return nil, errs.FailedPrecondition(s.spec.MissingImage)
	}
	return []Request{request}, nil
}

// All skips rows without a featured image; they have nothing to render.
func (s *EntitySource) All(ctx context.Context, db *gorm.DB) ([]Request, error) {
	var rows []entityRow
	if err := db.WithContext(ctx).Table(s.spec.Table).Select(s.selectClause()).
		Order(s.spec.Table + ".id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	requests := make([]Request, 0, len(rows))
	for _, row := range rows {
		if request := s.request(row.ID, row); request.FeaturedImageFileID != nil {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

type entityRow struct {
	ID                  string  `gorm:"column:id"`
	Title               string  `gorm:"column:title"`
	FeaturedImageFileID *string `gorm:"column:featured_image_file_id"`
}

func (s *EntitySource) selectClause() string {
	return fmt.Sprintf(
		"%s.id, %s AS title, %s AS featured_image_file_id",
		s.spec.Table, s.spec.TitleExpression, s.spec.FeaturedImageExpression,
	)
}

func (s *EntitySource) request(entityID string, row entityRow) Request {
	return Request{
		Target: Target{EntityType: s.spec.EntityType, EntityID: entityID, Kind: "entity"},
		Title:  row.Title, FeaturedImageFileID: optionalFileID(row.FeaturedImageFileID),
	}
}

var (
	_ RequestSource    = (*EntitySource)(nil)
	_ AllRequestSource = (*EntitySource)(nil)
	_ RequestSource    = (*LocalizedSource)(nil)
	_ AllRequestSource = (*LocalizedSource)(nil)
)
