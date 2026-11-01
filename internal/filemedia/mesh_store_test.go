package filemedia_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type meshFixture struct {
	*libraryFixture
	source, page string
	mesh         *filemedia.MeshOptimizationService
}

func newMeshFixture(t *testing.T) *meshFixture {
	t.Helper()
	f := newLibraryFixture(t)
	file := filemediatest.CatalogFile("mesh", nil)
	file.Extension = new("glb")
	file.MimeType = new("model/gltf-binary")
	f.store.Files[file.ID] = filemediatest.CatalogFileRecord{Row: file, SHA256: make([]byte, 32)}
	page, doc := uuid.NewString(), uuid.NewString()
	f.store.ScopedOwners["page:"+page] = filemediatest.ScopedOwner{DocumentID: doc}
	f.store.ScopedAttachments = []filemediatest.ScopedAttachmentRecord{{OwnerKind: "page", OwnerID: page, DocumentID: doc, Attachment: domain.ScopedAttachment{BlockID: uuid.NewString(), ReferencePath: "file", FileID: file.ID}}}
	return &meshFixture{libraryFixture: f, source: file.ID, page: page, mesh: filemedia.NewMeshOptimizationService(filemedia.MeshOptimizationDeps{Units: f.units, Authorizer: f.checker})}
}
func (f *meshFixture) input() filemedia.MeshOptimizationGenerateInput {
	return filemedia.MeshOptimizationGenerateInput{SourceFileID: f.source, EntityType: managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, EntityID: f.page, TargetRatioPercent: 50, Method: domain.MeshMethodDraco}
}
func (f *meshFixture) candidate(status, pipeline string) domain.MeshCandidate {
	row := filemediatest.MeshCandidate(f.source)
	row.Status = status
	row.EntityType = new(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE.String())
	row.EntityID = new(f.page)
	if pipeline != "" {
		row.PipelineVersion = pipeline
	}
	row.CacheKey = filemedia.BuildMeshOptimizationCacheKey(f.source, row.TargetRatioPercent, row.Method, row.PipelineVersion)
	f.store.PutMeshCandidate(row)
	return row
}
func meshCompleteEvent(row domain.MeshCandidate) *domain.MeshCompleteEvent {
	digest := sha256.Sum256([]byte("optimized mesh"))
	size := int64(512)
	return &domain.MeshCompleteEvent{JobID: *row.JobID, CorrelationID: row.ID, Identity: &domain.MeshIdentity{EntityType: int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE), EntityID: *row.EntityID, FileID: row.SourceFileID}, Output: &domain.MeshOutput{Written: &domain.MeshWritten{FileID: row.OutputObjectID, FileSize: size, SHA256: digest[:]}, OptimizedSizeBytes: &size}}
}

func TestMeshSourceMustBeLinkedForGenerateListSelectionAndDiscard(t *testing.T) {
	f := newMeshFixture(t)
	result, err := f.mesh.GenerateCandidate(f.ctx, f.input())
	require.NoError(t, err)
	require.True(t, result.Enqueued)
	require.Len(t, f.store.MeshJobs, 1)
	unlinked := filemediatest.CatalogFile("unlinked", nil)
	unlinked.Extension = new("glb")
	unlinked.MimeType = new("model/gltf-binary")
	f.store.Files[unlinked.ID] = filemediatest.CatalogFileRecord{Row: unlinked}
	row := filemediatest.MeshCandidate(unlinked.ID)
	row.Status = domain.MeshStatusReady
	row.OutputFileID = new(uuid.NewString())
	row.EntityType = new(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE.String())
	row.EntityID = &f.page
	f.store.PutMeshCandidate(row)
	input := f.input()
	input.SourceFileID = unlinked.ID
	_, err = f.mesh.GenerateCandidate(f.ctx, input)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	_, err = f.mesh.ListCandidates(f.ctx, filemedia.MeshOptimizationListInput{SourceFileID: unlinked.ID, EntityType: input.EntityType, EntityID: f.page})
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	_, err = f.mesh.UseCandidate(f.ctx, row.ID)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	err = f.mesh.DiscardCandidate(f.ctx, row.ID)
	require.Equal(t, connect.CodeNotFound, errs.CodeOf(err))
	require.Contains(t, f.store.MeshCandidateRows, row.ID)
	require.Len(t, f.store.MeshJobs, 1)
}

