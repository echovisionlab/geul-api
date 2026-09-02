//go:build integration

package ai

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestMetadataAIJobManagerLifecycleIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := testutil.SetupOryStack(t).SpiceDBClient
	provider := &stubAIProvider{text: `{"metadata":{"summary":"Generated summary"}}`}
	publisher := &capturingAsyncPublisher{}
	manager := NewMetadataJobManagerWithProvider(db, spiceDB, provider, publisher)
	user := seedMetadataAIRequester(t, db, spiceDB)
	ctx := auth.WithUser(context.Background(), user)

	job, err := manager.StartJob(ctx, user, &managev1.StartMetadataGenerationRequest{
		Context: `{"task":{"requestedKeys":["summary"]},"source":{"title":"AI job integration"}}`,
		Prompt:  "Generate a concise summary",
		Target: &managev1.AIResourceTarget{
			Type: managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE,
			Id:   uuid.NewString(),
		},
	})
	require.NoError(t, err)
	require.NotEmpty(t, job.Id)
	require.Equal(t, managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_QUEUED, job.Status)
	require.Equal(t, []string{"summary"}, job.RequestedKeys)
	requireMetadataAIJobStatus(t, db, job.Id, metadataAIJobStatusQueued)

	queueEvents := decodePublishedRoutedMessages(t, publisher.messages, "", eventpkg.QueueAiMetadataGenerate, func() *managev1.MetadataGenerationQueueEvent {
		return &managev1.MetadataGenerationQueueEvent{}
	})
	require.Len(t, queueEvents, 1)
	require.Equal(t, job.Id, queueEvents[0].JobId)

	require.NoError(t, manager.ProcessJob(ctx, job.Id))
	require.Equal(t, 1, provider.calls)

	ready, err := manager.GetJobForRequester(ctx, user, job.Id)
	require.NoError(t, err)
	require.Equal(t, managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_READY, ready.Status)
	require.NotNil(t, ready.Suggestion)
	require.Equal(t, "Generated summary", ready.Suggestion.GetSummary())
	require.Equal(t, "stub", ready.GetProvider())
	require.Equal(t, "stub-model", ready.GetModel())
	requireMetadataAIJobStatus(t, db, job.Id, metadataAIJobStatusReady)

	require.NoError(t, manager.ProcessJob(ctx, job.Id), "ready jobs should not be processed again")
	require.Equal(t, 1, provider.calls)

	applied, err := manager.ResolveJobForRequester(
		ctx,
		user,
		job.Id,
		managev1.MetadataGenerationJobResolution_METADATA_GENERATION_JOB_RESOLUTION_APPLIED,
	)
	require.NoError(t, err)
	require.Equal(t, managev1.MetadataGenerationJobStatus_METADATA_GENERATION_JOB_STATUS_APPLIED, applied.Status)
	require.NotNil(t, applied.ResolvedAt)
	requireMetadataAIJobStatus(t, db, job.Id, metadataAIJobStatusApplied)

	require.Len(t, publisher.messages, 1, "only the durable generation command is published")
}

func TestMetadataAIJobDispatchFailureRollsBackJobIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	spiceDB := testutil.SetupOryStack(t).SpiceDBClient
	provider := &stubAIProvider{text: `{"metadata":{"summary":"unused"}}`}
	publisher := &capturingAsyncPublisher{confirmedErr: errors.New("confirm unavailable")}
	manager := NewMetadataJobManagerWithProvider(db, spiceDB, provider, publisher)
	user := seedMetadataAIRequester(t, db, spiceDB)
	ctx := auth.WithUser(context.Background(), user)

	job, err := manager.StartJob(ctx, user, &managev1.StartMetadataGenerationRequest{
		Context: `{"task":{"requestedKeys":["summary"]},"source":{"title":"Dispatch failure"}}`,
		Prompt:  "Generate a concise summary",
		Target: &managev1.AIResourceTarget{
			Type: managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE,
			Id:   uuid.NewString(),
		},
	})
	require.Error(t, err)
	require.Nil(t, job)
	require.Zero(t, provider.calls)

	var stored metadataJobRecord
	require.ErrorIs(t, db.
		Where("requester_member_id = ?", user.MemberID.String()).
		Order("created_at DESC").
		First(&stored).Error, gorm.ErrRecordNotFound)
}

func TestMetadataAIJobConcurrentDeliveriesCallProviderOnceIntegration(t *testing.T) {
	db := newConcurrentServiceIntegrationDB(t)
	spiceDB := testutil.SetupOryStack(t).SpiceDBClient
	provider := &stubAIProvider{text: `{"metadata":{"summary":"Generated once"}}`}
	manager := NewMetadataJobManagerWithProvider(db, spiceDB, provider, &capturingAsyncPublisher{})
	user := seedMetadataAIRequester(t, db, spiceDB)
	now := time.Now().UTC()
	job := metadataJobRecord{
		ID:                uuid.NewString(),
		RequesterMemberID: user.MemberID.String(),
		TargetType:        managev1.AIResourceType_AI_RESOURCE_TYPE_PAGE.String(),
		TargetID:          uuid.NewString(),
		RequestedKeys:     []string{"summary"},
		Context:           `{"task":{"requestedKeys":["summary"]},"source":{"title":"Concurrent delivery"}}`,
		Prompt:            "Generate a concise summary",
		Status:            metadataAIJobStatusQueued,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	require.NoError(t, db.Create(&job).Error)
	t.Cleanup(func() {
		require.NoError(t, db.Delete(&metadataJobRecord{}, "id = ?", job.ID).Error)
	})

	var reads atomic.Int32
	var synchronizeInitialReads atomic.Bool
	synchronizeInitialReads.Store(true)
	initialReadsReady := make(chan struct{})
	callbackName := "test:metadata_ai_concurrent_initial_reads"
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(callbackName, func(*gorm.DB) {
		if !synchronizeInitialReads.Load() {
			return
		}
		if reads.Add(1) == 2 {
			synchronizeInitialReads.Store(false)
			close(initialReadsReady)
		}
		<-initialReadsReady
	}))
	t.Cleanup(func() {
		require.NoError(t, db.Callback().Query().Remove(callbackName))
	})

	errorsByWorker := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			errorsByWorker <- manager.ProcessJob(context.Background(), job.ID)
		}()
	}
	workers.Wait()
	close(errorsByWorker)
	for processErr := range errorsByWorker {
		require.NoError(t, processErr)
	}

	require.Equal(t, int32(2), reads.Load())
	require.Equal(t, 1, provider.calls)
	requireMetadataAIJobStatus(t, db, job.ID, metadataAIJobStatusReady)
}

func requireMetadataAIJobStatus(t *testing.T, db *gorm.DB, jobID string, status string) {
	t.Helper()

	var job metadataJobRecord
	require.NoError(t, db.First(&job, "id = ?", jobID).Error)
	require.Equal(t, status, job.Status)
}

func seedMetadataAIRequester(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient) *auth.UserInfo {
	t.Helper()

	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:    identityID,
		Email: "metadata-ai-requester-" + identityID + "@example.test",
		Name:  "Metadata AI Requester",
	})
	memberID := seedActiveMemberEmailPair(t, db, identityID, "metadata-ai-requester-"+identityID+"@example.test")
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, policyv1.Role.Admin())
	require.NoError(t, err)
	return &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(uuid.NewString()),
		Authenticated: true,
		Onboarded:     true,
	}
}
