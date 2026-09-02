//go:build integration

package filemedia

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFileManagerGetDoesNotIssueAfterDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	var once sync.Once
	service.testBeforeFileManagerDeliveryFence = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		once.Do(func() {
			close(beforeFence)
			<-releaseFence
		})
	}

	type result struct {
		response *connect.Response[managev1.GetFileResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?", fileID).Error)
	close(releaseFence)

	got := <-done
	require.Nil(t, got.response)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(got.err))
}

func TestFileManagerListRetriesWithoutFileDeletedBeforeSigningIntegration(t *testing.T) {
	db, ctx, service, folderID, fileID := newFileManagerDeliveryFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	var once sync.Once
	service.testBeforeFileManagerDeliveryFence = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		once.Do(func() {
			close(beforeFence)
			<-releaseFence
		})
	}

	type result struct {
		response *connect.Response[managev1.ListFileManagerItemsResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{FolderId: &folderID}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?", fileID).Error)
	close(releaseFence)

	got := <-done
	require.NoError(t, got.err)
	require.Empty(t, got.response.Msg.GetItems())
	require.Zero(t, got.response.Msg.GetTotal())
	require.Empty(t, got.response.Msg.GetNextPageToken())
}

func TestFileManagerGetSigningCompletesBeforeDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	signed := make(chan struct{})
	releaseSignedFence := make(chan struct{})
	service.testAfterFileManagerDeliverySigned = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(signed)
		<-releaseSignedFence
	}

	type result struct {
		response *connect.Response[managev1.GetFileResponse]
		err      error
	}
	requestDone := make(chan result, 1)
	go func() {
		response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
		requestDone <- result{response: response, err: err}
	}()
	<-signed

	backendPID, deleteAttempted, deleteDone := startFileManagerDeletionUpdate(t, db, fileID)
	<-deleteAttempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	select {
	case err := <-deleteDone:
		t.Fatalf("deletion completed before the signed-delivery fence committed: %v", err)
	default:
	}
	close(releaseSignedFence)

	got := <-requestDone
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetFile().GetDelivery().GetInline().GetUrl())
	require.NotEmpty(t, got.response.Msg.GetFile().GetDelivery().GetDownload().GetUrl())
	require.NoError(t, <-deleteDone)
	requireFileManagerDeletionPending(t, db, fileID)
}

func TestFileManagerListSigningCompletesBeforeDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, folderID, fileID := newFileManagerDeliveryFenceFixture(t)
	signed := make(chan struct{})
	releaseSignedFence := make(chan struct{})
	service.testAfterFileManagerDeliverySigned = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(signed)
		<-releaseSignedFence
	}

	type result struct {
		response *connect.Response[managev1.ListFileManagerItemsResponse]
		err      error
	}
	requestDone := make(chan result, 1)
	go func() {
		response, err := service.ListFileManagerItems(ctx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{FolderId: &folderID}))
		requestDone <- result{response: response, err: err}
	}()
	<-signed

	backendPID, deleteAttempted, deleteDone := startFileManagerDeletionUpdate(t, db, fileID)
	<-deleteAttempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	select {
	case err := <-deleteDone:
		t.Fatalf("deletion completed before the catalog signed-delivery fence committed: %v", err)
	default:
	}
	close(releaseSignedFence)

	got := <-requestDone
	require.NoError(t, got.err)
	require.Len(t, got.response.Msg.GetItems(), 1)
	delivery := got.response.Msg.GetItems()[0].GetFile().GetDelivery()
	require.NotEmpty(t, delivery.GetInline().GetUrl())
	require.NotEmpty(t, delivery.GetDownload().GetUrl())
	require.NoError(t, <-deleteDone)
	requireFileManagerDeletionPending(t, db, fileID)
}

func TestFileManagerGetDoesNotIssueAfterPrincipalRevocationCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeFileManagerDeliveryFence = func([]string) {
		close(beforeFence)
		<-releaseFence
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
		done <- err
	}()
	<-beforeFence
	released := false
	defer func() {
		if !released {
			close(releaseFence)
		}
	}()
	require.NoError(t, db.Table("kratos.identities").Where("id = ?", auth.GetUser(ctx).IdentityID.String()).Update("state", auth.KratosStateInactive).Error)
	close(releaseFence)
	released = true
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-done))
}

func TestFileManagerSigningCompletesBeforePrincipalRevocationCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterFileManagerDeliverySigned = func([]string) {
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *connect.Response[managev1.GetFileResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-signed
	released := false
	defer func() {
		if !released {
			close(releaseSigned)
		}
	}()
	backendPID, attempted, revoked := startIdentityRevocationUpdate(t, db, auth.GetUser(ctx).IdentityID.String())
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	released = true
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetFile().GetDelivery().GetDownload().GetUrl())
	require.NoError(t, <-revoked)
}