func TestMeshProfilesKeepIndependentPipelinesCachesAndClearScope(t *testing.T) {
	f := newMeshFixture(t)
	input := f.input()
	legacy, err := f.mesh.GenerateCandidate(f.ctx, input)
	require.NoError(t, err)
	input.Profile = managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1
	particle, err := f.mesh.GenerateCandidate(f.ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, legacy.Candidate.CacheKey, particle.Candidate.CacheKey)
	require.NotEqual(t, legacy.Candidate.ID, particle.Candidate.ID)
	cached, err := f.mesh.GenerateCandidate(f.ctx, input)
	require.NoError(t, err)
	require.True(t, cached.CacheHit)
	require.False(t, cached.Enqueued)
	require.Equal(t, particle.Candidate.ID, cached.Candidate.ID)
	require.Len(t, f.store.MeshJobs, 2)
	require.Equal(t, int32(managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1), f.store.MeshJobs[1].Profile)
	for _, profile := range []managev1.MeshOptimizationProfile{managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_UNSPECIFIED, input.Profile} {
		rows, err := f.mesh.ListCandidates(f.ctx, filemedia.MeshOptimizationListInput{SourceFileID: f.source, EntityType: input.EntityType, EntityID: f.page, Profile: profile})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		if profile == input.Profile {
			require.Equal(t, particle.Candidate.ID, rows[0].ID)
		} else {
			require.Equal(t, legacy.Candidate.ID, rows[0].ID)
		}
	}
	legacyRow, particleRow := f.store.MeshCandidateRows[legacy.Candidate.ID], f.store.MeshCandidateRows[particle.Candidate.ID]
	legacyRow.Status = domain.MeshStatusFailed
	particleRow.Status = domain.MeshStatusFailed
	f.store.PutMeshCandidate(legacyRow)
	f.store.PutMeshCandidate(particleRow)
	service := filemedia.ExportMeshFileService(f.units, f.checker)
	response, err := filemedia.NewConnectHandler(service).ClearMeshOptimizationCandidates(f.ctx, connect.NewRequest(&managev1.ClearMeshOptimizationCandidatesRequest{SourceFileId: f.source, EntityType: input.EntityType, EntityId: f.page, Profile: input.Profile}))
	require.NoError(t, err)
	require.True(t, response.Msg.Success)
	require.Contains(t, f.store.MeshCandidateRows, legacy.Candidate.ID)
	require.NotContains(t, f.store.MeshCandidateRows, particle.Candidate.ID)
}

func TestMeshSelectionPreservesDocumentAttachment(t *testing.T) {
	f := newMeshFixture(t)
	row := f.candidate(domain.MeshStatusReady, "")
	output := filemediatest.CatalogFile("output", nil)
	f.store.Files[output.ID] = filemediatest.CatalogFileRecord{Row: output}
	row.OutputFileID = &output.ID
	f.store.PutMeshCandidate(row)
	selected, err := f.mesh.UseCandidate(f.ctx, row.ID)
	require.NoError(t, err)
	require.NotNil(t, selected.SelectedAt)
	require.Nil(t, selected.ExpiresAt)
	require.Len(t, f.store.ScopedAttachments, 1)
	require.Equal(t, f.source, f.store.ScopedAttachments[0].Attachment.FileID)
}

