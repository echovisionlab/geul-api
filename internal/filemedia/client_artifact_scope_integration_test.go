//go:build integration

package filemedia

import (
	"errors"
	"testing"

	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestArtifactRepairReadsUncommittedFileReceiptIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
	file := model.File{ID: uuid.NewString(), FileName: "source", Extension: "webp", MimeType: "image/webp", FileSize: 64}
	require.NoError(t, db.Create(&file).Error)
	service := &Service{units: filemediapostgres.NewUnits(db, filemediapostgres.Ports{})}
	row := model.UploadSession{FileID: file.ID, Status: model.UploadSessionStatusFinalizing}
	allowed, err := service.clientMediaArtifactWritable(t.Context(), row)
	require.NoError(t, err)
	require.True(t, allowed)
	abort := errors.New("abort artifact owner")
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, tx.Model(&model.File{}).Where("id = ?", file.ID).Update("client_media_bundle_id", uuid.NewString()).Error)
		scoped := *service
		scoped.uploadReads = boundFileStoreReader{filemediapostgres.NewStore(tx, filemediapostgres.Ports{})}
		allowed, err := scoped.clientMediaArtifactWritable(t.Context(), row)
		require.NoError(t, err)
		require.False(t, allowed)
		return abort
	}), abort)
	allowed, err = service.clientMediaArtifactWritable(t.Context(), row)
	require.NoError(t, err)
	require.True(t, allowed)
}