func TestFileManagerSigningCompletesBeforeRenameAndDeletePrincipalFenceIntegration(t *testing.T) {
	for _, mutation := range []string{"rename", "delete"} {
		t.Run(mutation, func(t *testing.T) {
			db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
			signed := make(chan struct{})
			releaseSigned := make(chan struct{})
			var signedOnce sync.Once
			service.testAfterFileManagerDeliverySigned = func([]string) {
				signedOnce.Do(func() {
					close(signed)
					<-releaseSigned
				})
			}
			type getResult struct {
				response *connect.Response[managev1.GetFileResponse]
				err      error
			}
			getDone := make(chan getResult, 1)
			go func() {
				response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
				getDone <- getResult{response: response, err: err}
			}()
			<-signed
			backendPID := make(chan int, 1)
			attempted := make(chan struct{})
			service.testBeforeFileMutationPrincipal = func(tx *gorm.DB, fileIDs []string) {
				require.Equal(t, []string{fileID}, fileIDs)
				var pid int
				require.NoError(t, tx.Raw("SELECT pg_backend_pid()").Scan(&pid).Error)
				backendPID <- pid
				close(attempted)
			}
			mutationDone := make(chan error, 1)
			go func() {
				switch mutation {
				case "rename":
					_, err := service.RenameFile(ctx, connect.NewRequest(&managev1.RenameFileRequest{FileId: fileID, FileName: "renamed"}))
					mutationDone <- err
				case "delete":
					_, err := service.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: []string{fileID}}))
					mutationDone <- err
				}
			}()
			pid := <-backendPID
			<-attempted
			requirePostgresBackendWaitingForLock(t, db, pid)
			close(releaseSigned)
			got := <-getDone
			require.NoError(t, got.err)
			require.NotEmpty(t, got.response.Msg.GetFile().GetDelivery().GetDownload().GetUrl())
			require.NoError(t, <-mutationDone)
		})
	}
}

func TestFileManagerDeleteHoldingPrincipalCompletesWithoutSignerDeadlockIntegration(t *testing.T) {
	_, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	principalHeld := make(chan struct{})
	releaseMutation := make(chan struct{})
	service.testAfterFileMutationPrincipal = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(principalHeld)
		<-releaseMutation
	}
	mutationDone := make(chan error, 1)
	go func() {
		_, err := service.DeleteFiles(ctx, connect.NewRequest(&managev1.DeleteFilesRequest{FileIds: []string{fileID}}))
		mutationDone <- err
	}()
	<-principalHeld
	beforeSignerFence := make(chan struct{})
	var beforeOnce sync.Once
	service.testBeforeFileManagerDeliveryFence = func([]string) {
		beforeOnce.Do(func() { close(beforeSignerFence) })
	}
	signerDone := make(chan error, 1)
	go func() {
		_, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
		signerDone <- err
	}()
	<-beforeSignerFence
	close(releaseMutation)
	require.NoError(t, <-mutationDone)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-signerDone))
}

func TestFileManagerAuthorCanDownloadUnattachedFileIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminCtx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	user := auth.GetUser(adminCtx)
	service := &FileService{
		db: db, spiceDB: spiceDB,
		cdnDomain: "https://cdn.example.test", mediaDomain: "https://media.example.test",
		mediaSecret: "file-manager-unattached-secret", downloadTTL: time.Hour,
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}
	folder, err := service.CreateFileFolder(adminCtx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Unattached"}))
	require.NoError(t, err)
	fileID := seedFileManagerFile(t, db, folder.Msg.Folder.GetId(), user.MemberID.String(), "standalone", "pdf", "application/pdf")
	subject, err := auth.NewAccountIdentitySubject(user.IdentityID)
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Author())
	require.NoError(t, err)

	response, err := service.GetFile(adminCtx, connect.NewRequest(&managev1.GetFileRequest{FileId: fileID}))
	require.NoError(t, err)
	require.Zero(t, response.Msg.GetFile().GetUsageCount())
	require.Empty(t, response.Msg.GetDomainUsageSummary())
	require.NotEmpty(t, response.Msg.GetFile().GetDelivery().GetInline().GetUrl())
	require.NotEmpty(t, response.Msg.GetFile().GetDelivery().GetDownload().GetUrl())
	require.Equal(t, "standalone.pdf", response.Msg.GetFile().GetDelivery().GetDownload().GetFileName())

	listed, err := service.ListFileManagerItems(adminCtx, connect.NewRequest(&managev1.ListFileManagerItemsRequest{FolderId: &folder.Msg.Folder.Id}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.GetItems(), 1)
	require.Equal(t, fileID, listed.Msg.GetItems()[0].GetFile().GetId())
	require.NotEmpty(t, listed.Msg.GetItems()[0].GetFile().GetDelivery().GetDownload().GetUrl())
}

