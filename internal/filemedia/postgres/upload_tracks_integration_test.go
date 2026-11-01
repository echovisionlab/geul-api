//go:build integration

package filemediapostgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type uploadTrackAuthority func(context.Context, *gorm.DB, string) error

func (f uploadTrackAuthority) LockExistsWithDB(ctx context.Context, tx *gorm.DB, id string) error {
	return f(ctx, tx, id)
}

func TestUploadTrackAuthorityUsesFileTransactionIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	track, upload := uuid.NewString(), uuid.NewString()
	row := model.UploadSession{UploadID: upload, FileID: uuid.NewString(), UploadType: "UPLOAD_TYPE_GENERAL_FILE", FileName: "file", FileSize: 6, RequestedMime: "image/webp", TotalParts: 1, ChunkSize: 6, Status: model.UploadSessionStatusInitiated}
	require.NoError(t, db.Omit("EntityID").Create(&row).Error)
	calls := 0
	port := uploadTrackAuthority(func(ctx context.Context, tx *gorm.DB, id string) error {
		calls++
		require.Equal(t, track, id)
		return tx.WithContext(ctx).Model(&model.UploadSession{}).Where("upload_id = ?", upload).Update("status", model.UploadSessionStatusFinalizing).Error
	})
	units := filemediapostgres.NewUnits(db, filemediapostgres.Ports{UploadTracks: port})
	abort := errors.New("rollback File owner")
	require.ErrorIs(t, units.Transaction(t.Context(), false, func(unit mutation.Tx[domain.Store]) error {
		require.NoError(t, unit.Store().RequireUploadTrack(t.Context(), track))
		got, err := unit.Store().UploadSession(t.Context(), upload, row.FileID, false)
		require.NoError(t, err)
		require.Equal(t, domain.UploadSessionStatusFinalizing, got.Status)
		return abort
	}), abort)
	require.Equal(t, 1, calls)
	var stored model.UploadSession
	require.NoError(t, db.Where("upload_id = ?", upload).Take(&stored).Error)
	require.Equal(t, model.UploadSessionStatusInitiated, stored.Status)
}