func TestMeshGenerationQueueFailureRollsBackNewAndRevivedCandidates(t *testing.T) {
	for _, revive := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "revived"}[revive], func(t *testing.T) {
			f := newMeshFixture(t)
			var original domain.MeshCandidate
			if revive {
				original = f.candidate(domain.MeshStatusFailed, "")
			}
			failure := errors.New("queue unavailable")
			f.store.MeshJobError = failure
			_, err := f.mesh.GenerateCandidate(f.ctx, f.input())
			require.ErrorIs(t, err, failure)
			require.Empty(t, f.store.MeshJobs)
			if revive {
				require.Equal(t, original, f.store.MeshCandidateRows[original.ID])
			} else {
				require.Empty(t, f.store.MeshCandidateRows)
			}
		})
	}
}

func TestMeshGenerateRequiresPermissionAndLiveCanonicalGLBSource(t *testing.T) {
	for _, state := range []string{"denied", "pending deletion", "wrong MIME", "wrong extension"} {
		t.Run(state, func(t *testing.T) {
			f := newMeshFixture(t)
			source := f.store.Files[f.source]
			switch state {
			case "denied":
				f.checker.Deny(policyv1.Page.Edit(f.page))
			case "pending deletion":
				source.PendingDeletion = true
			case "wrong MIME":
				source.Row.MimeType = new("image/png")
			case "wrong extension":
				source.Row.Extension = new("bin")
			}
			f.store.Files[f.source] = source
			_, err := f.mesh.GenerateCandidate(f.ctx, f.input())
			require.Error(t, err)
			require.Empty(t, f.store.MeshCandidateRows)
			require.Empty(t, f.store.MeshJobs)
		})
	}
}

func TestMeshCompletionValidatesAllocatedBytesAndReplaysWithoutReplacingTheFile(t *testing.T) {
	f := newMeshFixture(t)
	row := f.candidate(domain.MeshStatusProcessing, "")
	event := meshCompleteEvent(row)
	completed, err := f.mesh.HandleComplete(f.ctx, event)
	require.NoError(t, err)
	require.Equal(t, domain.MeshStatusReady, completed.Status)
	output := f.store.Files[row.OutputObjectID]
	require.Equal(t, int64(512), *output.Row.FileSize)
	require.Equal(t, row.OutputObjectID, output.Row.Name)
	require.Equal(t, "glb", *output.Row.Extension)
	require.Equal(t, event.Output.Written.SHA256, output.SHA256)
	completed, err = f.mesh.HandleComplete(f.ctx, event)
	require.NoError(t, err)
	require.Equal(t, domain.MeshStatusReady, completed.Status)
	event.Output.Written.FileID = uuid.NewString()
	_, err = f.mesh.HandleComplete(f.ctx, event)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	event.Output.Written.FileID = row.OutputObjectID
	event.Output.Written.SHA256 = make([]byte, 32)
	_, err = f.mesh.HandleComplete(f.ctx, event)
	require.ErrorContains(t, err, "conflicts with existing output metadata")
	require.Equal(t, output.SHA256, f.store.Files[row.OutputObjectID].SHA256)
	require.Equal(t, domain.MeshStatusReady, f.store.MeshCandidateRows[row.ID].Status)
}

func TestMeshCompletionRequiresSizeAndTheAllocatedProfile(t *testing.T) {
	for _, mode := range []string{"size", "profile"} {
		t.Run(mode, func(t *testing.T) {
			f := newMeshFixture(t)
			pipeline := ""
			if mode == "profile" {
				pipeline = domain.MeshPipelineParticleMeshV1
			}
			row := f.candidate(domain.MeshStatusProcessing, pipeline)
			event := meshCompleteEvent(row)
			if mode == "size" {
				event.Output.OptimizedSizeBytes = nil
			}
			_, err := f.mesh.HandleComplete(f.ctx, event)
			require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
			require.NotContains(t, f.store.Files, row.OutputObjectID)
			require.Equal(t, domain.MeshStatusProcessing, f.store.MeshCandidateRows[row.ID].Status)
			if mode == "profile" {
				require.ErrorContains(t, err, "output.profile")
				event.Output.Profile = int32(managev1.MeshOptimizationProfile_MESH_OPTIMIZATION_PROFILE_PARTICLE_MESH_V1)
				completed, err := f.mesh.HandleComplete(f.ctx, event)
				require.NoError(t, err)
				require.Equal(t, domain.MeshStatusReady, completed.Status)
			}
		})
	}
}

