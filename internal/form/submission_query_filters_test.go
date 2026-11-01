package form

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
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
	query, err := applyFormSubmissionFilters(db.Model(&model.FormSubmission{}), ListFormSubmissionsQuery{
		Search:          search,
		CountryCode:     countryCode,
		CreatedAtFrom:   &from,
		CreatedAtBefore: &before,
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
			_, err := applyFormSubmissionFilters(newFormSubmissionQueryDryRunDB(t), test.query)
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
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())

	_, err = applyFormSubmissionSort(newFormSubmissionQueryDryRunDB(t), []*commonv1.SortSpec{{Field: "created_at"}})
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code())
}

func newFormSubmissionQueryDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	return db
}
