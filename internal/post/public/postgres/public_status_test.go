package postpublicpostgres

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// A public read never names a draft, so a status filter the public reads
// do not take is invalid before any query runs.
func TestPublicPostReadsRejectADraftStatusBeforeQuerying(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mock.ExpectationsWereMet()) })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DryRun: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	draft := managev1.PostStatus_POST_STATUS_DRAFT.String()
	published := managev1.PostStatus_POST_STATUS_PUBLISHED.String()

	for name, filter := range map[string]*commonv1.FilterSpec{
		"draft equals":        {Field: "status", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: draft},
		"draft inequality":    {Field: "status", Op: commonv1.FilterOp_FILTER_OP_NEQ, Value: draft},
		"draft exclusion":     {Field: "status", Op: commonv1.FilterOp_FILTER_OP_NOT_IN, Values: []string{draft}},
		"published and draft": {Field: "status", Op: commonv1.FilterOp_FILTER_OP_IN, Values: []string{published, draft}},
		"unknown field":       {Field: "unknown", Op: commonv1.FilterOp_FILTER_OP_EQ, Value: "value"},
		"category range":      {Field: "category_id", Op: commonv1.FilterOp_FILTER_OP_NEQ, Value: "category"},
	} {
		_, err := applyPublicPostFilters(db.Model(&model.Post{}), []*commonv1.FilterSpec{filter})
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code(), name)
	}
}
