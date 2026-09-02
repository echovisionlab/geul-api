package mq

import (
	"context"
	"database/sql"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

type recordingSignalExecutor struct {
	query string
	args  []any
}

func (executor *recordingSignalExecutor) ExecContext(
	_ context.Context,
	query string,
	args ...any,
) (sql.Result, error) {
	executor.query = query
	executor.args = append([]any(nil), args...)
	return signalResult(1), nil
}

func (*recordingSignalExecutor) QueryContext(
	context.Context,
	string,
	...any,
) (*sql.Rows, error) {
	return nil, nil
}

func (*recordingSignalExecutor) QueryRowContext(
	context.Context,
	string,
	...any,
) *sql.Row {
	return &sql.Row{}
}

type signalResult int64

func (result signalResult) LastInsertId() (int64, error) { return int64(result), nil }
func (result signalResult) RowsAffected() (int64, error) { return int64(result), nil }

func TestProviderContentUpdatedUsesCallerTransactionWithoutInteractiveSuppression(t *testing.T) {
	t.Parallel()

	executor := &recordingSignalExecutor{}
	publisher := &Publisher{}
	err := publisher.PublishContentUpdatedWithExecutor(
		context.Background(),
		executor,
		&managev1.ContentUpdatedEvent{},
	)
	require.NoError(t, err)
	require.Equal(t, "SELECT pg_notify($1, $2)", executor.query)
	require.Len(t, executor.args, 2)
	require.Equal(t, "content.updated", executor.args[0])
	require.NotEmpty(t, executor.args[1])
}
