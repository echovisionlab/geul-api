package programevent

import (
	"strings"
	"time"
	"unicode"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/pathslug"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/google/uuid"
)

func collectProgramEventIDs(events []Event) []string {
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

func validateProgramEventLocation(mode LocationMode, mapPlaceID *string) error {
	switch mode {
	case LocationModeMapPlace, LocationModeHybrid:
		if mapPlaceID == nil || strings.TrimSpace(*mapPlaceID) == "" {
			return domainerr.Required("map_place_id")
		}
	case LocationModeOnline, LocationModeTBA:
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

// mapProgramEventSeriesFilters rewrites a status filter's series API names
// to the stored values.
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

func programEventSeriesFilterStorageValue(value string) (Status, error) {
	stored, ok := seriesStatusFilterValues[value]
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

// uuidParseCanonical parses value as the canonical UUID field names.
func uuidParseCanonical(value, field string) (uuid.UUID, error) {
	parsed, err := uuidutil.ParseCanonical(value, field)
	if err != nil {
		return uuid.Nil, domainerr.InvalidField(field, err.Error())
	}
	return parsed, nil
}

func sameNullableString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameNullableTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameNullableInt32(left, right *int32) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameStringSlice(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// pagination is where a page of limit and offset sits among total matches,
// counting the rows it holds.
func pagination(limit, offset int32, rows int, total int64) Pagination {
	return Pagination{
		Total: int32(total), Limit: limit, Offset: offset,
		HasMore: int64(offset)+int64(rows) < total,
	}
}