func TestFileManagerGetDropsOptimizedOutputDeliveryDeletedBeforeSigningIntegration(t *testing.T) {
	db, ctx, service, folderID, sourceFileID := newFileManagerDeliveryFenceFixture(t)
	outputFileID := seedFileManagerFile(
		t,
		db,
		folderID,
		auth.GetUser(ctx).MemberID.String(),
		"optimized",
		"glb",
		"model/gltf-binary",
	)
	candidateID := uuid.NewString()
	require.NoError(t, db.Create(&model.MeshOptimizationCandidate{
		ID: candidateID, SourceFileID: sourceFileID, OutputObjectID: outputFileID, OutputFileID: &outputFileID,
		TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco,
		PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1,
		CacheKey:        sourceFileID + ":50:DRACO:file-manager-fence",
		Status:          model.MeshOptimizationCandidateStatusReady,
		CreatedAt:       time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error)
	expectedIDs := []string{sourceFileID, outputFileID}
	if expectedIDs[1] < expectedIDs[0] {
		expectedIDs[0], expectedIDs[1] = expectedIDs[1], expectedIDs[0]
	}
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeFileManagerDeliveryFence = func(fileIDs []string) {
		require.Equal(t, expectedIDs, fileIDs)
		close(beforeFence)
		<-releaseFence
	}

	type result struct {
		response *connect.Response[managev1.GetFileResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: sourceFileID}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?", outputFileID).Error)
	close(releaseFence)

	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetFile().GetDelivery().GetDownload().GetUrl())
	require.Len(t, got.response.Msg.GetGeneratedOutputs(), 1)
	require.Equal(t, candidateID, got.response.Msg.GetGeneratedOutputs()[0].GetId())
	require.Nil(t, got.response.Msg.GetGeneratedOutputs()[0].GetDelivery())
}

func TestFileManagerGetDropsOptimizedOutputWhenCandidateChangesBeforeSigningIntegration(t *testing.T) {
	for _, mutation := range []string{"deleted", "status", "output"} {
		t.Run(mutation, func(t *testing.T) {
			db, ctx, service, folderID, sourceFileID := newFileManagerDeliveryFenceFixture(t)
			outputFileID := seedFileManagerFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "optimized-"+mutation, "glb", "model/gltf-binary")
			candidateID := uuid.NewString()
			require.NoError(t, db.Create(&model.MeshOptimizationCandidate{
				ID: candidateID, SourceFileID: sourceFileID, OutputObjectID: outputFileID, OutputFileID: &outputFileID,
				TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco,
				PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1,
				CacheKey:        sourceFileID + ":50:DRACO:file-manager-candidate-" + mutation,
				Status:          model.MeshOptimizationCandidateStatusReady, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}).Error)
			beforeFence := make(chan struct{})
			releaseFence := make(chan struct{})
			service.testBeforeFileManagerDeliveryFence = func([]string) {
				close(beforeFence)
				<-releaseFence
			}
			type result struct {
				response *connect.Response[managev1.GetFileResponse]
				err      error
			}
			done := make(chan result, 1)
			go func() {
				response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: sourceFileID}))
				done <- result{response: response, err: err}
			}()
			<-beforeFence
			switch mutation {
			case "deleted":
				require.NoError(t, db.Exec("DELETE FROM mesh_optimization_candidate WHERE id = ?", candidateID).Error)
			case "status":
				require.NoError(t, db.Table("mesh_optimization_candidate").Where("id = ?", candidateID).Update("status", model.MeshOptimizationCandidateStatusFailed).Error)
			case "output":
				replacementID := seedFileManagerFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "replacement", "glb", "model/gltf-binary")
				require.NoError(t, db.Table("mesh_optimization_candidate").Where("id = ?", candidateID).Update("output_file_id", replacementID).Error)
			}
			close(releaseFence)
			got := <-done
			require.NoError(t, got.err)
			require.Len(t, got.response.Msg.GetGeneratedOutputs(), 1)
			require.Nil(t, got.response.Msg.GetGeneratedOutputs()[0].GetDelivery())
		})
	}
}

func TestFileManagerOptimizedOutputSigningCompletesBeforeCandidateMutationIntegration(t *testing.T) {
	db, ctx, service, folderID, sourceFileID := newFileManagerDeliveryFenceFixture(t)
	outputFileID := seedFileManagerFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "optimized-locked", "glb", "model/gltf-binary")
	candidateID := uuid.NewString()
	require.NoError(t, db.Create(&model.MeshOptimizationCandidate{
		ID: candidateID, SourceFileID: sourceFileID, OutputObjectID: outputFileID, OutputFileID: &outputFileID,
		TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco,
		PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1,
		CacheKey:        sourceFileID + ":50:DRACO:file-manager-candidate-lock",
		Status:          model.MeshOptimizationCandidateStatusReady, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error)
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterFileManagerDeliverySigned = func([]string) {
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *connect.Response[managev1.GetFileResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: sourceFileID}))
		done <- result{response: response, err: err}
	}()
	<-signed
	backendPID, attempted, mutated := startMeshCandidateStatusUpdate(t, db, candidateID)
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	got := <-done
	require.NoError(t, got.err)
	require.Len(t, got.response.Msg.GetGeneratedOutputs(), 1)
	require.NotEmpty(t, got.response.Msg.GetGeneratedOutputs()[0].GetDelivery().GetInline().GetUrl())
	require.NoError(t, <-mutated)
}

func TestFileManagerOptimizedOutputSigningCompletesBeforeMeshCompletionIntegration(t *testing.T) {
	db, ctx, service, folderID, sourceFileID := newFileManagerDeliveryFenceFixture(t)
	outputFileID := seedFileManagerFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "optimized-completion", "glb", "model/gltf-binary")
	candidate := model.MeshOptimizationCandidate{
		ID: uuid.NewString(), SourceFileID: sourceFileID, OutputObjectID: outputFileID, OutputFileID: &outputFileID,
		TargetRatioPercent: 50, Method: model.MeshOptimizationMethodDraco,
		PipelineVersion: model.MeshOptimizationPipelineVersionDracoWebpV1,
		CacheKey:        sourceFileID + ":50:DRACO:file-manager-completion-lock",
		Status:          model.MeshOptimizationCandidateStatusReady, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&candidate).Error)
	var output model.File
	require.NoError(t, db.Select("id", "file_size", "sha256").Where("id = ?", outputFileID).Take(&output).Error)
	optimizedSize := output.FileSize
	completion := meshOptimizationCompletion{
		event:        &managev1.MeshOptimizationCompleteEvent{},
		output:       &managev1.MeshOptimizationOutput{OptimizedSizeBytes: &optimizedSize},
		outputFileID: outputFileID, fileSize: output.FileSize, sha256: append([]byte(nil), output.SHA256...),
		completedAt: time.Now().UTC(), expiresAt: time.Now().UTC().Add(meshOptimizationCandidateTTL),
	}
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterFileManagerDeliverySigned = func([]string) {
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *connect.Response[managev1.GetFileResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: sourceFileID}))
		done <- result{response: response, err: err}
	}()
	<-signed
	backendPID, attempted, completed := startMeshCompletion(t, db, candidate, completion)
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetGeneratedOutputs()[0].GetDelivery().GetInline().GetUrl())
	require.NoError(t, <-completed)
}

