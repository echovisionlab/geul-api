//go:build integration

package filemediapostgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFaviconCommitDriverJoinsStoreWritesIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	file, asset := uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Create(&model.File{ID: file, FileName: "favicon", Extension: "png", MimeType: "image/png", FileSize: 64}).Error)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Create(&model.PublicAsset{ID: asset, Kind: "favicon", Extension: "png", MimeType: "image/png", ObjectKey: "asset/" + asset + ".png", Disposition: "inline", Status: model.PublicAssetStatusReady, FileSize: new(int64(64)), SHA256: make([]byte, 32), ReadyAt: &now}).Error)
	commits := filemediapostgres.NewFaviconCommits(db, filemediapostgres.Ports{})
	abort := errors.New("owner rollback")
	result := commits.Commit(t.Context(), func(s domain.Store) error {
		require.NoError(t, s.InsertFaviconDerivative(t.Context(), uuid.NewString(), file, "FILE_DERIVATIVE_TYPE_FAVICON_PNG_32", asset))
		return abort
	})
	require.Equal(t, domain.FaviconChangeFailed, result.Stage)
	require.ErrorIs(t, result.Err, abort)
	var count int64
	require.NoError(t, db.Table("file_derivative").Where("file_id = ?", file).Count(&count).Error)
	require.Zero(t, count)
	result = commits.Commit(t.Context(), func(s domain.Store) error {
		return s.InsertFaviconDerivative(t.Context(), uuid.NewString(), file, "FILE_DERIVATIVE_TYPE_FAVICON_PNG_32", asset)
	})
	require.Equal(t, domain.FaviconCommitted, result.Stage)
	require.NoError(t, result.Err)
	require.NoError(t, db.Table("file_derivative").Where("file_id = ?", file).Count(&count).Error)
	require.Equal(t, int64(1), count)
}
