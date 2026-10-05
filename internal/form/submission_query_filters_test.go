package form

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestApplyFormSubmissionFiltersBuildsBoundLiteralSearchAndDateRange(t *testing.T) {
	db := newFormSubmissionQueryDryRunDB(t)
	from := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	search := `A%_\B`
	countryCode := "us"
	query, err := applyFormSubmissionFilters(db.Model(&model.FormSubmission{}), &managev1.ListFormSubmissionsRequest{
		Search:          &search,
		CountryCode:     &countryCode,
		CreatedAtFrom:   timestamppb.New(from),
		CreatedAtBefore: timestamppb.New(before),
	})
	require.NoError(t, err)

	result := query.Find(&[]model.FormSubmission{})
	require.NoError(t, result.Error)
	require.Contains(t, result.Statement.SQL.String(), "data::text ILIKE")
	require.Contains(t, result.Statement.SQL.String(), "ESCAPE")
	require.Contains(t, result.Statement.SQL.String(), "country_code =")
	require.Contains(t, result.Statement.SQL.String(), "created_at >=")
	require.Contains(t, result.Statement.SQL.String(), "created_at <")
	require.Contains(t, result.Statement.Vars, `%A\%\_\\B%`)
	require.Contains(t, result.Statement.Vars, "US")
	require.Contains(t, result.Statement.Vars, from)
	require.Contains(t, result.Statement.Vars, before)
}

func TestApplyFormSubmissionFiltersRejectsInvalidBoundsAndOversizedInput(t *testing.T) {
	tests := []struct {
		name string
		req  *managev1.ListFormSubmissionsRequest
	}{
		{
			name: "oversized search",
			req:  &managev1.ListFormSubmissionsRequest{Search: stringPointer(strings.Repeat("x", formSubmissionSearchMaxRunes+1))},
		},
		{
			name: "invalid country code",
			req:  &managev1.ListFormSubmissionsRequest{CountryCode: stringPointer("USA")},
		},
		{
			name: "empty timestamp range",
			req: &managev1.ListFormSubmissionsRequest{
				CreatedAtFrom:   timestamppb.New(time.Unix(100, 0)),
				CreatedAtBefore: timestamppb.New(time.Unix(100, 0)),
			},
		},
		{
			name: "invalid protobuf timestamp",
			req:  &managev1.ListFormSubmissionsRequest{CreatedAtFrom: &timestamppb.Timestamp{Seconds: 253402300800}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := applyFormSubmissionFilters(newFormSubmissionQueryDryRunDB(t), test.req)
			require.Error(t, err)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestApplyFormSubmissionSortAllowsAliasesAndKeepsStableTies(t *testing.T) {
	defaultSorted, err := applyFormSubmissionSort(newFormSubmissionQueryDryRunDB(t), nil)
	require.NoError(t, err)
	defaultSQL := defaultSorted.Find(&[]model.FormSubmission{}).Statement.SQL.String()
	require.Contains(t, defaultSQL, "ORDER BY created_at DESC,id DESC")

	ascending, err := applyFormSubmissionSort(newFormSubmissionQueryDryRunDB(t), []*commonv1.SortSpec{{
		Field: "createdAt",
		Order: commonv1.SortOrder_SORT_ORDER_ASC,
	}})
	require.NoError(t, err)
	ascendingSQL := ascending.Find(&[]model.FormSubmission{}).Statement.SQL.String()
	require.Contains(t, ascendingSQL, "ORDER BY created_at ASC,id DESC")

	descending, err := applyFormSubmissionSort(newFormSubmissionQueryDryRunDB(t), []*commonv1.SortSpec{{
		Field: "created_at",
		Order: commonv1.SortOrder_SORT_ORDER_DESC,
	}})
	require.NoError(t, err)
	descendingSQL := descending.Find(&[]model.FormSubmission{}).Statement.SQL.String()
	require.Contains(t, descendingSQL, "ORDER BY created_at DESC,id DESC")

	_, err = applyFormSubmissionSort(newFormSubmissionQueryDryRunDB(t), []*commonv1.SortSpec{{
		Field: "created_at; DROP TABLE form_submission",
		Order: commonv1.SortOrder_SORT_ORDER_ASC,
	}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	_, err = applyFormSubmissionSort(newFormSubmissionQueryDryRunDB(t), []*commonv1.SortSpec{{Field: "created_at"}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func newFormSubmissionQueryDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	return db
}

func stringPointer(value string) *string { return &value }
