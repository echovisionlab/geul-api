package form

import (
	"strings"
	"unicode/utf8"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

const formSubmissionSearchMaxRunes = 200

var formSubmissionSortFields = map[string]string{
	"createdAt":  "created_at",
	"created_at": "created_at",
}

func applyFormSubmissionFilters(
	query *gorm.DB,
	req *managev1.ListFormSubmissionsRequest,
) (*gorm.DB, error) {
	search := req.GetSearch()
	if utf8.RuneCountInString(search) > formSubmissionSearchMaxRunes {
		return nil, errs.InvalidArgument("search", "must be at most 200 characters")
	}
	if search != "" {
		pattern := "%" + queryutil.EscapeILIKEPattern(search) + "%"
		query = query.Where("data::text ILIKE ? ESCAPE E'\\\\'", pattern)
	}

	if countryCode := strings.TrimSpace(req.GetCountryCode()); countryCode != "" {
		if len(countryCode) != 2 || !isASCIILetter(countryCode[0]) || !isASCIILetter(countryCode[1]) {
			return nil, errs.InvalidArgument("country_code", "must be a two-letter country code")
		}
		countryCode = strings.ToUpper(countryCode)
		query = query.Where("country_code = ?", countryCode)
	}

	createdAtFrom := req.GetCreatedAtFrom()
	createdAtBefore := req.GetCreatedAtBefore()
	if createdAtFrom != nil {
		if err := createdAtFrom.CheckValid(); err != nil {
			return nil, errs.InvalidArgument("created_at_from", "must be a valid timestamp")
		}
	}
	if createdAtBefore != nil {
		if err := createdAtBefore.CheckValid(); err != nil {
			return nil, errs.InvalidArgument("created_at_before", "must be a valid timestamp")
		}
	}
	if createdAtFrom != nil && createdAtBefore != nil && !createdAtFrom.AsTime().Before(createdAtBefore.AsTime()) {
		return nil, errs.InvalidArgument("created_at_from", "must be earlier than created_at_before")
	}
	if createdAtFrom != nil {
		query = query.Where("created_at >= ?", createdAtFrom.AsTime())
	}
	if createdAtBefore != nil {
		query = query.Where("created_at < ?", createdAtBefore.AsTime())
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
			return nil, errs.InvalidSortField(field)
		}

		var order string
		switch sort.GetOrder() {
		case commonv1.SortOrder_SORT_ORDER_ASC:
			order = "ASC"
		case commonv1.SortOrder_SORT_ORDER_DESC:
			order = "DESC"
		default:
			return nil, errs.InvalidArgument("sorts.order", "must be ASC or DESC")
		}
		query = query.Order(column + " " + order)
	}

	return query.Order("id DESC"), nil
}
