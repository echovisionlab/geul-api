package workpublicpostgres

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestWorkFilterConfigOnlyAcceptsPublicStatuses(t *testing.T) {
	require.ElementsMatch(t, []string{
		managev1.WorkStatus_WORK_STATUS_PUBLISHED.String(),
		managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(),
	}, workFilterConfig.Fields["status"].EnumValues)
}

// A public read never names a draft, so a status filter or sort the public
// reads do not take is invalid before any query runs.
func TestPublicReadsRejectADraftStatusAndUnknownFieldsBeforeQuerying(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DryRun: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	draft := managev1.WorkStatus_WORK_STATUS_DRAFT.String()
	published := managev1.WorkStatus_WORK_STATUS_PUBLISHED.String()

	for name, filter := range map[string]*commonv1.FilterSpec{
		"draft equals":        {Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: draft},
		"draft not equals":    {Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ, Value: draft},
		"published and draft": {Field: "status", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{published, draft}},
		"unknown field":       {Field: "unknown", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "value"},
	} {
		_, err := applyPublicWorkFilters(db.Model(&model.Work{}), []*commonv1.FilterSpec{filter})
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), name)
	}
	_, err = workSortConfig.ApplySort(db.Model(&model.Work{}), []*commonv1.SortSpec{{Field: "unknown", Order: commonv1.SortOrder_SORT_ORDER_ASC}})
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}