func TestManageGetMediaDeliveryDoesNotIssueAfterDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(beforeFence)
		<-releaseFence
	}

	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?", fileID).Error)
	close(releaseFence)

	got := <-done
	require.Nil(t, got.response)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(got.err))
}

func TestManageBulkMediaDeliveriesExcludesFileDeletedBeforeSigningIntegration(t *testing.T) {
	db, ctx, service, folderID, fileID := newFileManagerDeliveryFenceFixture(t)
	secondFileID := seedManagedFileManagerFenceFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "bulk-current")
	expectedIDs := []string{fileID, secondFileID}
	if expectedIDs[1] < expectedIDs[0] {
		expectedIDs[0], expectedIDs[1] = expectedIDs[1], expectedIDs[0]
	}
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func(fileIDs []string) {
		require.Equal(t, expectedIDs, fileIDs)
		close(beforeFence)
		<-releaseFence
	}

	type result struct {
		response *connect.Response[managev1.GetBulkMediaDeliveriesResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetBulkMediaDeliveries(ctx, connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{
			FileIds: []string{fileID, secondFileID},
		}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?", fileID).Error)
	close(releaseFence)

	got := <-done
	require.NoError(t, got.err)
	require.NotContains(t, got.response.Msg.GetFiles(), fileID)
	require.Contains(t, got.response.Msg.GetFiles(), secondFileID)
	require.NotEmpty(t, got.response.Msg.GetFiles()[secondFileID].GetDelivery().GetDownload().GetUrl())
}

func TestManageGetMediaDeliverySigningCompletesBeforeDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	signed := make(chan struct{})
	releaseSignedFence := make(chan struct{})
	service.testAfterManageDeliverySigned = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(signed)
		<-releaseSignedFence
	}

	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	requestDone := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		requestDone <- result{response: response, err: err}
	}()
	<-signed

	backendPID, deleteAttempted, deleteDone := startFileManagerDeletionUpdate(t, db, fileID)
	<-deleteAttempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSignedFence)

	got := <-requestDone
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetInline().GetUrl())
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetDownload().GetUrl())
	require.NoError(t, <-deleteDone)
	requireFileManagerDeletionPending(t, db, fileID)
}

func TestManageGetMediaDeliveryRetriesOnceAfterFileMetadataChangeIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	var changedOnce sync.Once
	service.testBeforeManageDeliveryFence = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		changedOnce.Do(func() {
			require.NoError(t, db.Table("file").Where("id = ?", fileID).Update("duration_seconds", 7).Error)
		})
	}
	response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
	require.NoError(t, err)
	require.EqualValues(t, 7, response.Msg.GetDelivery().GetDurationSeconds())
	require.NotEmpty(t, response.Msg.GetDelivery().GetInline().GetUrl())
	require.NotEmpty(t, response.Msg.GetDelivery().GetDownload().GetUrl())
}

func TestManageGetMediaDeliveryMetadataRetryIsBoundedIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	changeCount := 0
	service.testBeforeManageDeliveryFence = func([]string) {
		changeCount++
		require.NoError(t, db.Table("file").Where("id = ?", fileID).Update("duration_seconds", changeCount).Error)
	}
	_, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	require.Equal(t, 2, changeCount)
}

func TestManageBulkMediaDeliverySigningCompletesBeforeDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, folderID, fileID := newFileManagerDeliveryFenceFixture(t)
	secondFileID := seedManagedFileManagerFenceFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "bulk-locked")
	expectedIDs := []string{fileID, secondFileID}
	if expectedIDs[1] < expectedIDs[0] {
		expectedIDs[0], expectedIDs[1] = expectedIDs[1], expectedIDs[0]
	}
	signed := make(chan struct{})
	releaseSignedFence := make(chan struct{})
	service.testAfterManageDeliverySigned = func(fileIDs []string) {
		require.Equal(t, expectedIDs, fileIDs)
		close(signed)
		<-releaseSignedFence
	}

	type result struct {
		response *connect.Response[managev1.GetBulkMediaDeliveriesResponse]
		err      error
	}
	requestDone := make(chan result, 1)
	go func() {
		response, err := service.GetBulkMediaDeliveries(ctx, connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{
			FileIds: []string{secondFileID, fileID},
		}))
		requestDone <- result{response: response, err: err}
	}()
	<-signed

	backendPID, deleteAttempted, deleteDone := startFileManagerDeletionUpdate(t, db, fileID)
	<-deleteAttempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSignedFence)

	got := <-requestDone
	require.NoError(t, got.err)
	require.Len(t, got.response.Msg.GetFiles(), 2)
	for _, expectedFileID := range []string{fileID, secondFileID} {
		require.NotEmpty(t, got.response.Msg.GetFiles()[expectedFileID].GetDelivery().GetDownload().GetUrl())
	}
	require.NoError(t, <-deleteDone)
	requireFileManagerDeletionPending(t, db, fileID)
}

