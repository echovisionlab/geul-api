package query

import (
	"database/sql"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/structured"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPagination(t *testing.T) {
	cfg := PaginationConfig{DefaultLimit: 20, MaxLimit: 50}

	assert.Equal(t, Pagination{Limit: 20, Offset: 0}, ExtractPagination(nil, cfg))
	assert.Equal(t, Pagination{Limit: 10, Offset: 5}, ExtractPaginationRaw(10, 5, cfg))
	assert.Equal(t, Pagination{Limit: 50, Offset: 0}, ExtractPaginationRaw(500, -10, cfg))

	p := Pagination{Limit: 10, Offset: 20}
	assert.True(t, p.HasMore(31))
	assert.False(t, p.HasMore(30))
	assert.Equal(t, &commonv1.PaginationResponse{
		Total:   31,
		Limit:   10,
		Offset:  20,
		HasMore: true,
	}, p.BuildResponse(31))

	params := GetPaginationParams(0, -1, 25)
	assert.Equal(t, PaginationParams{Limit: 25, Offset: 0}, params)
}

func TestNormalizePaginationParams(t *testing.T) {
	limit, offset := NormalizePaginationParams(&commonv1.PaginationRequest{Limit: 500, Offset: -10})
	assert.Equal(t, int32(100), limit)
	assert.Zero(t, offset)
}

func TestSortConfig(t *testing.T) {
	db := newDryRunDB(t).Table("post")
	cfg := SortConfig{
		AllowedFields: map[string]string{
			"title":      "post.title",
			"created_at": "post.created_at",
		},
		DefaultSort: "post.created_at DESC, post.id ASC",
	}

	q, err := cfg.ApplySort(db, nil)
	require.NoError(t, err)
	assert.Contains(t, q.Find(&[]struct{}{}).Statement.SQL.String(), "ORDER BY post.created_at DESC, post.id ASC")

	q, err = cfg.ApplySort(newDryRunDB(t).Table("post"), []*commonv1.SortSpec{
		{Field: "title", Order: commonv1.SortOrder_SORT_ORDER_ASC},
		{Field: "created_at", Order: commonv1.SortOrder_SORT_ORDER_DESC},
	})
	require.NoError(t, err)
	sql := q.Find(&[]struct{}{}).Statement.SQL.String()
	assert.Contains(t, sql, "ORDER BY post.title ASC,post.created_at DESC")

}

func TestFilterConfigAppliesFilters(t *testing.T) {
	db := newDryRunDB(t).Table("post")
	cfg := FilterConfig{
		Fields: map[string]FieldDef{
			"id":         {Column: "post.id", Type: TypeID, AllowedOps: IDOps},
			"title":      {Column: "post.title", Type: TypeText, AllowedOps: TextOps},
			"status":     {Column: "post.status", Type: TypeEnum, AllowedOps: EnumOps, EnumValues: []string{"draft", "published"}},
			"score":      {Column: "post.score", Type: TypeNumber, AllowedOps: NumberOps},
			"published":  {Column: "post.published", Type: TypeBool, AllowedOps: BoolOps},
			"created_at": {Column: "post.created_at", Type: TypeDate, AllowedOps: DateOps},
			"search": {
				Type:          TypeText,
				AllowedOps:    SearchOps,
				SearchColumns: []string{"post.title", "post.summary"},
			},
		},
		DefaultFilters: []*commonv1.FilterSpec{
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ, Value: "draft"},
		},
	}

	q, err := cfg.ApplyFilters(db, []*commonv1.FilterSpec{
		{Field: "title", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: `100%_match\ok`},
		{Field: "score", Op: commonv1.FilterOp_FILTER_OP_GTE, Value: "10"},
		{Field: "published", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "true"},
		{Field: "created_at", Op: commonv1.FilterOp_FILTER_OP_LT, Value: "2026-04-20"},
		{Field: "id", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{
			"11111111-1111-4111-8111-111111111111",
			"22222222-2222-4222-8222-222222222222",
		}},
		{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "needle"},
	})
	require.NoError(t, err)

	stmt := q.Find(&[]struct{}{}).Statement
	sqlText := stmt.SQL.String()
	assert.Contains(t, sqlText, "post.status != $1")
	assert.Contains(t, sqlText, "post.title ILIKE")
	assert.Contains(t, sqlText, "post.score >= ")
	assert.Contains(t, sqlText, "post.published = ")
	assert.Contains(t, sqlText, "post.created_at < ")
	assert.Contains(t, sqlText, "post.id IN")
	assert.Contains(t, sqlText, "post.title ILIKE")
	assert.Contains(t, sqlText, "post.summary ILIKE")
	assert.Contains(t, stmt.Vars, `%100\%\_match\\ok%`)
	assert.Contains(t, stmt.Vars, int64(10))
	assert.Contains(t, stmt.Vars, true)
	assert.Contains(t, stmt.Vars, "11111111-1111-4111-8111-111111111111")
}

