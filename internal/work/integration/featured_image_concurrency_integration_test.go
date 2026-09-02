//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestDeleteWorkFeaturedImageReloadsCurrentImageAfterRootLockIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Work Featured Image Concurrency Admin")
	ctx, cancel := context.WithTimeout(workIntegrationAdminCtx(adminID), 10*time.Second)
	defer cancel()
	service := newWorkIntegrationService(t, db, adminID, &recordingWorkFileDeleter{})
	present := true

	created, err := service.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Work Featured Image Concurrency " + integrationTestUUID(),
		Type:  managev1.WorkType_WORK_TYPE_ARTICLE, Year: 2026, Month: 8, IsPresent: &present,
		Document: emptyWorkIntegrationDocument("en"),
	}))
	require.NoError(t, err)
	fileID := integrationTestUUID()
	seedIntegrationFile(t, db, fileID, "concurrent-featured", "image/webp", nil)

	blocker := db.WithContext(ctx).Begin()
	require.NoError(t, blocker.Error)
	lockHeld := true
	t.Cleanup(func() {
		if lockHeld {
			_ = blocker.Rollback().Error
		}
	})
	require.NoError(t, blocker.Exec("SELECT id FROM work WHERE id = ? FOR UPDATE", created.Msg.Id).Error)

	deleteResult := make(chan struct {
		response *connect.Response[managev1.OgAssetDeleteResponse]
		err      error
	}, 1)
	go func() {
		response, deleteErr := service.DeleteWorkFeaturedImage(
			ctx,
			connect.NewRequest(&managev1.DeleteWorkFeaturedImageRequest{WorkId: created.Msg.Id}),
		)
		deleteResult <- struct {
			response *connect.Response[managev1.OgAssetDeleteResponse]
			err      error
		}{response: response, err: deleteErr}
	}()

	select {
	case result := <-deleteResult:
		require.Failf(t, "featured image deletion bypassed the Work root lock", "returned early: %v", result.err)
	case <-time.After(150 * time.Millisecond):
	}

	// This represents a concurrent Set transaction committing before Delete
	// acquires the same Work root lock.
	require.NoError(t, blocker.Model(&model.Work{}).
		Where("id = ?", created.Msg.Id).
		Update("featured_image_file_id", fileID).Error)
	require.NoError(t, blocker.Commit().Error)
	lockHeld = false

	var deleted struct {
		response *connect.Response[managev1.OgAssetDeleteResponse]
		err      error
	}
	select {
	case deleted = <-deleteResult:
	case <-ctx.Done():
		require.FailNow(t, "featured image deletion did not resume after the Work root lock was released")
	}
	require.NoError(t, deleted.err)
	require.NotNil(t, deleted.response)
	require.NotNil(t, deleted.response.Msg.OgGenerationRunId)

	var work model.Work
	require.NoError(t, db.Select("featured_image_file_id").First(&work, "id = ?", created.Msg.Id).Error)
	require.Nil(t, work.FeaturedImageFileID)

	var generation model.OgGeneration
	require.NoError(t, db.Where("run_id = ?", deleted.response.Msg.GetOgGenerationRunId()).Take(&generation).Error)
	var snapshot struct {
		FeaturedImage *json.RawMessage `json:"featured_image,omitempty"`
	}
	require.NoError(t, json.Unmarshal(generation.EntitySnapshot, &snapshot))
	require.Nil(t, snapshot.FeaturedImage)
}
