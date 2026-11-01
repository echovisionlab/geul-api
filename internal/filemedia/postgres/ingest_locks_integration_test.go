//go:build integration

package filemediapostgres_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type ingestScope func(context.Context, func(domain.IngestSession) error) error

type advisoryUnits struct {
	mutation.UnitOfWork[domain.Store]
	scope ingestScope
}

func (u advisoryUnits) Transaction(ctx context.Context, authorization bool, work func(mutation.Tx[domain.Store]) error) error {
	return u.scope(ctx, func(session domain.IngestSession) error { return session.Units.Transaction(ctx, authorization, work) })
}
func ingestScopes(locks domain.IngestLocks) map[string]ingestScope {
	upload, file := uuid.NewString(), uuid.NewString()
	return map[string]ingestScope{
		"multipart": func(ctx context.Context, work func(domain.IngestSession) error) error {
			return locks.Multipart(ctx, upload, file, work)
		},
		"remote": func(ctx context.Context, work func(domain.IngestSession) error) error {
			return locks.RemoteImport(ctx, file, work)
		},
	}
}

func TestIngestAdvisoryLocksSerializeAndReleaseAfterCancellationIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	units := filemediapostgres.NewUnits(db, filemediapostgres.Ports{})
	for name, scope := range ingestScopes(filemediapostgres.NewIngestLocks(db, filemediapostgres.Ports{})) {
		t.Run(name, func(t *testing.T) {
			h := mutationtest.PostgresHarness[domain.Store](db, advisoryUnits{units, scope})
			var committed atomic.Bool
			require.NoError(t, mutationtest.RequireSecondWaits(t, h,
				func(context.Context, domain.Store) error { return nil },
				func(context.Context, domain.Store) error { committed.Store(true); return nil },
				func(context.Context, domain.Store) error { require.True(t, committed.Load()); return nil }))
			ctx, cancel := context.WithCancel(t.Context())
			err := scope(ctx, func(domain.IngestSession) error { cancel(); return ctx.Err() })
			require.ErrorIs(t, err, context.Canceled)
			ctx, released := context.WithTimeout(t.Context(), 2*time.Second)
			defer released()
			require.NoError(t, scope(ctx, func(domain.IngestSession) error { return nil }), "cancelled request must release the session lock")
		})
	}
}

func TestIngestAdvisoryScopeUsesOneConnectionAcrossDatabasePhasesIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(1)
	for name, scope := range ingestScopes(filemediapostgres.NewIngestLocks(db, filemediapostgres.Ports{})) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			require.NoError(t, scope(ctx, func(session domain.IngestSession) error {
				read := func(store domain.Store) error {
					_, err := store.UploadOwnerExists(ctx, "work", uuid.NewString())
					return err
				}
				require.NoError(t, session.Units.Read(ctx, read))
				require.NoError(t, session.Units.ReadLocked(ctx, read))
				require.NoError(t, session.Units.ReadCurrent(ctx, read))
				require.NoError(t, session.Units.Transaction(ctx, false, func(tx mutation.Tx[domain.Store]) error { return read(tx.Store()) }))
				result := session.FaviconCommits.Commit(ctx, read)
				require.Equal(t, domain.FaviconCommitted, result.Stage)
				return result.Err
			}))
		})
	}
}
