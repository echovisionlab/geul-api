package series

import commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

// ListSeriesAdminQuery lists every Series for an administrator, filtered
// and sorted in the shared query language.
type ListSeriesAdminQuery struct {
	Page    Page
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}