func TestManagePostCollaboratorGetsInlineWithoutOriginalDownloadIntegration(t *testing.T) {
	db, ctx, service, _, fileID, _ := newManagePostCollaboratorFenceFixture(t)

	response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
	require.NoError(t, err)
	require.NotEmpty(t, response.Msg.GetDelivery().GetInline().GetUrl())
	require.Nil(t, response.Msg.GetDelivery().GetDownload())
	var audience string
	require.NoError(t, db.Table("content_block_attachment").Where("file_id = ?", fileID).Pluck("download_audience", &audience).Error)
	require.Equal(t, "disabled", audience)
}

func TestManagePostCollaboratorDoesNotIssueAfterDetachCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID, blockID := newManagePostCollaboratorFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(beforeFence)
		<-releaseFence
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- err
	}()
	<-beforeFence
	require.NoError(t, db.Exec("DELETE FROM content_block_attachment WHERE block_id = ?", blockID).Error)
	close(releaseFence)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-done))
}

func TestManagePostCollaboratorDoesNotIssueAfterPrincipalRevocationCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID, _ := newManagePostCollaboratorFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	var once sync.Once
	service.testBeforeManageDeliveryFence = func([]string) {
		once.Do(func() {
			close(beforeFence)
			<-releaseFence
		})
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- err
	}()
	<-beforeFence
	released := false
	defer func() {
		if !released {
			close(releaseFence)
		}
	}()
	require.NoError(t, db.Table("kratos.identities").Where("id = ?", auth.GetUser(ctx).IdentityID.String()).Update("state", auth.KratosStateInactive).Error)
	close(releaseFence)
	released = true
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-done))
}

func TestManagePostCollaboratorSigningCompletesBeforePrincipalRevocationCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID, _ := newManagePostCollaboratorFenceFixture(t)
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterManageDeliverySigned = func([]string) {
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-signed
	released := false
	defer func() {
		if !released {
			close(releaseSigned)
		}
	}()
	backendPID, attempted, revoked := startIdentityRevocationUpdate(t, db, auth.GetUser(ctx).IdentityID.String())
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	released = true
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetInline().GetUrl())
	require.Nil(t, got.response.Msg.GetDelivery().GetDownload())
	require.NoError(t, <-revoked)
}

func TestManageStrongDeliveryDoesNotIssueAfterPrincipalRevocationCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	var once sync.Once
	service.testBeforeManageDeliveryFence = func([]string) {
		once.Do(func() {
			close(beforeFence)
			<-releaseFence
		})
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- err
	}()
	<-beforeFence
	released := false
	defer func() {
		if !released {
			close(releaseFence)
		}
	}()
	require.NoError(t, db.Table("kratos.identities").Where("id = ?", auth.GetUser(ctx).IdentityID.String()).Update("state", auth.KratosStateInactive).Error)
	close(releaseFence)
	released = true
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-done))
}

func TestManagePostCollaboratorSigningCompletesBeforeDetachCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID, blockID := newManagePostCollaboratorFenceFixture(t)
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterManageDeliverySigned = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-signed
	backendPID, attempted, detached := startManageUsageDetach(t, db, blockID)
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetInline().GetUrl())
	require.Nil(t, got.response.Msg.GetDelivery().GetDownload())
	require.NoError(t, <-detached)
}

func TestManagePostCollaboratorDoesNotIssueAfterFileDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID, _ := newManagePostCollaboratorFenceFixture(t)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func([]string) {
		close(beforeFence)
		<-releaseFence
	}
	done := make(chan error, 1)
	go func() {
		_, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- err
	}()
	<-beforeFence
	require.NoError(t, db.Exec("UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = ?", fileID).Error)
	close(releaseFence)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(<-done))
}

func TestManagePostCollaboratorSigningCompletesBeforeFileDeletionCommitIntegration(t *testing.T) {
	db, ctx, service, _, fileID, _ := newManagePostCollaboratorFenceFixture(t)
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterManageDeliverySigned = func([]string) {
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-signed
	backendPID, attempted, deleted := startFileManagerDeletionUpdate(t, db, fileID)
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetInline().GetUrl())
	require.Nil(t, got.response.Msg.GetDelivery().GetDownload())
	require.NoError(t, <-deleted)
}

