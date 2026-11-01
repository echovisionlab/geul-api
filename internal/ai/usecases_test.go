package ai_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/ai"
	"github.com/echovisionlab/geul-api/internal/ai/aitest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/llm"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const metadataContext = `{"task":{"requestedKeys":["summary"]},"source":{"title":"Night walks"}}`

type env struct {
	store    *aitest.Store
	units    *mutationtest.UnitOfWork[ai.Store]
	checker  *authztest.Checker
	provider *scriptedProvider
	service  *ai.Service
	runner   *ai.MetadataJobManager
}

func newEnv(t *testing.T) *env {
	t.Helper()
	store, units := aitest.New()
	e := &env{store: store, units: units, checker: authztest.AllowAll(), provider: &scriptedProvider{text: `{"metadata":{"summary":"A walk at night"}}`}}
	var err error
	e.service, err = ai.NewService(ai.Deps{Units: units, SpiceDB: e.checker, Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{}})
	require.NoError(t, err)
	e.runner, err = ai.NewMetadataJobManager(ai.JobManagerDeps{
		Units: units, SpiceDB: e.checker, Fence: &identitystatetest.Fence{}, Audit: &audittest.Recorder{}, Provider: e.provider,
	})
	require.NoError(t, err)
	return e
}

func newRequester(t *testing.T) (*auth.UserInfo, context.Context) {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.98")
	require.NoError(t, err)
	user := &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
	return user, auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), requestContext), user)
}

func codeOf(err error) connect.Code { return errs.Wrap(err).Code() }

func pageTarget() *ai.Target {
	return &ai.Target{Type: "AI_RESOURCE_TYPE_PAGE", ID: uuid.NewString()}
}

type scriptedProvider struct {
	text       string
	err        error
	onGenerate func()
	calls      atomic.Int32
}

func (p *scriptedProvider) GenerateText(context.Context, llm.GenerationRequest) (string, error) {
	p.calls.Add(1)
	if p.onGenerate != nil {
		p.onGenerate()
	}
	return p.text, p.err
}

func (*scriptedProvider) StartSession(context.Context, llm.SessionSpec) (llm.Session, error) {
	return nil, errors.New("sessions are not supported by the metadata test provider")
}

func (*scriptedProvider) ProviderName() string { return "metadata-test" }
func (*scriptedProvider) ModelName() string    { return "metadata-test-model" }

func TestMetadataJobRunsFromQueuedToApplied(t *testing.T) {
	e := newEnv(t)
	user, ctx := newRequester(t)

	queued, err := e.service.StartMetadataGeneration(ctx, ai.StartMetadataGenerationCommand{Target: pageTarget(), Context: metadataContext})
	require.NoError(t, err)
	require.Equal(t, ai.JobQueued, queued.Status)
	require.Equal(t, user.MemberID.String(), queued.RequesterMemberID)
	require.Equal(t, []string{"summary"}, queued.RequestedKeys)
	require.Equal(t, []string{queued.ID}, e.store.Enqueued(), "the job and its command commit together")

	require.NoError(t, e.runner.ProcessJob(context.Background(), queued.ID))
	ready, err := e.service.GetMetadataGenerationJob(ctx, ai.GetMetadataGenerationJobQuery{JobID: queued.ID})
	require.NoError(t, err)
	require.Equal(t, ai.JobReady, ready.Status)
	require.Equal(t, map[string]string{"summary": "A walk at night"}, ready.Suggestion)
	require.Equal(t, "metadata-test", *ready.Provider)

	applied, err := e.service.ResolveMetadataGenerationJob(ctx, ai.ResolveMetadataGenerationJobCommand{JobID: queued.ID, Resolution: ai.ResolutionApplied})
	require.NoError(t, err)
	require.Equal(t, ai.JobApplied, applied.Status)
	require.NotNil(t, applied.ResolvedAt)

	_, err = e.service.ResolveMetadataGenerationJob(ctx, ai.ResolveMetadataGenerationJobCommand{JobID: queued.ID, Resolution: ai.ResolutionDismissed})
	require.Equal(t, connect.CodeInvalidArgument, codeOf(err), "a resolved job is no longer ready")
}

