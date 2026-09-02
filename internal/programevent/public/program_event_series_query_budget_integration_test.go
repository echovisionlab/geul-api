//go:build integration

package public

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

type programEventSeriesQueryCounter struct {
	logger.Interface
	count *atomic.Int64
}

func (l programEventSeriesQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	l.count.Add(1)
	l.Interface.Trace(ctx, begin, fc, err)
}

func TestPublicProgramEventSeriesListQueryBudgetDoesNotGrowPerRowIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	suffix := uuid.NewString()
	for index := 0; index < 12; index++ {
		fileID, _ := seedCanonicalPublicFileFixture(
			t,
			db,
			fmt.Sprintf("program-event-series-%02d.webp", index),
			"image/webp",
			"poster",
		)
		require.NoError(t, db.Exec(`
			INSERT INTO program_event_series (
				id, slug, status, title, poster_file_id, created_at, updated_at
			) VALUES (?::uuid, ?, ?, ?, ?::uuid, NOW(), NOW())
		`, uuid.NewString(), fmt.Sprintf("event-series-%02d-%s", index, suffix),
			managev1.ProgramEventStatus_PROGRAM_EVENT_STATUS_PUBLISHED.String(),
			fmt.Sprintf("Event Series %02d", index), fileID).Error)
	}

	var queryCount atomic.Int64
	countedDB := db.Session(&gorm.Session{Logger: programEventSeriesQueryCounter{
		Interface: db.Config.Logger,
		count:     &queryCount,
	}})
	service := NewProgramEventSeriesService(countedDB, newPublicProgramEventAssets(countedDB, "https://cdn.example.com"))
	listCount := func(limit int32) int64 {
		queryCount.Store(0)
		response, err := service.List(context.Background(), connect.NewRequest(&openv1.ListProgramEventSeriesRequest{
			Pagination: &commonv1.PaginationRequest{Limit: limit},
		}))
		require.NoError(t, err)
		require.Len(t, response.Msg.Series, int(limit))
		return queryCount.Load()
	}

	oneRowQueries := listCount(1)
	twelveRowQueries := listCount(12)
	require.Equal(t, oneRowQueries, twelveRowQueries)
	require.LessOrEqual(t, twelveRowQueries, int64(3))
}
