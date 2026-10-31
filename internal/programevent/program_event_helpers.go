package programevent

import (
	"context"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func toProtoProgramEventSummary(event *model.ProgramEvent, posterFileID *string) *managev1.ProgramEventSummary {
	slug := event.Slug
	return &managev1.ProgramEventSummary{
		Id:           event.ID,
		Status:       programEventStatusToProto(event.Status),
		SourceLocale: event.SourceLocale,
		Title:        event.Title,
		Slug:         &slug,
		TypeId:       event.TypeID,
		SeriesId:     event.SeriesID,
		StartsAt:     timestamppb.New(event.StartsAt),
		EndsAt:       timestampProtoPtr(event.EndsAt),
		Timezone:     event.Timezone,
		LocationMode: locationModeToProto(event.LocationMode),
		MapPlaceId:   event.MapPlaceID,
		PosterFileId: posterFileID,
		PublishedAt:  timestampProtoPtr(event.PublishedAt),
		UpdatedAt:    timestamppb.New(event.UpdatedAt),
	}
}

func collectProgramEventIDs(events []model.ProgramEvent) []string {
	ids := make([]string, 0, len(events))
	for i := range events {
		ids = append(ids, events[i].ID)
	}
	return ids
}

func validateProgramEventSlug(slug string) (string, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return "", domainerr.Required("slug")
	}
	if err := pathslug.Validate(slug); err != nil {
		return "", err
	}
	if len(slug) > 160 {
		return "", domainerr.InvalidField("slug", "must be at most 160 characters")
	}
	for _, r := range slug {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '-' {
			return "", domainerr.InvalidField("slug", "can only contain lowercase letters, numbers, and hyphens")
		}
	}
	return slug, nil
}

func normalizeRequiredProgramEventLocale(field, locale string) (string, error) {
	normalized := localization.NormalizeSupportedLocale(locale)
	if normalized == nil {
		return "", domainerr.InvalidField(field, "unsupported locale")
	}
	return *normalized, nil
}

func validateProgramEventLocation(mode model.ProgramEventLocationMode, mapPlaceID *string) error {
	switch mode {
	case model.ProgramEventLocationModeMapPlace, model.ProgramEventLocationModeHybrid:
		if mapPlaceID == nil || strings.TrimSpace(*mapPlaceID) == "" {
			return domainerr.Required("map_place_id")
		}
	case model.ProgramEventLocationModeOnline, model.ProgramEventLocationModeTBA:
	default:
		return domainerr.InvalidField("location_mode", "unsupported location mode")
	}
	return nil
}

func validateProgramEventTimeRange(startsAt time.Time, endsAt *time.Time) error {
	if endsAt != nil && endsAt.Before(startsAt) {
		return domainerr.InvalidField("ends_at", "must be greater than or equal to starts_at")
	}
	return nil
}

func programEventSeriesStatusStorageValue(status managev1.ProgramEventSeriesStatus) (model.ProgramEventStatus, error) {
	stored, ok := programEventSeriesStatusFromProto(status)
	if !ok {
		return "", domainerr.InvalidField("status", "must be draft or published")
	}
	return stored, nil
}

func mapProgramEventSeriesFilters(filters []*commonv1.FilterSpec) ([]*commonv1.FilterSpec, error) {
	mapped := make([]*commonv1.FilterSpec, 0, len(filters))
	for _, filter := range filters {
		if filter == nil || filter.GetField() != "status" {
			mapped = append(mapped, filter)
			continue
		}
		mappedFilter := &commonv1.FilterSpec{Field: filter.GetField(), Op: filter.GetOp()}
		if filter.GetOp() == commonv1.FilterOp_FILTER_OP_IN || filter.GetOp() == commonv1.FilterOp_FILTER_OP_NOT_IN {
			mappedFilter.Values = make([]string, 0, len(filter.GetValues()))
			for _, value := range filter.GetValues() {
				storageValue, err := programEventSeriesFilterStorageValue(value)
				if err != nil {
					return nil, err
				}
				mappedFilter.Values = append(mappedFilter.Values, string(storageValue))
			}
		} else {
			storageValue, err := programEventSeriesFilterStorageValue(filter.GetValue())
			if err != nil {
				return nil, err
			}
			mappedFilter.Value = string(storageValue)
		}
		mapped = append(mapped, mappedFilter)
	}
	return mapped, nil
}

func programEventSeriesFilterStorageValue(value string) (model.ProgramEventStatus, error) {
	stored, ok := programEventSeriesStatusFilterValues[value]
	if !ok {
		return "", domainerr.InvalidField("status", "must be draft or published")
	}
	return stored, nil
}

// pageLimitOffset is the page a list query asks for; Limit defaults to 50.
func pageLimitOffset(limit, offset int32) (int32, int32) {
	if limit <= 0 {
		limit = 50
	}
	return limit, offset
}

func timestampPtr(ts *timestamppb.Timestamp) *time.Time {
	if ts == nil {
		return nil
	}
	value := ts.AsTime()
	return &value
}

func timestampProtoPtr(ts *time.Time) *timestamppb.Timestamp {
	if ts == nil {
		return nil
	}
	return timestamppb.New(*ts)
}

func nullableString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func countPresent(values ...*string) int {
	count := 0
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			count++
		}
	}
	return count
}

func normalizedStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func nextProgramEventCreditSortOrder(ctx context.Context, db *gorm.DB, eventID string) (int32, error) {
	var row struct {
		SortOrder int32 `gorm:"column:sort_order"`
	}
	if err := db.WithContext(ctx).
		Raw("SELECT COALESCE(MAX(sort_order), -1) + 1 AS sort_order FROM program_event_credit WHERE event_id = ?", eventID).
		Scan(&row).Error; err != nil {
		return 0, err
	}
	return row.SortOrder, nil
}

func touchProgramEvent(ctx context.Context, db *gorm.DB, eventID string) error {
	if err := db.WithContext(ctx).
		Model(&model.ProgramEvent{}).
		Where("id = ?", eventID).
		Update("updated_at", time.Now().UTC()).Error; err != nil {
		return err
	}
	return nil
}

// uuidParseCanonical parses value as the canonical UUID field names.
func uuidParseCanonical(value, field string) (uuid.UUID, error) {
	parsed, err := uuidutil.ParseCanonical(value, field)
	if err != nil {
		return uuid.Nil, domainerr.InvalidField(field, err.Error())
	}
	return parsed, nil
}