func TestMetadataJobsBelongToTheirRequester(t *testing.T) {
	e := newEnv(t)
	_, ctx := newRequester(t)
	queued, err := e.service.StartMetadataGeneration(ctx, ai.StartMetadataGenerationCommand{Target: pageTarget(), Context: metadataContext})
	require.NoError(t, err)
	_, other := newRequester(t)

	_, err = e.service.GetMetadataGenerationJob(other, ai.GetMetadataGenerationJobQuery{JobID: queued.ID})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	_, err = e.service.ResolveMetadataGenerationJob(other, ai.ResolveMetadataGenerationJobCommand{JobID: queued.ID, Resolution: ai.ResolutionApplied})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	_, err = e.service.GetMetadataGenerationJob(ctx, ai.GetMetadataGenerationJobQuery{JobID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
}

func TestStartMetadataGenerationRejectsAndQueuesNothing(t *testing.T) {
	for name, test := range map[string]struct {
		arrange func(e *env) ai.StartMetadataGenerationCommand
		code    connect.Code
	}{
		"an administrator without a target": {
			arrange: func(*env) ai.StartMetadataGenerationCommand {
				return ai.StartMetadataGenerationCommand{Context: metadataContext}
			},
			code: connect.CodeInvalidArgument,
		},
		"an unsupported requested key": {
			arrange: func(*env) ai.StartMetadataGenerationCommand {
				return ai.StartMetadataGenerationCommand{Target: pageTarget(), Context: `{"task":{"requestedKeys":["title"]}}`}
			},
			code: connect.CodeInvalidArgument,
		},
		"a Work for a non-administrator": {
			arrange: func(e *env) ai.StartMetadataGenerationCommand {
				e.checker.Deny(policyv1.Platform.IsAdmin())
				return ai.StartMetadataGenerationCommand{Target: &ai.Target{Type: ai.TargetWork, ID: uuid.NewString()}, Context: metadataContext}
			},
			code: connect.CodePermissionDenied,
		},
		"a Page for an author who is not an administrator": {
			arrange: func(e *env) ai.StartMetadataGenerationCommand {
				e.checker.Deny(policyv1.Platform.IsAdmin())
				e.checker.Deny(policyv1.Platform.IsAuthor())
				return ai.StartMetadataGenerationCommand{Target: pageTarget(), Context: metadataContext}
			},
			code: connect.CodePermissionDenied,
		},
		"a failed command enqueue": {
			arrange: func(e *env) ai.StartMetadataGenerationCommand {
				e.store.EnqueueErr = errors.New("confirm unavailable")
				return ai.StartMetadataGenerationCommand{Target: pageTarget(), Context: metadataContext}
			},
			code: connect.CodeInternal,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			command := test.arrange(e)
			_, ctx := newRequester(t)

			_, err := e.service.StartMetadataGeneration(ctx, command)

			require.Equal(t, test.code, codeOf(err), "error %v", err)
			require.Empty(t, e.store.Jobs(), "a rejected or failed request stores no job")
		})
	}
}

func TestStartMetadataGenerationLetsAnEditorAskForItsPost(t *testing.T) {
	e := newEnv(t)
	postID := uuid.NewString()
	e.checker.Deny(policyv1.Platform.IsAdmin())
	e.checker.Deny(policyv1.Platform.IsAuthor())
	e.checker.Deny(policyv1.Artist.Edit(postID))

	_, ctx := newRequester(t)
	_, err := e.service.StartMetadataGeneration(ctx, ai.StartMetadataGenerationCommand{
		Target: &ai.Target{Type: ai.TargetPost, ID: postID}, Context: metadataContext,
	})
	require.NoError(t, err, "Post Edit authority admits the request")

	e.checker.Deny(policyv1.Post.Edit(postID))
	_, err = e.service.StartMetadataGeneration(ctx, ai.StartMetadataGenerationCommand{
		Target: &ai.Target{Type: ai.TargetPost, ID: postID}, Context: metadataContext,
	})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
}

func seedJob(e *env, status ai.JobStatus, startedAt time.Time) ai.Job {
	job := aitest.NewJob(uuid.NewString())
	job.Status = status
	if !startedAt.IsZero() {
		job.StartedAt = &startedAt
	}
	job.CreatedAt, job.UpdatedAt = time.Now(), time.Now()
	e.store.SeedJob(job)
	return job
}

func status(t *testing.T, e *env, jobID string) ai.Job {
	t.Helper()
	job, ok := e.store.StoredJob(jobID)
	require.True(t, ok)
	return job
}

func TestProcessJobReclaimsAStaleRunningJob(t *testing.T) {
	e := newEnv(t)
	job := seedJob(e, ai.JobRunning, time.Now().Add(-4*time.Minute))

	require.NoError(t, e.runner.ProcessJob(context.Background(), job.ID))

	require.EqualValues(t, 1, e.provider.calls.Load())
	require.Equal(t, ai.JobReady, status(t, e, job.ID).Status)
}

func TestProcessJobLeavesAFreshRunningJobToItsDelivery(t *testing.T) {
	e := newEnv(t)
	job := seedJob(e, ai.JobRunning, time.Now())

	err := e.runner.ProcessJob(context.Background(), job.ID)

	require.ErrorContains(t, err, "being processed by another delivery")
	require.Zero(t, e.provider.calls.Load())
	require.Equal(t, ai.JobRunning, status(t, e, job.ID).Status)
}

func TestProcessJobRetriesAfterATransientReadFailure(t *testing.T) {
	e := newEnv(t)
	job := seedJob(e, ai.JobQueued, time.Time{})
	outage := errors.New("temporary database outage")
	e.store.ReadErr = outage

	require.ErrorIs(t, e.runner.ProcessJob(context.Background(), job.ID), outage)
	require.Zero(t, e.provider.calls.Load())

	require.NoError(t, e.runner.ProcessJob(context.Background(), job.ID))
	require.Equal(t, ai.JobReady, status(t, e, job.ID).Status)
}

func TestProcessJobStoresACancellationAfterTheDeliveryStops(t *testing.T) {
	e := newEnv(t)
	job := seedJob(e, ai.JobQueued, time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	e.provider.onGenerate = cancel

	require.NoError(t, e.runner.ProcessJob(ctx, job.ID))

	failed := status(t, e, job.ID)
	require.Equal(t, ai.JobFailed, failed.Status)
	require.Equal(t, context.Canceled.Error(), *failed.Error)
}

func TestProcessJobStoresAProviderFailure(t *testing.T) {
	e := newEnv(t)
	job := seedJob(e, ai.JobQueued, time.Time{})
	e.provider.err = errors.New("provider unavailable")

	require.NoError(t, e.runner.ProcessJob(context.Background(), job.ID))

	failed := status(t, e, job.ID)
	require.Equal(t, ai.JobFailed, failed.Status)
	require.Equal(t, "provider unavailable", *failed.Error)
	require.NotNil(t, failed.DurationMS)
}

func TestProcessJobIgnoresAMissingOrFinishedJob(t *testing.T) {
	e := newEnv(t)
	require.NoError(t, e.runner.ProcessJob(context.Background(), uuid.NewString()))
	ready := seedJob(e, ai.JobReady, time.Time{})
	require.NoError(t, e.runner.ProcessJob(context.Background(), ready.ID))
	require.Zero(t, e.provider.calls.Load())
	require.Error(t, e.runner.ProcessJob(context.Background(), " "))
}

func TestRecoverExpiredJobsRepublishesOnlyAMissingCommand(t *testing.T) {
	e := newEnv(t)
	old := time.Now().Add(-11 * time.Minute)
	pending, orphan := seedJob(e, ai.JobQueued, time.Time{}), seedJob(e, ai.JobQueued, time.Time{})
	for _, job := range []ai.Job{pending, orphan} {
		job.UpdatedAt = old
		e.store.SeedJob(job)
	}
	e.store.SeedTransport(pending.ID)

	recovered, err := e.runner.RecoverExpiredJobs(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)
	require.Equal(t, []string{orphan.ID}, e.store.Enqueued())
	require.True(t, status(t, e, orphan.ID).UpdatedAt.After(old))

	recovered, err = e.runner.RecoverExpiredJobs(context.Background(), 0)
	require.NoError(t, err)
	require.Zero(t, recovered, "the refreshed update time holds the job back")
}

func TestRecoverExpiredJobsRepublishesAStaleRunningLease(t *testing.T) {
	e := newEnv(t)
	job := seedJob(e, ai.JobRunning, time.Now().Add(-11*time.Minute))
	job.UpdatedAt = time.Now().Add(-11 * time.Minute)
	e.store.SeedJob(job)

	recovered, err := e.runner.RecoverExpiredJobs(context.Background(), 0)
	require.NoError(t, err)
	require.Equal(t, 1, recovered)

	require.NoError(t, e.runner.ProcessJob(context.Background(), job.ID))
	require.Equal(t, ai.JobReady, status(t, e, job.ID).Status)
}
