package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/scheduler"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestHandleScheduledRunsMetadataAIRecovery(t *testing.T) {
	processor := &metadataAIRecoveryTestProcessor{}
	handlers := &Handlers{db: newMetadataAIRecoveryTestDB(t), metadataAI: processor}

	require.NoError(t, handlers.HandleScheduled(context.Background(), scheduler.JobRecoverMetadataAI))
	require.Equal(t, 1, processor.calls)
	require.Zero(t, processor.limit, "the manager default keeps the recovery batch bounded")
}

func TestHandleScheduledPropagatesMetadataAIRecoveryFailure(t *testing.T) {
	expected := errors.New("recovery scan failed")
	processor := &metadataAIRecoveryTestProcessor{err: expected}
	handlers := &Handlers{db: newMetadataAIRecoveryTestDB(t), metadataAI: processor}

	require.ErrorIs(t, handlers.HandleScheduled(context.Background(), scheduler.JobRecoverMetadataAI), expected)
	require.Equal(t, 1, processor.calls)
}

func newMetadataAIRecoveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	return db
}

type metadataAIRecoveryTestProcessor struct {
	calls int
	limit int
	err   error
}

func (*metadataAIRecoveryTestProcessor) ProcessJob(context.Context, string) error { return nil }

func (p *metadataAIRecoveryTestProcessor) RecoverExpiredJobs(_ context.Context, limit int) (int, error) {
	p.calls++
	p.limit = limit
	return 0, p.err
}
