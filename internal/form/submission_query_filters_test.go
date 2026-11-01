package form

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestFormSubmissionListKeepsTheSearchAndUpperCasesTheCountry(t *testing.T) {
	from := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	list, err := formSubmissionList(ListFormSubmissionsQuery{
		FormID: "form-1", Search: `A%_\B`, CountryCode: " us ", CreatedAtFrom: &from, CreatedAtBefore: &before,
	})
	require.NoError(t, err)
	require.Equal(t, SubmissionList{
		FormID: "form-1", Search: `A%_\B`, CountryCode: "US", CreatedAtFrom: &from, CreatedAtBefore: &before,
		Order: []SubmissionOrder{},
	}, list)
}

func TestFormSubmissionListRejectsInvalidBoundsAndOversizedInput(t *testing.T) {
	bound := time.Unix(100, 0)
	tests := []struct {
		name  string
		query ListFormSubmissionsQuery
	}{
		{
			name:  "oversized search",
			query: ListFormSubmissionsQuery{Search: strings.Repeat("x", formSubmissionSearchMaxRunes+1)},
		},
		{
			name:  "invalid country code",
			query: ListFormSubmissionsQuery{CountryCode: "USA"},
		},
		{
			name:  "empty timestamp range",
			query: ListFormSubmissionsQuery{CreatedAtFrom: &bound, CreatedAtBefore: &bound},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := formSubmissionList(test.query)
			require.Error(t, err)
			require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
		})
	}
}

func TestListFormSubmissionsTranslatorRejectsInvalidProtobufTimestamp(t *testing.T) {
	_, err := listFormSubmissionsQuery(&managev1.ListFormSubmissionsRequest{CreatedAtFrom: &timestamppb.Timestamp{Seconds: 253402300800}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
	require.Equal(t, "created_at_from: must be a valid timestamp", errs.Wrap(err).Message())
}

func TestFormSubmissionOrderAllowsAliasesAndRejectsOtherSorts(t *testing.T) {
	order, err := formSubmissionOrder([]*commonv1.SortSpec{
		{Field: "createdAt", Order: commonv1.SortOrder_SORT_ORDER_ASC},
		{Field: "created_at", Order: commonv1.SortOrder_SORT_ORDER_DESC},
	})
	require.NoError(t, err)
	require.Equal(t, []SubmissionOrder{{}, {Descending: true}}, order)

	_, err = formSubmissionOrder([]*commonv1.SortSpec{{
		Field: "created_at; DROP TABLE form_submission",
		Order: commonv1.SortOrder_SORT_ORDER_ASC,
	}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = formSubmissionOrder([]*commonv1.SortSpec{{Field: "created_at"}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}