func TestManageBulkUsesPerFileDeliveryCapabilitiesIntegration(t *testing.T) {
	db, ctx, service, _, usageFileID, _ := newManagePostCollaboratorFenceFixture(t)
	user := auth.GetUser(ctx)
	uploaderFileID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, extension, mime_type, file_size, uploaded_by_member_id)
		VALUES (?::uuid, 'owned', 'mp4', 'video/mp4', 4096, ?::uuid)
	`, uploaderFileID, user.MemberID.String()).Error)
	require.NoError(t, db.Exec(
		"INSERT INTO file_ingest_binding (file_id, upload_type) VALUES (?::uuid, ?)",
		uploaderFileID, managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO.String(),
	).Error)

	response, err := service.GetBulkMediaDeliveries(ctx, connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{
		FileIds: []string{uploaderFileID, usageFileID},
	}))
	require.NoError(t, err)
	require.Nil(t, response.Msg.GetFiles()[usageFileID].GetDelivery().GetDownload())
	require.NotEmpty(t, response.Msg.GetFiles()[usageFileID].GetDelivery().GetInline().GetUrl())
	require.NotEmpty(t, response.Msg.GetFiles()[uploaderFileID].GetDelivery().GetDownload().GetUrl())
}

func TestManageBulkExcludesDetachedUsageButKeepsCurrentUsageIntegration(t *testing.T) {
	db, ctx, service, _, firstFileID, firstBlockID := newManagePostCollaboratorFenceFixture(t)
	_, secondFileID, secondBlockID := seedManagePostCollaboratorUsage(t, db, service.spiceDB, auth.GetUser(ctx))
	expected := []string{firstFileID, secondFileID}
	sort.Strings(expected)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func(fileIDs []string) {
		require.Equal(t, expected, fileIDs)
		close(beforeFence)
		<-releaseFence
	}
	type result struct {
		response *connect.Response[managev1.GetBulkMediaDeliveriesResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetBulkMediaDeliveries(ctx, connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{FileIds: expected}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("DELETE FROM content_block_attachment WHERE block_id = ?", firstBlockID).Error)
	close(releaseFence)
	got := <-done
	require.NoError(t, got.err)
	require.NotContains(t, got.response.Msg.GetFiles(), firstFileID)
	require.Contains(t, got.response.Msg.GetFiles(), secondFileID)
	require.Nil(t, got.response.Msg.GetFiles()[secondFileID].GetDelivery().GetDownload())
	require.NotEmpty(t, secondBlockID)
}

func TestManageSameFileRemainsAvailableThroughAnotherAuthorizedUsageIntegration(t *testing.T) {
	db, ctx, service, _, fileID, firstBlockID := newManagePostCollaboratorFenceFixture(t)
	user := auth.GetUser(ctx)
	postID, _, secondBlockID := seedManagePostCollaboratorUsageForFile(t, db, service.spiceDB, user, fileID)
	require.NotEmpty(t, postID)
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func([]string) {
		close(beforeFence)
		<-releaseFence
	}
	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("DELETE FROM content_block_attachment WHERE block_id = ?", firstBlockID).Error)
	close(releaseFence)
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetInline().GetUrl())
	require.Nil(t, got.response.Msg.GetDelivery().GetDownload())
	require.NotEmpty(t, secondBlockID)
}

func TestManageGlobalAuthorDownloadIsIndependentOfUsageDetachIntegration(t *testing.T) {
	db, ctx, service, _, fileID, blockID := newManagePostCollaboratorFenceFixture(t)
	grantIntegrationGlobalRole(t, service.spiceDB, auth.GetUser(ctx).IdentityID.String(), policyv1.Role.Author())
	beforeFence := make(chan struct{})
	releaseFence := make(chan struct{})
	service.testBeforeManageDeliveryFence = func([]string) {
		close(beforeFence)
		<-releaseFence
	}
	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	done := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		done <- result{response: response, err: err}
	}()
	<-beforeFence
	require.NoError(t, db.Exec("DELETE FROM content_block_attachment WHERE block_id = ?", blockID).Error)
	close(releaseFence)
	got := <-done
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetDownload().GetUrl())
}

func TestManagePageAndWorkContentWitnessRejectsCommittedDocumentChangeIntegration(t *testing.T) {
	for _, resourceType := range []string{"page", "work"} {
		t.Run(resourceType, func(t *testing.T) {
			db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
			resourceID, _, _ := seedManageContentOwnerUsage(t, db, resourceType, fileID)
			usages, err := service.currentManageFileDeliveryUsages(ctx, []string{fileID})
			require.NoError(t, err)
			var witness manageFileDeliveryUsageWitness
			for _, candidate := range usages[fileID] {
				if candidate.resourceType == resourceType && candidate.resourceID == resourceID {
					witness = candidate
					break
				}
			}
			require.Equal(t, resourceType, witness.resourceType)
			replacementDocumentID := uuid.NewString()
			require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)", replacementDocumentID, resourceType).Error)
			require.NoError(t, db.Table(resourceType).Where("id = ?", resourceID).Update("content_document_id", replacementDocumentID).Error)
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				current, lockErr := lockManageDeliveryUsageOwners(ctx, tx, []manageFileDeliveryUsageWitness{witness})
				require.NoError(t, lockErr)
				require.False(t, current[witness.resourceType+"\x00"+witness.resourceID])
				return nil
			}))
		})
	}
}

func TestManagePageUsageSigningCompletesBeforeDocumentChangeIntegration(t *testing.T) {
	db, ctx, service, _, fileID := newFileManagerDeliveryFenceFixture(t)
	pageID, _, _ := seedManageContentOwnerUsage(t, db, "page", fileID)
	usages, err := service.currentManageFileDeliveryUsages(ctx, []string{fileID})
	require.NoError(t, err)
	var witness manageFileDeliveryUsageWitness
	for _, candidate := range usages[fileID] {
		if candidate.resourceType == "page" && candidate.resourceID == pageID {
			witness = candidate
			break
		}
	}
	require.Equal(t, "page", witness.resourceType)
	response, err := service.getFileUrlsForID(ctx, fileID)
	require.NoError(t, err)
	authorization := &manageFileDeliveryAuthorization{
		principal: auth.GetUser(ctx),
		files: map[string]manageFileDeliveryGrant{
			fileID: {usageWitnesses: []manageFileDeliveryUsageWitness{witness}},
		},
	}
	signed := make(chan struct{})
	releaseSigned := make(chan struct{})
	service.testAfterManageDeliverySigned = func([]string) {
		close(signed)
		<-releaseSigned
	}
	type result struct {
		response *managev1.GetMediaDeliveryResponse
		current  bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		finalized, current, finalizeErr := service.finalizeUsageManageFileURLResponse(ctx, fileID, response, authorization)
		done <- result{response: finalized, current: current, err: finalizeErr}
	}()
	<-signed
	replacementDocumentID := uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?::uuid, 'page')", replacementDocumentID).Error)
	backendPID, attempted, changed := startContentOwnerDocumentUpdate(t, db, "page", pageID, replacementDocumentID)
	<-attempted
	requirePostgresBackendWaitingForLock(t, db, backendPID)
	close(releaseSigned)
	got := <-done
	require.NoError(t, got.err)
	require.True(t, got.current)
	require.NotEmpty(t, got.response.GetDelivery().GetInline().GetUrl())
	require.Nil(t, got.response.GetDelivery().GetDownload())
	require.NoError(t, <-changed)
}

func TestManageStrongSigningDoesNotDeadlockAvatarPromotionIntegration(t *testing.T) {
	db, ctx, service, folderID, _ := newFileManagerDeliveryFenceFixture(t)
	fileID := seedFileManagerFile(t, db, folderID, auth.GetUser(ctx).MemberID.String(), "avatar", "webp", "image/webp")
	require.NoError(t, db.Exec(
		"INSERT INTO file_ingest_binding (file_id, upload_type, entity_id) VALUES (?::uuid, ?, ?::uuid)",
		fileID, managev1.UploadType_UPLOAD_TYPE_USER_AVATAR.String(), auth.GetUser(ctx).MemberID.String(),
	).Error)
	identityHeld := make(chan struct{})
	startPromotion := make(chan struct{})
	promotionDone := make(chan error, 1)
	go func() {
		promotionDone <- identitystate.WithMutation(ctx, db, auth.GetUser(ctx).IdentityID.String(), func(mutationCtx context.Context, _ *gorm.DB) error {
			close(identityHeld)
			<-startPromotion
			_, _, _, _, err := service.prepareSourceAssetPromotion(mutationCtx, fileID, "avatar")
			return err
		})
	}()
	<-identityHeld
	principalLockAttempted := make(chan struct{})
	service.testBeforeManagePrincipalLock = func(fileIDs []string) {
		require.Equal(t, []string{fileID}, fileIDs)
		close(principalLockAttempted)
	}
	type result struct {
		response *connect.Response[managev1.GetMediaDeliveryResponse]
		err      error
	}
	signed := make(chan result, 1)
	go func() {
		response, err := service.GetMediaDelivery(ctx, connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID}))
		signed <- result{response: response, err: err}
	}()
	<-principalLockAttempted
	close(startPromotion)
	select {
	case err := <-promotionDone:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("avatar promotion did not complete while the strong signer waited for the principal fence")
	}
	got := <-signed
	require.NoError(t, got.err)
	require.NotEmpty(t, got.response.Msg.GetDelivery().GetDownload().GetUrl())
}

func newFileManagerDeliveryFenceFixture(
	t *testing.T,
) (*gorm.DB, context.Context, *FileService, string, string) {
	t.Helper()
	db := newConcurrentServiceIntegrationDB(t)
	ctx, spiceDB := integrationAdminCtxWithIdentityAndSpiceDB(t, db)
	service := &FileService{
		db: db, spiceDB: spiceDB,
		cdnDomain: "https://cdn.example.test", mediaDomain: "https://media.example.test",
		mediaSecret: "file-manager-fence-secret", downloadTTL: time.Hour,
		asyncPublisher:  noopAsyncPublisher{},
		memberSummaries: newIntegrationMemberSummaries(db, "https://cdn.example.test"),
	}
	folder, err := service.CreateFileFolder(ctx, connect.NewRequest(&managev1.CreateFileFolderRequest{Name: "Fence"}))
	require.NoError(t, err)
	fileID := seedManagedFileManagerFenceFile(t, db, folder.Msg.Folder.GetId(), auth.GetUser(ctx).MemberID.String(), "race")
	return db, ctx, service, folder.Msg.Folder.GetId(), fileID
}

func newManagePostCollaboratorFenceFixture(
	t *testing.T,
) (*gorm.DB, context.Context, *FileService, string, string, string) {
	t.Helper()
	db := newConcurrentServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	memberID := seedExternalKratosIdentityWithTraits(t, db, identityID, "Manage delivery collaborator")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.User())
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(integrationTestUUID()), Authenticated: true, Onboarded: true,
	})
	service := &FileService{
		db: db, spiceDB: spiceDB,
		cdnDomain: "https://cdn.example.test", mediaDomain: "https://media.example.test",
		mediaSecret: "manage-usage-fence-secret", downloadTTL: time.Hour,
		postAccess: newIntegrationPostAccess(db, spiceDB),
	}
	postID, fileID, blockID := seedManagePostCollaboratorUsage(t, db, spiceDB, auth.GetUser(ctx))
	return db, ctx, service, postID, fileID, blockID
}

func seedManagePostCollaboratorUsage(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	user *auth.UserInfo,
) (string, string, string) {
	t.Helper()
	fileID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO file (id, file_name, extension, mime_type, file_size)
		VALUES (?::uuid, 'collaborator', 'mp4', 'video/mp4', 4096)
	`, fileID).Error)
	return seedManagePostCollaboratorUsageForFile(t, db, spiceDB, user, fileID)
}

