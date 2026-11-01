package form

import (
	"strings"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"gorm.io/gorm"
)

const formSubmissionSearchMaxRunes = 200

var formSubmissionSortFields = map[string]string{
	"createdAt":  "created_at",
	"created_at": "created_at",
}

func applyFormSubmissionFilters(query *gorm.DB, filters ListFormSubmissionsQuery) (*gorm.DB, error) {
	if utf8.RuneCountInString(filters.Search) > formSubmissionSearchMaxRunes {
		return nil, domainerr.InvalidField("search", "must be at most 200 characters")
	}
	if filters.Search != "" {
		pattern := "%" + queryutil.EscapeILIKEPattern(filters.Search) + "%"
		query = query.Where("data::text ILIKE ? ESCAPE E'\\\\'", pattern)
	}

	if countryCode := strings.TrimSpace(filters.CountryCode); countryCode != "" {
		if len(countryCode) != 2 || !isASCIILetter(countryCode[0]) || !isASCIILetter(countryCode[1]) {
			return nil, domainerr.InvalidField("country_code", "must be a two-letter country code")
		}
		countryCode = strings.ToUpper(countryCode)
		query = query.Where("country_code = ?", countryCode)
	}

	if filters.CreatedAtFrom != nil && filters.CreatedAtBefore != nil && !filters.CreatedAtFrom.Before(*filters.CreatedAtBefore) {
		return nil, domainerr.InvalidField("created_at_from", "must be earlier than created_at_before")
	}
	if filters.CreatedAtFrom != nil {
		query = query.Where("created_at >= ?", *filters.CreatedAtFrom)
	}
	if filters.CreatedAtBefore != nil {
		query = query.Where("created_at < ?", *filters.CreatedAtBefore)
	}

	return query, nil
}

func isASCIILetter(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}

func applyFormSubmissionSort(query *gorm.DB, sorts []*commonv1.SortSpec) (*gorm.DB, error) {
	if len(sorts) == 0 {
		return query.Order("created_at DESC").Order("id DESC"), nil
	}

	for _, sort := range sorts {
		field := sort.GetField()
		column, ok := formSubmissionSortFields[field]
		if !ok {
			return nil, domainerr.New(domainerr.Invalid, "", "invalid sort field: "+field)
		}

		var order string
		switch sort.GetOrder() {
		case commonv1.SortOrder_SORT_ORDER_ASC:
			order = "ASC"
		case commonv1.SortOrder_SORT_ORDER_DESC:
			order = "DESC"
		default:
			return nil, domainerr.InvalidField("sorts.order", "must be ASC or DESC")
		}
		query = query.Order(column + " " + order)
	}

	return query.Order("id DESC"), nil
}
