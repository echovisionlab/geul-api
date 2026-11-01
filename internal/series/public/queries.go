package public

import commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

// ListSeriesQuery lists published Series. A zero Limit reads the default
// page; AcceptLanguage picks their localization.
type ListSeriesQuery struct {
	Limit          int32
	Offset         int32
	Filters        []*commonv1.FilterSpec
	Sorts          []*commonv1.SortSpec
	AcceptLanguage string
}