func seedManagePostCollaboratorUsageForFile(
	t *testing.T,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	user *auth.UserInfo,
	fileID string,
) (string, string, string) {
	t.Helper()
	postID, documentID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO content_document (id, profile) VALUES (?::uuid, 'post')`, documentID).Error)
	require.NoError(t, db.Exec(`INSERT INTO post (id, content_document_id) VALUES (?::uuid, ?::uuid)`, postID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (id, document_id, container_slot, position, kind)
		VALUES (?::uuid, ?::uuid, 'content', 0, 'file')
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience)
		VALUES (?::uuid, 'file', 'active', ?::uuid, 'disabled')
	`, blockID, fileID).Error)
	seedFileDeliveryContentPolicy(t, spiceDB, "post", postID)
	seedFileDeliveryPostCollaboratorAuthority(t, spiceDB, postID, user.IdentityID.String())
	return postID, fileID, blockID
}

func seedManageContentOwnerUsage(
	t *testing.T,
	db *gorm.DB,
	resourceType string,
	fileID string,
) (string, string, string) {
	t.Helper()
	resourceID, documentID, blockID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec("INSERT INTO content_document (id, profile) VALUES (?::uuid, ?)", documentID, resourceType).Error)
	switch resourceType {
	case "page":
		require.NoError(t, db.Exec("INSERT INTO page (id, content_document_id) VALUES (?::uuid, ?::uuid)", resourceID, documentID).Error)
	case "work":
		require.NoError(t, db.Exec(`
			INSERT INTO work (id, type, year, month, is_present, content_document_id)
			VALUES (?::uuid, 'WORK_TYPE_ARTICLE', 2026, 8, TRUE, ?::uuid)
		`, resourceID, documentID).Error)
	default:
		t.Fatalf("unsupported Content owner %q", resourceType)
	}
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (id, document_id, container_slot, position, kind)
		VALUES (?::uuid, ?::uuid, 'content', 0, 'file')
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id, download_audience)
		VALUES (?::uuid, 'file', 'active', ?::uuid, 'disabled')
	`, blockID, fileID).Error)
	return resourceID, documentID, blockID
}

func seedFileDeliveryPostCollaboratorAuthority(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
	postID string,
	identityID string,
) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	grant, err := policyv1.Post.TouchCollaborator(postID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), grant)
	require.NoError(t, err)
}

func startManageUsageDetach(
	t *testing.T,
	db *gorm.DB,
	blockID string,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		if _, err = tx.ExecContext(ctx, "DELETE FROM content_block_attachment WHERE block_id = $1", blockID); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}

func startMeshCandidateStatusUpdate(
	t *testing.T,
	db *gorm.DB,
	candidateID string,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		if _, err = tx.ExecContext(ctx, "UPDATE mesh_optimization_candidate SET status = 'failed' WHERE id = $1", candidateID); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}

func startMeshCompletion(
	t *testing.T,
	db *gorm.DB,
	candidate model.MeshOptimizationCandidate,
	completion meshOptimizationCompletion,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	backendPID := make(chan int, 1)
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		pidSent := false
		attemptedClosed := false
		err := db.Connection(func(connection *gorm.DB) error {
			var pid int
			if err := connection.Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				return err
			}
			backendPID <- pid
			pidSent = true
			return connection.Transaction(func(tx *gorm.DB) error {
				close(attempted)
				attemptedClosed = true
				_, _, err := applyMeshOptimizationCompletionWithDB(context.Background(), tx, &candidate, completion)
				return err
			})
		})
		if !pidSent {
			backendPID <- 0
		}
		if !attemptedClosed {
			close(attempted)
		}
		done <- err
	}()
	return <-backendPID, attempted, done
}

func startContentOwnerDocumentUpdate(
	t *testing.T,
	db *gorm.DB,
	table string,
	resourceID string,
	documentID string,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	if table != "page" && table != "work" {
		t.Fatalf("unsupported Content owner table %q", table)
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		query := "UPDATE " + table + " SET content_document_id = $1 WHERE id = $2"
		if _, err = tx.ExecContext(ctx, query, documentID, resourceID); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}

func startIdentityRevocationUpdate(
	t *testing.T,
	db *gorm.DB,
	identityID string,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		if _, err = tx.ExecContext(ctx, "UPDATE kratos.identities SET state = 'inactive' WHERE id = $1", identityID); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}

func seedManagedFileManagerFenceFile(t *testing.T, db *gorm.DB, folderID, memberID, name string) string {
	t.Helper()
	fileID := seedFileManagerFile(t, db, folderID, memberID, name, "pdf", "application/pdf")
	require.NoError(t, db.Exec(
		"INSERT INTO file_ingest_binding (file_id, upload_type) VALUES (?, ?)",
		fileID,
		managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE.String(),
	).Error)
	return fileID
}

func startFileManagerDeletionUpdate(
	t *testing.T,
	db *gorm.DB,
	fileID string,
) (int, <-chan struct{}, <-chan error) {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	var backendPID int
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&backendPID))
	attempted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		defer cancel()
		defer conn.Close()
		tx, err := conn.BeginTx(ctx, &sql.TxOptions{})
		if err != nil {
			close(attempted)
			done <- err
			return
		}
		close(attempted)
		if _, err = tx.ExecContext(ctx, "UPDATE file SET delete_requested_at = CURRENT_TIMESTAMP WHERE id = $1", fileID); err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		done <- err
	}()
	return backendPID, attempted, done
}

func requirePostgresBackendWaitingForLock(t *testing.T, db *gorm.DB, backendPID int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var waitEventType *string
		err := db.Raw("SELECT wait_event_type FROM pg_stat_activity WHERE pid = ?", backendPID).Scan(&waitEventType).Error
		require.NoError(t, err)
		if waitEventType != nil && *waitEventType == "Lock" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("deletion backend did not wait on the File row lock")
}

func requireFileManagerDeletionPending(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ? AND delete_requested_at IS NOT NULL", fileID).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
