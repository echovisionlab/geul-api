//go:build integration

package worker

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/adapters/filemediaqueue"

	mediaassetpostgres "github.com/echovisionlab/geul-api/internal/mediaasset/postgres"

	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	filemediaapplication "github.com/echovisionlab/geul-api/internal/filemedia/application"
	filemediaapplicationpostgres "github.com/echovisionlab/geul-api/internal/filemedia/application/postgres"
	"github.com/echovisionlab/geul-api/internal/model"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type signalingFileAuthorizationDeletion struct {
	called chan struct{}
}

func (deletion signalingFileAuthorizationDeletion) DeleteAndVerify(
	context.Context,
	policyv1.Resource,
) (func(context.Context) error, time.Time, error) {
	deletion.called <- struct{}{}
	return func(context.Context) error { return nil }, time.Now(), nil
}

func TestFileDeleteConsumerIntentRejectsAttachWithoutStorageLockStarvationIntegration(t *testing.T) {
	t.Parallel()
	pg := newWorkerIntegrationPostgres(t)
	db := pg.DB
	now := time.Now().UTC()
	file := model.File{
		ID: uuid.NewString(), FileName: "serialized", MimeType: "audio/wav",
		FileSize: 1024, Extension: "wav", SHA256: make([]byte, 32),
		DeleteRequestedAt: &now, CreatedAt: now,
	}
	require.NoError(t, db.Create(&file).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Exec(`DELETE FROM file WHERE id = ?`, file.ID).Error)
	})
	original, err := filemediaqueue.CanonicalObjectTarget(filemedia.File(file))
	require.NoError(t, err)
	deleteStarted := make(chan struct{}, 1)
	releaseDelete := make(chan struct{})
	var releaseDeleteOnce sync.Once
	releaseBlockedDelete := func() {
		releaseDeleteOnce.Do(func() { close(releaseDelete) })
	}
	t.Cleanup(releaseBlockedDelete)
	s3Client := newFileDeleteTestS3Client(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deleteStarted <- struct{}{}
		<-releaseDelete
		w.WriteHeader(http.StatusNoContent)
	}))
	files := newWorkerFileMediaRuntime(db, s3Client, newInMemoryFileAuthorizationDeletion())
	event := &managev1.FileDeleteEvent{FileId: file.ID, Original: original}

	deleteDone := make(chan error, 1)
	go func() {
		deleteDone <- files.HandleFileDelete(context.Background(), event)
	}()
	select {
	case <-deleteStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("file delete did not reach object storage")
	}

	attachDone := make(chan error, 1)
	go func() {
		attachDone <- db.WithContext(context.Background()).Transaction(func(tx *gorm.DB) error {
			return mediaassetpostgres.NewStore(tx).LockAttachableFiles(context.Background(), []string{file.ID})
		})
	}()
	select {
	case err := <-attachDone:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("attach guard was starved by object storage deletion")
	}
	releaseBlockedDelete()
	select {
	case err := <-deleteDone:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("file delete starved after storage completion")
	}
}

func TestFileDeleteFinalizerUsesGlobalAuthorizationMutationFenceIntegration(t *testing.T) {
	t.Parallel()
	pg := newWorkerIntegrationPostgres(t)
	db := pg.DB
	now := time.Now().UTC()
	file := model.File{
		ID: uuid.NewString(), FileName: "authorization-fence", MimeType: "audio/wav",
		FileSize: 1024, Extension: "wav", SHA256: make([]byte, 32),
		DeleteRequestedAt: &now, CreatedAt: now,
	}
	require.NoError(t, db.Create(&file).Error)
	t.Cleanup(func() { _ = db.Exec(`DELETE FROM file WHERE id = ?`, file.ID).Error })
	original, err := filemediaqueue.CanonicalObjectTarget(filemedia.File(file))
	require.NoError(t, err)
	event := &managev1.FileDeleteEvent{FileId: file.ID, Original: original}

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	require.NoError(t, authzmutation.LockTransaction(blocker))
	blockerClosed := false
	t.Cleanup(func() {
		if !blockerClosed {
			_ = blocker.Rollback().Error
		}
	})

	authorizationCalled := make(chan struct{}, 1)
	deletion := filemediaapplication.NewDeletion(filemediaapplicationpostgres.NewDeletionUnits(db), signalingFileAuthorizationDeletion{
		called: authorizationCalled,
	})
	finalized := make(chan error, 1)
	go func() {
		finalized <- deletion.Finalize(context.Background(), filemediaapplication.DeletionTargetFromProto(event))
	}()

	pgtest.RequireLockWaiters(t, db, 1)
	select {
	case <-authorizationCalled:
		t.Fatal("File authorization deletion bypassed the global mutation fence")
	default:
	}
	require.NoError(t, blocker.Rollback().Error)
	blockerClosed = true

	select {
	case <-authorizationCalled:
	case <-time.After(3 * time.Second):
		t.Fatal("File authorization deletion did not resume after releasing the global fence")
	}
	select {
	case err := <-finalized:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("File finalization did not complete after releasing the global fence")
	}
}
