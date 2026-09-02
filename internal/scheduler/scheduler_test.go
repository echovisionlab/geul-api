package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewSchedulerRequiresDependencies(t *testing.T) {
	assert.PanicsWithValue(t, "scheduler: pusher cannot be nil", func() {
		NewScheduler(nil, &fakeLeader{}, "instance-1")
	})
	assert.PanicsWithValue(t, "scheduler: leader cannot be nil", func() {
		NewScheduler(&recordingPusher{}, nil, "instance-1")
	})

	s := NewScheduler(&recordingPusher{}, &fakeLeader{}, "instance-1")
	require.NotNil(t, s)
}

func TestSchedulerJobRunsOnlyOnLeader(t *testing.T) {
	job := JobProcessScheduledCampaigns

	t.Run("not leader skips push", func(t *testing.T) {
		pusher := &recordingPusher{}
		leader := &fakeLeader{leader: false}
		s := NewScheduler(pusher, leader, "instance-1")
		s.addJob("* * * * *", job)

		require.Len(t, s.cron.Entries(), 1)
		s.cron.Entries()[0].Job.Run()

		assert.Empty(t, pusher.jobs)
	})

	t.Run("leader pushes job", func(t *testing.T) {
		pusher := &recordingPusher{}
		leader := &fakeLeader{leader: true}
		s := NewScheduler(pusher, leader, "instance-1")
		s.addJob("* * * * *", job)

		require.Len(t, s.cron.Entries(), 1)
		s.cron.Entries()[0].Job.Run()

		require.Len(t, pusher.jobs, 1)
		assert.Equal(t, job, pusher.jobs[0])
	})
}

func TestSchedulerJobDoesNotDuplicateRetryCoalescibleWakeup(t *testing.T) {
	job := JobProcessScheduledCampaigns
	pusher := &recordingPusher{failuresBeforeSuccess: 1}
	s := NewScheduler(pusher, &fakeLeader{leader: true}, "instance-1")
	s.addJob("* * * * *", job)

	require.Len(t, s.cron.Entries(), 1)
	s.cron.Entries()[0].Job.Run()

	assert.Equal(t, 1, pusher.calls)
	assert.Empty(t, pusher.jobs)
}

func TestSchedulerFailureLogKeepsTheJobType(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	job := JobUpdateGeoIP
	s := NewScheduler(&recordingPusher{failuresBeforeSuccess: 1}, &fakeLeader{leader: true}, "instance-1")
	s.addJob("* * * * *", job)
	s.cron.Entries()[0].Job.Run()

	var record map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &record))
	assert.Equal(t, string(job), record["type"])
}

func TestSchedulerStopWaitsForLeaderLoop(t *testing.T) {
	leader := &blockingStopLeader{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
		release: make(chan struct{}),
	}
	s := NewScheduler(&recordingPusher{}, leader, "instance-1")
	s.Start(t.Context())
	select {
	case <-leader.started:
	case <-time.After(time.Second):
		t.Fatal("leader loop did not start")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Stop()
	}()
	select {
	case <-leader.stopped:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop leader")
	}
	select {
	case <-done:
		t.Fatal("scheduler returned before leader loop exited")
	default:
	}
	close(leader.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler did not wait for leader loop exit")
	}
}

func TestJobPusherFunc(t *testing.T) {
	job := JobUpdateGeoIP
	publisher := &recordingSchedulerPublisher{}
	adapter := JobPusherFunc(publisher.RunScheduledJob)

	require.NoError(t, adapter.Push(t.Context(), job))
	require.Len(t, publisher.jobs, 1)
	assert.Equal(t, job, publisher.jobs[0])
	assert.True(t, publisher.sawContext)

	publisher.err = errors.New("publish failed")
	assert.ErrorIs(t, adapter.Push(t.Context(), job), publisher.err)
}

func TestSchedulerJobNamesRemainStable(t *testing.T) {
	assert.Equal(t, "cleanup.dangling", string(JobCleanupDangling))
	assert.Equal(t, "cleanup.public_assets", string(JobCleanupPublicAssets))
	assert.Equal(t, "email.campaign_due", string(JobProcessScheduledCampaigns))
	assert.Equal(t, "post.scheduled_due", string(JobProcessScheduledPosts))
	assert.Equal(t, "cleanup.auth_code_issuance", string(JobCleanupAuthCodeIssuance))
	assert.Equal(t, "cleanup.pgmq_archives", string(JobCleanupPGMQArchives))
}

func TestGeoIPWakeupRunsDailyAfterCleanupWindow(t *testing.T) {
	require.Equal(t, "15 3 * * *", geoIPWakeupSchedule)
}

type recordingPusher struct {
	calls                 int
	failuresBeforeSuccess int
	jobs                  []Job
}

func (p *recordingPusher) Push(_ context.Context, job Job) error {
	p.calls++
	if p.calls <= p.failuresBeforeSuccess {
		return errors.New("temporary push failure")
	}
	p.jobs = append(p.jobs, job)
	return nil
}

type fakeLeader struct {
	leader bool
}

func (l *fakeLeader) IsLeader() bool {
	return l.leader
}

func (l *fakeLeader) Start(ctx context.Context) {
	<-ctx.Done()
}

func (l *fakeLeader) Stop() {}

type recordingSchedulerPublisher struct {
	err        error
	jobs       []Job
	sawContext bool
}

type blockingStopLeader struct {
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (l *blockingStopLeader) IsLeader() bool { return false }
func (l *blockingStopLeader) Start(ctx context.Context) {
	close(l.started)
	<-ctx.Done()
	<-l.release
}
func (l *blockingStopLeader) Stop() { close(l.stopped) }

func (p *recordingSchedulerPublisher) RunScheduledJob(ctx context.Context, job Job) error {
	p.sawContext = ctx != nil
	if p.err != nil {
		return p.err
	}
	p.jobs = append(p.jobs, job)
	return nil
}
