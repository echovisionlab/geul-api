//go:build integration

package application

import (
	"context"
	"testing"
	"time"

	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// newWorkerIntegrationPostgres returns a database owned by the calling test.
func newWorkerIntegrationPostgres(t *testing.T) *testutil.AppPostgres {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	})
}

func newWorkerIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return newWorkerIntegrationPostgres(t).DB
}

func seedWorkerAllocatedMediaGeneration(
	t *testing.T,
	db *gorm.DB,
	fileID string,
) (*model.MediaGeneration, *commonv1.MediaGenerationWriteTarget) {
	t.Helper()
	generationID := uuid.NewString()
	objectPrefix, err := mediaauth.MediaHLSObjectPrefix(fileID, generationID)
	require.NoError(t, err)
	now := time.Now().UTC()
	generation := &model.MediaGeneration{
		ID:           generationID,
		FileID:       fileID,
		Kind:         "hls",
		ObjectPrefix: objectPrefix,
		ManifestName: "master.m3u8",
		Status:       model.MediaGenerationStatusAllocated,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	require.NoError(t, db.Create(generation).Error)
	return generation, &commonv1.MediaGenerationWriteTarget{
		GenerationId: generation.ID,
		FileId:       generation.FileID,
		ObjectPrefix: generation.ObjectPrefix,
	}
}

type recordingWorkerPublisher struct {
	fileDeleteEvents               []*managev1.FileDeleteEvent
	mediaProcessingLifecycleEvents []*managev1.MediaProcessingLifecycleEvent
	waveformGenerateEvents         []*managev1.WaveformGenerateEvent
}

func (p *recordingWorkerPublisher) PublishMediaProcessingLifecycle(
	_ context.Context,
	event *managev1.MediaProcessingLifecycleEvent,
) error {
	p.mediaProcessingLifecycleEvents = append(p.mediaProcessingLifecycleEvents, event)
	return nil
}

func (p *recordingWorkerPublisher) PublishWaveformGenerate(
	_ context.Context,
	event *managev1.WaveformGenerateEvent,
) error {
	p.waveformGenerateEvents = append(p.waveformGenerateEvents, event)
	return nil
}
