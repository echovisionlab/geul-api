package series

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type managerSummaryStub map[string]*managev1.SeriesManager

func (summaries managerSummaryStub) LoadSeriesManagers(context.Context, []string) (map[string]*managev1.SeriesManager, error) {
	return summaries, nil
}

func TestSeriesManagerResponseUsesRelationCreationTimeAndKeepsOrder(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	createdAt := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT .*member_id.*created_at.*FROM "series_manager"`).WithArgs("series-1").
		WillReturnRows(sqlmock.NewRows([]string{"member_id", "created_at"}).AddRow("member-2", createdAt).AddRow("member-1", createdAt.Add(time.Hour)))
	summaries := managerSummaryStub{
		"member-1": {MemberId: "member-1", Nickname: "First"},
		"member-2": {MemberId: "member-2", Nickname: "Second"},
	}
	service := &SeriesService{db: db, members: summaries}
	managers, err := service.loadSeriesManagerProjections(t.Context(), "series-1")
	require.NoError(t, err)
	require.Len(t, managers, 2)
	require.Equal(t, "member-2", managers[0].MemberId)
	require.Equal(t, "Second", managers[0].Nickname)
	require.Equal(t, createdAt, managers[0].CreatedAt.AsTime())
	require.Equal(t, createdAt.Add(time.Hour), managers[1].CreatedAt.AsTime())
	require.Nil(t, summaries["member-1"].CreatedAt, "relation metadata must not mutate a reused Member projection")
	require.NoError(t, mock.ExpectationsWereMet())
}
