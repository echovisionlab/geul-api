package public

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	releasepkg "github.com/echovisionlab/geul-api/internal/release"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

const releaseListTestDriverName = "dsub-release-public-list-test"

var (
	registerReleaseListTestDriver sync.Once
	releaseListTestScenarios      sync.Map
)

type releaseListTestScenario struct {
	mu      sync.Mutex
	queries []string
	args    [][]driver.NamedValue
}

func (scenario *releaseListTestScenario) record(query string, args []driver.NamedValue) {
	scenario.mu.Lock()
	defer scenario.mu.Unlock()
	scenario.queries = append(scenario.queries, query)
	scenario.args = append(scenario.args, append([]driver.NamedValue(nil), args...))
}

func (scenario *releaseListTestScenario) querySnapshot() ([]string, [][]driver.NamedValue) {
	scenario.mu.Lock()
	defer scenario.mu.Unlock()
	queries := append([]string(nil), scenario.queries...)
	args := make([][]driver.NamedValue, len(scenario.args))
	for index := range scenario.args {
		args[index] = append([]driver.NamedValue(nil), scenario.args[index]...)
	}
	return queries, args
}

type releaseListTestDriver struct{}

func (releaseListTestDriver) Open(name string) (driver.Conn, error) {
	value, ok := releaseListTestScenarios.Load(name)
	if !ok {
		return nil, errors.New("unknown Release public list test database")
	}
	return &releaseListTestConnection{scenario: value.(*releaseListTestScenario)}, nil
}

type releaseListTestConnection struct {
	scenario *releaseListTestScenario
}

func (*releaseListTestConnection) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (*releaseListTestConnection) Close() error { return nil }

func (*releaseListTestConnection) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (connection *releaseListTestConnection) QueryContext(
	_ context.Context,
	query string,
	args []driver.NamedValue,
) (driver.Rows, error) {
	connection.scenario.record(query, args)
	switch {
	case strings.Contains(query, `FROM "release"`):
		if err := requirePublishedReleaseListQuery(query, args); err != nil {
			return nil, err
		}
		contradictsPublicFence := strings.Contains(query, "status !=") ||
			strings.Contains(query, "status NOT IN")
		if strings.Contains(query, "count(*)") {
			if contradictsPublicFence {
				return newReleaseListTestRows([]string{"count"}, []driver.Value{int64(0)}), nil
			}
			return newReleaseListTestRows([]string{"count"}, []driver.Value{int64(1)}), nil
		}
		if contradictsPublicFence {
			return newReleaseListTestRows([]string{"id", "type", "status", "created_at", "updated_at"}), nil
		}
		return newReleaseListTestRows(
			[]string{"id", "type", "status", "created_at", "updated_at"},
			[]driver.Value{
				"11111111-1111-4111-8111-111111111111",
				managev1.ReleaseType_RELEASE_TYPE_ALBUM.String(),
				ReleaseStatusPublished,
				time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC),
			},
		), nil
	case strings.Contains(query, "release_translation"):
		return newReleaseListTestRows(
			[]string{"entity_id", "title"},
			[]driver.Value{"11111111-1111-4111-8111-111111111111", "Published release"},
		), nil
	case strings.Contains(query, "release_file"):
		return newReleaseListTestRows([]string{"release_id", "file_id"}), nil
	case strings.Contains(query, "release_artist"):
		return newReleaseListTestRows([]string{"release_id", "artist_id", "image_file_id"}), nil
	default:
		return nil, fmt.Errorf("unexpected Release public list query: %s", query)
	}
}

var _ driver.QueryerContext = (*releaseListTestConnection)(nil)

func requirePublishedReleaseListQuery(query string, args []driver.NamedValue) error {
	if !strings.Contains(query, "status =") {
		return errors.New("Release public list query is missing an exact status predicate")
	}
	foundPublished := false
	for _, argument := range args {
		if argument.Value == ReleaseStatusDraft {
			return errors.New("Release public list query contains the draft status")
		}
		if argument.Value == ReleaseStatusPublished {
			foundPublished = true
		}
	}
	if !foundPublished {
		return errors.New("Release public list query is missing the published status")
	}
	return nil
}

type releaseListTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func newReleaseListTestRows(columns []string, rows ...[]driver.Value) *releaseListTestRows {
	return &releaseListTestRows{columns: columns, values: rows}
}

func (rows *releaseListTestRows) Columns() []string { return rows.columns }
func (*releaseListTestRows) Close() error           { return nil }

func (rows *releaseListTestRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}

type releaseListTestMedia struct{}

func (releaseListTestMedia) ReadySourceAssets(
	context.Context,
	*gorm.DB,
	[]string,
	...string,
) (map[string]*commonv1.AssetRef, error) {
	return map[string]*commonv1.AssetRef{}, nil
}

func (releaseListTestMedia) TrackWaveforms(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error) {
	return map[string]*commonv1.AssetRef{}, nil
}