func TestFilterConfigUserFilterOverridesDefault(t *testing.T) {
	db := newDryRunDB(t).Table("post")
	cfg := FilterConfig{
		Fields: map[string]FieldDef{
			"status": {Column: "post.status", Type: TypeEnum, AllowedOps: EnumOps, EnumValues: []string{"draft", "published"}},
		},
		DefaultFilters: []*commonv1.FilterSpec{
			{Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ, Value: "draft"},
		},
	}

	q, err := cfg.ApplyFilters(db, []*commonv1.FilterSpec{
		{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "published"},
	})
	require.NoError(t, err)

	stmt := q.Find(&[]struct{}{}).Statement
	sqlText := stmt.SQL.String()
	assert.Contains(t, sqlText, "post.status = ")
	assert.NotContains(t, sqlText, "post.status !=")
	assert.Equal(t, structured.Values{"published"}, stmt.Vars)
}

func TestFilterConfigValidationErrors(t *testing.T) {
	db := newDryRunDB(t).Table("post")
	cfg := FilterConfig{
		Fields: map[string]FieldDef{
			"id":        {Column: "post.id", Type: TypeID, AllowedOps: IDOps},
			"title":     {Column: "post.title", Type: TypeText, AllowedOps: TextOps},
			"score":     {Column: "post.score", Type: TypeNumber, AllowedOps: NumberOps},
			"published": {Column: "post.published", Type: TypeBool, AllowedOps: BoolOps},
			"status":    {Column: "post.status", Type: TypeEnum, AllowedOps: EnumOps, EnumValues: []string{"draft"}},
		},
	}

	for _, tc := range []struct {
		name   string
		filter *commonv1.FilterSpec
	}{
		{name: "unknown field", filter: &commonv1.FilterSpec{Field: "unknown", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "x"}},
		{name: "invalid op", filter: &commonv1.FilterSpec{Field: "score", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: "1"}},
		{name: "missing value", filter: &commonv1.FilterSpec{Field: "title", Op: commonv1.FilterOp_FILTER_OP_EQ}},
		{name: "empty in", filter: &commonv1.FilterSpec{Field: "id", Op: commonv1.FilterOp_FILTER_OP_IN}},
		{name: "invalid uuid", filter: &commonv1.FilterSpec{Field: "id", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "not-uuid"}},
		{name: "invalid number", filter: &commonv1.FilterSpec{Field: "score", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "nan-ish"}},
		{name: "invalid bool", filter: &commonv1.FilterSpec{Field: "published", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "maybe"}},
		{name: "invalid enum", filter: &commonv1.FilterSpec{Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "published"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := cfg.ApplyFilters(db, []*commonv1.FilterSpec{tc.filter})
			require.Error(t, err)
			assert.Nil(t, q)
			assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		})
	}
}

func TestEscapeILIKEPattern(t *testing.T) {
	assert.Equal(t, `100\%\_done\\ok`, EscapeILIKEPattern(`100%_done\ok`))
}

func newDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()

	sqlDB, err := sql.Open("pgx", "postgres://user:pass@127.0.0.1:1/db?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DryRun:               true,
		DisableAutomaticPing: true,
	})
	require.NoError(t, err)
	return db
}
