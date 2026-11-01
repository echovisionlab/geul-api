//go:build integration

package applicationpostgres_test

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/structured"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	"github.com/echovisionlab/geul-api/internal/transcode"
	"github.com/echovisionlab/geul-api/internal/transcode/application"
	"github.com/echovisionlab/geul-api/internal/transcode/application/applicationtest"
	applicationpostgres "github.com/echovisionlab/geul-api/internal/transcode/application/postgres"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func newDB(t *testing.T) *gorm.DB {
	t.Helper()
	return pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{ApplyAppSchemaSQL: true}).DB
}

func seedFile(t *testing.T, db *gorm.DB, extension, mimeType string) string {
	t.Helper()
	id := uuid.NewString()
	digest := sha256.Sum256([]byte(id))
	require.NoError(t, db.Create(&model.File{
		ID: id, FileName: id, MimeType: mimeType, FileSize: 12345, Extension: extension, SHA256: digest[:],
		CreatedAt: time.Now().UTC(),
	}).Error)
	return id
}

func seedGeneration(t *testing.T, db *gorm.DB, fileID string) *commonv1.MediaGenerationWriteTarget {
	t.Helper()
	id := uuid.NewString()
	prefix, err := mediaauth.MediaHLSObjectPrefix(fileID, id)
	require.NoError(t, err)
	now := time.Now().UTC()
	require.NoError(t, db.Create(&model.MediaGeneration{
		ID: id, FileID: fileID, Kind: "hls", ObjectPrefix: prefix, ManifestName: "master.m3u8",
		Status: model.MediaGenerationStatusAllocated, CreatedAt: now, UpdatedAt: now,
	}).Error)
	return &commonv1.MediaGenerationWriteTarget{GenerationId: id, FileId: fileID, ObjectPrefix: prefix}
}

func TestPostgresStoreKeepsTheStoreContractIntegration(t *testing.T) {
	t.Parallel()
	applicationtest.RunStoreContract(t, func(t *testing.T) applicationtest.Harness {
		db := newDB(t)
		return applicationtest.Harness{
			Harness:  mutationtest.PostgresHarness(db, applicationpostgres.NewUnits(db)),
			SeedFile: func(t *testing.T, extension, mimeType string) string { return seedFile(t, db, extension, mimeType) },
			RequestFileDeletion: func(t *testing.T, fileID string) {
				require.NoError(t, db.Model(&model.File{}).Where("id = ?", fileID).
					Updates(structured.Fields{"delete_requested_at": time.Now().UTC()}).Error)
			},
			SeedAllocatedAsset: func(t *testing.T, kind, fileID, extension, mimeType string) *commonv1.AssetWriteTarget {
				_, target, err := mediaasset.AllocationResultProto(mediaasset.NewLifecycle(mediaassetpostgres.NewUnits(db), "").AllocatePublicAsset(t.Context(), mediaasset.Allocation{
					SourceFileID: &fileID, Kind: kind, Extension: extension, MimeType: mimeType,
					Disposition: mediaasset.DispositionInline,
				}))
				require.NoError(t, err)
				return target
			},
			SeedAllocatedGeneration: func(t *testing.T, fileID string) *commonv1.MediaGenerationWriteTarget {
				return seedGeneration(t, db, fileID)
			},
			SeedJob: func(t *testing.T, job transcode.Job) {
				require.NoError(t, db.Create(&model.TranscodeJob{
					EventID: job.EventID, QueueName: job.QueueName, EntityType: job.EntityType, EntityID: job.EntityID,
					FileID: job.FileID, Payload: job.Payload, Status: job.Status, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
				}).Error)
			},
			SeedWaveformJob: func(t *testing.T, job transcode.WaveformJob) {
				require.NoError(t, db.Create(&model.WaveformJob{
					EventID: job.EventID, EntityType: job.EntityType, EntityID: job.EntityID, FileID: job.FileID,
					Status: job.Status, CancelRequested: job.CancelRequested, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
				}).Error)
			},
		}
	})
}

// Completing a File's new HLS generation retires the one its derivative
// pointed at, once and in the same transaction as the switch.
func TestCompletingAGenerationSwitchesTheHLSDerivativeAndRetiresThePreviousIntegration(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	units := applicationpostgres.NewUnits(db)
	harness := mutationtest.PostgresHarness(db, units)
	fileID := seedFile(t, db, "mp4", "video/mp4")
	previous, current := seedGeneration(t, db, fileID), seedGeneration(t, db, fileID)
	const hls = "FILE_DERIVATIVE_TYPE_HLS"
	complete := func(target *commonv1.MediaGenerationWriteTarget, fill byte) error {
		return mutationtest.Transact(t, harness, func(ctx context.Context, store application.Store) error {
			generation, err := store.CompleteMediaGeneration(ctx, applicationtest.WrittenGeneration(target, fill))
			if err != nil {
				return err
			}
			return store.UpsertDerivative(ctx, application.Derivative{
				ID: uuid.NewString(), FileID: fileID, Type: hls, MediaGenerationID: &generation.ID,
			})
		})
	}
	generationOf := func(id string) model.MediaGeneration {
		var generation model.MediaGeneration
		require.NoError(t, db.Where("id = ?", id).Take(&generation).Error)
		return generation
	}

	require.NoError(t, complete(previous, 0x88))
	require.NoError(t, complete(current, 0x99))
	var switched string
	require.NoError(t, db.Raw(`SELECT media_generation_id::text FROM file_derivative WHERE file_id = ? AND type = ?`, fileID, hls).
		Scan(&switched).Error)
	require.Equal(t, current.GetGenerationId(), switched)
	retired := generationOf(previous.GetGenerationId())
	require.Equal(t, model.MediaGenerationStatusRetired, retired.Status)
	require.False(t, retired.DeleteAfter.Before(retired.RetiredAt.Add(7*time.Hour)))

	require.NoError(t, complete(current, 0x99))
	require.Equal(t, *retired.DeleteAfter, *generationOf(previous.GetGenerationId()).DeleteAfter)
	require.Equal(t, model.MediaGenerationStatusReady, generationOf(current.GetGenerationId()).Status)
}