func TestMeshEventsRequireExactJobCandidateAndIdentity(t *testing.T) {
	for _, mode := range []string{"file", "entity", "entity type", "job", "candidate"} {
		t.Run(mode, func(t *testing.T) {
			f := newMeshFixture(t)
			row := f.candidate(domain.MeshStatusPending, "")
			event := &domain.MeshProgressEvent{JobID: *row.JobID, CorrelationID: row.ID, Identity: meshCompleteEvent(row).Identity}
			switch mode {
			case "file":
				event.Identity.FileID = uuid.NewString()
			case "entity":
				event.Identity.EntityID = uuid.NewString()
			case "entity type":
				event.Identity.EntityType = int32(managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK)
			case "job":
				event.JobID = uuid.NewString()
			case "candidate":
				event.CorrelationID = uuid.NewString()
			}
			_, err := f.mesh.HandleProgress(f.ctx, event)
			want := connect.CodeInvalidArgument
			if mode == "job" || mode == "candidate" {
				want = connect.CodeNotFound
			}
			require.Equal(t, want, errs.CodeOf(err))
			require.Equal(t, domain.MeshStatusPending, f.store.MeshCandidateRows[row.ID].Status)
		})
	}
}

func TestMeshRequeueReturnsTheStoredCandidateAndCleansTheSupersededOutput(t *testing.T) {
	f := newMeshFixture(t)
	row := f.candidate(domain.MeshStatusFailed, "")
	row.EntityID = new(uuid.NewString())
	f.store.PutMeshCandidate(row)
	var deleted string
	deleter := meshDeleteFunc(func(_ context.Context, id string) error { deleted = id; return nil })
	mesh := filemedia.NewMeshOptimizationService(filemedia.MeshOptimizationDeps{Units: f.units, Authorizer: f.checker, FileDeleter: deleter})
	result, err := mesh.GenerateCandidate(f.ctx, f.input())
	require.NoError(t, err)
	require.True(t, result.Enqueued)
	require.Equal(t, row.ID, result.Candidate.ID)
	require.Equal(t, new(f.page), result.Candidate.EntityID)
	require.Equal(t, f.store.MeshCandidateRows[row.ID], result.Candidate)
	require.Equal(t, row.OutputObjectID, deleted)
	require.NotEqual(t, row.OutputObjectID, result.Candidate.OutputObjectID)
}

type meshDeleteFunc func(context.Context, string) error

func (f meshDeleteFunc) DeleteFileByID(ctx context.Context, id string) error { return f(ctx, id) }

func TestMeshRetirementUnprotectsBeforeDeleteAndRetainsCleanupOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "deleted", true: "retry required"}[fail], func(t *testing.T) {
			f := newMeshFixture(t)
			row := f.candidate(domain.MeshStatusReady, "")
			row.OutputFileID = new(row.OutputObjectID)
			f.store.PutMeshCandidate(row)
			failure := errors.New("delete failed")
			var deleted string
			deleter := meshDeleteFunc(func(_ context.Context, id string) error {
				stored := f.store.MeshCandidateRows[row.ID]
				require.Equal(t, domain.MeshStatusCancelled, stored.Status)
				require.Nil(t, stored.OutputFileID)
				deleted = id
				if fail {
					return failure
				}
				return nil
			})
			mesh := filemedia.NewMeshOptimizationService(filemedia.MeshOptimizationDeps{Units: f.units, FileDeleter: deleter})
			err := filemedia.ExportDeleteMeshCandidateAndOutput(mesh, f.ctx, row)
			require.Equal(t, row.OutputObjectID, deleted)
			if fail {
				require.ErrorIs(t, err, failure)
				require.Equal(t, domain.MeshStatusCancelled, f.store.MeshCandidateRows[row.ID].Status)
			} else {
				require.NoError(t, err)
				require.NotContains(t, f.store.MeshCandidateRows, row.ID)
			}
		})
	}
}