func (releaseListTestMedia) TrackSpectrograms(context.Context, *gorm.DB, []string) (map[string]*commonv1.AssetRef, error) {
	return map[string]*commonv1.AssetRef{}, nil
}

func (releaseListTestMedia) TrackHLS(context.Context, *gorm.DB, []string) (map[string]*commonv1.HlsMediaRef, error) {
	return map[string]*commonv1.HlsMediaRef{}, nil
}

func (releaseListTestMedia) DownloadRef(MediaFile) (*commonv1.ExpiringMediaRef, error) {
	return nil, nil
}

type releaseListTestArtists struct{}

func (releaseListTestArtists) LoadArtistSummaries(context.Context, []string) (map[string]releasepkg.ArtistSummary, error) {
	return map[string]releasepkg.ArtistSummary{}, nil
}

func (releaseListTestArtists) LoadArtistSummariesWithDB(
	context.Context,
	*gorm.DB,
	[]string,
) (map[string]releasepkg.ArtistSummary, error) {
	return map[string]releasepkg.ArtistSummary{}, nil
}

func newReleaseListTestService(t *testing.T) (*ReleaseService, *releaseListTestScenario) {
	t.Helper()
	registerReleaseListTestDriver.Do(func() {
		sql.Register(releaseListTestDriverName, releaseListTestDriver{})
	})
	dsn := uuid.NewString()
	scenario := &releaseListTestScenario{}
	releaseListTestScenarios.Store(dsn, scenario)
	sqlDB, err := sql.Open(releaseListTestDriverName, dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
		releaseListTestScenarios.Delete(dsn)
	})
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return &ReleaseService{db: db, media: releaseListTestMedia{}, artists: releaseListTestArtists{}}, scenario
}

func TestReleaseListDefaultsToPublishedAndReachesDatabase(t *testing.T) {
	service, scenario := newReleaseListTestService(t)

	response, err := service.List(t.Context(), connect.NewRequest(&openv1.ListReleasesRequest{}))
	require.NoError(t, err)
	require.Equal(t, int32(1), response.Msg.GetPagination().GetTotal())
	require.Len(t, response.Msg.GetReleases(), 1)
	require.Equal(t, "11111111-1111-4111-8111-111111111111", response.Msg.GetReleases()[0].GetId())
	require.Equal(t, "Published release", response.Msg.GetReleases()[0].GetTitle())

	queries, _ := scenario.querySnapshot()
	require.NotEmpty(t, queries)
}

func TestReleaseListPreservesCallerStatusValidation(t *testing.T) {
	t.Run("published", func(t *testing.T) {
		service, _ := newReleaseListTestService(t)
		response, err := service.List(t.Context(), connect.NewRequest(&openv1.ListReleasesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "status",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: ReleaseStatusPublished,
			}},
		}))
		require.NoError(t, err)
		require.Len(t, response.Msg.GetReleases(), 1)
	})

	t.Run("draft", func(t *testing.T) {
		service, scenario := newReleaseListTestService(t)
		_, err := service.List(t.Context(), connect.NewRequest(&openv1.ListReleasesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "status",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: ReleaseStatusDraft,
			}},
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		queries, _ := scenario.querySnapshot()
		require.Empty(t, queries)
	})

	t.Run("not published", func(t *testing.T) {
		service, scenario := newReleaseListTestService(t)
		response, err := service.List(t.Context(), connect.NewRequest(&openv1.ListReleasesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "status",
				Op:    commonv1.FilterOp_FILTER_OP_NEQ,
				Value: ReleaseStatusPublished,
			}},
		}))
		require.NoError(t, err)
		require.Empty(t, response.Msg.GetReleases())
		require.Zero(t, response.Msg.GetPagination().GetTotal())
		queries, _ := scenario.querySnapshot()
		require.NotEmpty(t, queries)
	})

	t.Run("not in published", func(t *testing.T) {
		service, scenario := newReleaseListTestService(t)
		response, err := service.List(t.Context(), connect.NewRequest(&openv1.ListReleasesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field:  "status",
				Op:     commonv1.FilterOp_FILTER_OP_NOT_IN,
				Values: []string{ReleaseStatusPublished},
			}},
		}))
		require.NoError(t, err)
		require.Empty(t, response.Msg.GetReleases())
		require.Zero(t, response.Msg.GetPagination().GetTotal())
		queries, _ := scenario.querySnapshot()
		require.NotEmpty(t, queries)
	})

	t.Run("unknown field", func(t *testing.T) {
		service, scenario := newReleaseListTestService(t)
		_, err := service.List(t.Context(), connect.NewRequest(&openv1.ListReleasesRequest{
			Filters: []*commonv1.FilterSpec{{
				Field: "unknown",
				Op:    commonv1.FilterOp_FILTER_OP_EQ,
				Value: "value",
			}},
		}))
		require.Error(t, err)
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
		queries, _ := scenario.querySnapshot()
		require.Empty(t, queries)
	})
}
