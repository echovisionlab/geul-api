//go:build integration

package postgreslock

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWithAdvisoryLeaderAllowsOnlyOneConcurrentRunnerIntegration(t *testing.T) {
	db := newConcurrentPostgres(t)
	const lockKey = int64(0x4745554c54455354) // GEULTEST
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- WithAdvisoryLeader(t.Context(), db, lockKey, func(context.Context) error {
			close(firstStarted)
			<-releaseFirst
			return nil
		})
	}()

	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for advisory leader")
	}
	secondRan := false
	require.NoError(t, WithAdvisoryLeader(t.Context(), db, lockKey, func(context.Context) error {
		secondRan = true
		return nil
	}))
	require.False(t, secondRan)

	close(releaseFirst)
	require.NoError(t, <-firstDone)
	require.NoError(t, WithAdvisoryLeader(t.Context(), db, lockKey, func(context.Context) error {
		secondRan = true
		return nil
	}))
	require.True(t, secondRan)
}

func TestWithAdvisoryLeaderReleasesAfterCancellationAndWorkErrorIntegration(t *testing.T) {
	db := newConcurrentPostgres(t)
	const lockKey = int64(0x4745554c43414e43) // GEULCANC
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- WithAdvisoryLeader(ctx, db, lockKey, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for advisory leader")
	}
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	wantErr := errors.New("work failed")
	require.ErrorIs(t, WithAdvisoryLeader(t.Context(), db, lockKey, func(context.Context) error { return wantErr }), wantErr)
	ranAfterRelease := false
	require.NoError(t, WithAdvisoryLeader(t.Context(), db, lockKey, func(context.Context) error {
		ranAfterRelease = true
		return nil
	}))
	require.True(t, ranAfterRelease)
}

func newConcurrentPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	pg := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{})
	db, err := gorm.Open(postgres.Open(pg.DSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}
