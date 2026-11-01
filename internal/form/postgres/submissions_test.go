package formpostgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
)

func TestFilterSubmissionsBuildsBoundLiteralSearchAndDateRange(t *testing.T) {
	from := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)
	before := from.Add(time.Hour)
	query := filterSubmissions(newSubmissionQueryDryRunDB(t).Model(&model.FormSubmission{}), form.SubmissionList{
		Search: `A%_\B`, CountryCode: "US", CreatedAtFrom: &from, CreatedAtBefore: &before,
	})

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

func TestSubmissionOrderKeepsStableTies(t *testing.T) {
	for _, test := range []struct {
		order []form.SubmissionOrder
		sql   string
	}{
		{sql: "ORDER BY created_at DESC,id DESC"},
		{order: []form.SubmissionOrder{{}}, sql: "ORDER BY created_at ASC,id DESC"},
		{order: []form.SubmissionOrder{{Descending: true}}, sql: "ORDER BY created_at DESC,id DESC"},
	} {
		sorted := submissionOrder(newSubmissionQueryDryRunDB(t), test.order)
		require.Contains(t, sorted.Find(&[]model.FormSubmission{}).Statement.SQL.String(), test.sql)
	}
}

func newSubmissionQueryDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	return db
}
