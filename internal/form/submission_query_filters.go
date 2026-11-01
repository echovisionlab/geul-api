package form

import (
	"strings"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

const formSubmissionSearchMaxRunes = 200

var formSubmissionSortFields = map[string]struct{}{
	"createdAt":  {},
	"created_at": {},
}

// formSubmissionList validates the filters and sorts of query.
func formSubmissionList(query ListFormSubmissionsQuery) (SubmissionList, error) {
	list := SubmissionList{FormID: query.FormID}
	if utf8.RuneCountInString(query.Search) > formSubmissionSearchMaxRunes {
		return SubmissionList{}, domainerr.InvalidField("search", "must be at most 200 characters")
	}
	list.Search = query.Search

	if countryCode := strings.TrimSpace(query.CountryCode); countryCode != "" {
		if len(countryCode) != 2 || !isASCIILetter(countryCode[0]) || !isASCIILetter(countryCode[1]) {
			return SubmissionList{}, domainerr.InvalidField("country_code", "must be a two-letter country code")
		}
		list.CountryCode = strings.ToUpper(countryCode)
	}

	if query.CreatedAtFrom != nil && query.CreatedAtBefore != nil && !query.CreatedAtFrom.Before(*query.CreatedAtBefore) {
		return SubmissionList{}, domainerr.InvalidField("created_at_from", "must be earlier than created_at_before")
	}
	list.CreatedAtFrom, list.CreatedAtBefore = query.CreatedAtFrom, query.CreatedAtBefore

	order, err := formSubmissionOrder(query.Sorts)
	if err != nil {
		return SubmissionList{}, err
	}
	list.Order = order
	return list, nil
}

func isASCIILetter(value byte) bool {
	return (value >= 'A' && value <= 'Z') || (value >= 'a' && value <= 'z')
}

func formSubmissionOrder(sorts []*commonv1.SortSpec) ([]SubmissionOrder, error) {
	order := make([]SubmissionOrder, 0, len(sorts))
	for _, sort := range sorts {
		field := sort.GetField()
		if _, ok := formSubmissionSortFields[field]; !ok {
			return nil, domainerr.New(domainerr.Invalid, "", "invalid sort field: "+field)
		}
		switch sort.GetOrder() {
		case commonv1.SortOrder_SORT_ORDER_ASC:
			order = append(order, SubmissionOrder{})
		case commonv1.SortOrder_SORT_ORDER_DESC:
			order = append(order, SubmissionOrder{Descending: true})
		default:
			return nil, domainerr.InvalidField("sorts.order", "must be ASC or DESC")
		}
	}
	return order, nil
}