func TestFileDeletionRetiresLateMeshCompletionAndQueuesItsOutput(t *testing.T) {
	f := newMeshFixture(t)
	f.store.ScopedAttachments = nil
	row := f.candidate(domain.MeshStatusProcessing, "")
	require.NoError(t, f.files.DeleteFileByID(f.ctx, f.source))
	require.True(t, f.store.Files[f.source].PendingDeletion)
	require.Equal(t, domain.MeshStatusCancelled, f.store.MeshCandidateRows[row.ID].Status)
	require.Len(t, f.store.DeletionOutbox, 1)
	require.Equal(t, f.source, f.store.DeletionOutbox[0].FileID)
	require.False(t, f.store.DeletionOutbox[0].HasPublicAssets)
	require.Empty(t, f.store.DeletionOutbox[0].Generations)
	mesh := filemedia.NewMeshOptimizationService(filemedia.MeshOptimizationDeps{Units: f.units, FileDeleter: f.files})
	completed, err := mesh.HandleComplete(f.ctx, meshCompleteEvent(row))
	require.NoError(t, err)
	require.Equal(t, domain.MeshStatusCancelled, completed.Status)
	require.True(t, f.store.Files[row.OutputObjectID].PendingDeletion)
	require.NotContains(t, f.store.MeshCandidateRows, row.ID)
	require.Len(t, f.store.DeletionOutbox, 2)
	require.Equal(t, row.OutputObjectID, f.store.DeletionOutbox[1].FileID)
}

func TestMeshProgressFailureAndExpiryPreserveCleanupOrder(t *testing.T) {
	f := newMeshFixture(t)
	row := f.candidate(domain.MeshStatusPending, "")
	identity := meshCompleteEvent(row).Identity
	at := time.Now().UTC().Truncate(time.Millisecond)
	progressed, err := f.mesh.HandleProgress(f.ctx, &domain.MeshProgressEvent{JobID: *row.JobID, CorrelationID: row.ID, Identity: identity, TimestampMS: at.UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, domain.MeshStatusProcessing, progressed.Status)
	failed, err := f.mesh.HandleFailed(f.ctx, &domain.MeshFailedEvent{JobID: *row.JobID, CorrelationID: row.ID, Identity: identity, Error: "rejected", TimestampMS: at.UnixMilli()})
	require.NoError(t, err)
	require.Equal(t, domain.MeshStatusFailed, failed.Status)
	require.Equal(t, new("rejected"), failed.ErrorMessage)
	deleted := []string{}
	mesh := filemedia.NewMeshOptimizationService(filemedia.MeshOptimizationDeps{Units: mutationtest.New[filemedia.Store](f.store), FileDeleter: meshDeleteFunc(func(_ context.Context, id string) error {
		deleted = append(deleted, id)
		require.Equal(t, domain.MeshStatusCancelled, f.store.MeshCandidateRows[row.ID].Status)
		return nil
	})})
	count, err := mesh.ExpireStaleUnselectedCandidates(f.ctx, at.Add(8*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.Equal(t, []string{row.OutputObjectID}, deleted)
	require.Empty(t, f.store.MeshCandidateRows)
}

func TestMeshFailureUsesTheReasonWhenTheErrorIsEmpty(t *testing.T) {
	for _, test := range []struct{ name, reason, want string }{
		{"unspecified", "", domain.MeshFailureUnspecified},
		{"rejected", domain.MeshFailureRejected, domain.MeshFailureRejected},
		{"unknown", "999", "999"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newMeshFixture(t)
			row := f.candidate(domain.MeshStatusProcessing, "")
			result, err := f.mesh.HandleFailed(f.ctx, &domain.MeshFailedEvent{JobID: *row.JobID, CorrelationID: row.ID, Identity: meshCompleteEvent(row).Identity, Reason: test.reason, Error: " "})
			require.NoError(t, err)
			require.Equal(t, domain.MeshStatusFailed, result.Status)
			require.Equal(t, new(test.want), result.ErrorMessage)
		})
	}
}
