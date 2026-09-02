//go:build integration

package application

import (
	"bytes"
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLoadMediaProcessingSnapshotIncludesOnlyReadyCanonicalOutputs(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	ctx := context.Background()
	fileID := uuid.NewString()
	seedWorkerTranscodeFile(t, db, fileID, "ogg", "audio/ogg")

	lifecycle := mediaasset.NewLifecycle(db, "")
	_, waveformTarget, err := lifecycle.AllocatePublicAsset(ctx, mediaasset.Allocation{
		SourceFileID: &fileID,
		Kind:         "waveform",
		Extension:    "json",
		MimeType:     "application/json",
		Disposition:  commonv1.AssetDisposition_ASSET_DISPOSITION_INLINE,
	})
	require.NoError(t, err)
	_, hlsTarget := seedWorkerAllocatedMediaGeneration(t, db, fileID)

	require.NoError(t, db.Exec(`
		INSERT INTO file_derivative (file_id, type, asset_id)
		VALUES (?, ?, ?)
	`, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String(), waveformTarget.GetAssetId()).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO file_derivative (file_id, type, media_generation_id)
		VALUES (?, ?, ?)
	`, fileID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String(), hlsTarget.GetGenerationId()).Error)

	handlers := &Handlers{db: db}
	snapshot, err := handlers.loadMediaProcessingSnapshot(ctx, fileID)
	require.NoError(t, err)
	require.Empty(t, snapshot.Derivatives)

	_, err = lifecycle.CompletePublicAsset(ctx, &commonv1.AssetWriteResult{
		AssetId: waveformTarget.GetAssetId(), FileSize: 1024, Sha256: bytes.Repeat([]byte{0x44}, 32),
	})
	require.NoError(t, err)
	_, err = lifecycle.CompleteMediaGeneration(ctx, &commonv1.MediaGenerationWriteResult{
		GenerationId: hlsTarget.GetGenerationId(), ManifestSha256: bytes.Repeat([]byte{0x55}, 32),
		ObjectCount: 2, TotalSize: 4096,
	})
	require.NoError(t, err)

	snapshot, err = handlers.loadMediaProcessingSnapshot(ctx, fileID)
	require.NoError(t, err)
	require.Equal(t, waveformTarget.GetAssetId(), *snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM.String()].AssetID)
	require.Equal(t, hlsTarget.GetGenerationId(), *snapshot.Derivatives[managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String()].MediaGenerationID)
}
