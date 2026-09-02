package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

const geoIPWakeupSchedule = "15 3 * * *"

type Job string

const (
	JobCleanupDangling           Job = "cleanup.dangling"
	JobCleanupPublicAssets       Job = "cleanup.public_assets"
	JobCleanupIncomplete         Job = "cleanup.incomplete"
	JobCleanupShareLinks         Job = "cleanup.share_links"
	JobCleanupAuthCodeIssuance   Job = "cleanup.auth_code_issuance"
	JobCleanupPGMQArchives       Job = "cleanup.pgmq_archives"
	JobProcessUserDeletions      Job = "cleanup.user_deletions"
	JobActivateTerms             Job = "policy.terms"
	JobActivatePrivacy           Job = "policy.privacy"
	JobUnbanExpired              Job = "policy.unban"
	JobUpdateGeoIP               Job = "maintenance.geoip"
	JobProcessScheduledCampaigns Job = "email.campaign_due"
	JobProcessScheduledPosts     Job = "post.scheduled_due"
)

// JobPusher runs a coalescible scheduler wake-up against authoritative state.
type JobPusher interface {
	Push(ctx context.Context, job Job) error
}

type JobPusherFunc func(context.Context, Job) error

func (push JobPusherFunc) Push(ctx context.Context, job Job) error {
	return push(ctx, job)
}

type Scheduler struct {
	cron       *cron.Cron
	pusher     JobPusher
	leader     LeaderElector
	instanceID string

	mu        sync.RWMutex
	runCtx    context.Context
	cancel    context.CancelFunc
	startOnce sync.Once
	stopOnce  sync.Once
	leaderWG  sync.WaitGroup
}

// NewScheduler creates a new scheduler.
// Panics if pusher or leader is nil.
func NewScheduler(pusher JobPusher, leader LeaderElector, instanceID string) *Scheduler {
	if pusher == nil {
		panic("scheduler: pusher cannot be nil")
	}
	if leader == nil {
		panic("scheduler: leader cannot be nil")
	}

	// Asia/Seoul timezone
	loc, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		loc = time.FixedZone("KST", 9*60*60)
	}

	return &Scheduler{
		cron:       cron.New(cron.WithLocation(loc)),
		pusher:     pusher,
		leader:     leader,
		instanceID: instanceID,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	s.startOnce.Do(func() {
		s.start(ctx)
	})
}

func (s *Scheduler) start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.runCtx = runCtx
	s.cancel = cancel
	s.mu.Unlock()

	s.leaderWG.Go(func() {
		s.leader.Start(runCtx)
	})

	// Cleanup jobs
	s.addJob("5 * * * *", JobCleanupAuthCodeIssuance)     // Hourly at :05
	s.addJob("10 * * * *", JobCleanupPGMQArchives)        // Hourly at :10
	s.addJob("0 2 * * *", JobCleanupShareLinks)           // Daily 2 AM
	s.addJob("*/5 * * * *", JobCleanupPublicAssets)       // Every 5 minutes
	s.addJob("0 3 * * *", JobCleanupDangling)             // Daily 3 AM
	s.addJob("0 4 * * *", JobCleanupIncomplete)           // Daily 4 AM
	s.addJob("0 5 * * *", JobProcessUserDeletions)        // Daily 5 AM
	s.addJob("*/1 * * * *", JobProcessScheduledCampaigns) // Every minute
	s.addJob("*/1 * * * *", JobProcessScheduledPosts)     // Every minute

	// Policy jobs
	s.addJob("*/15 * * * *", JobActivateTerms)   // Every 15 minutes
	s.addJob("*/15 * * * *", JobActivatePrivacy) // Every 15 minutes
	s.addJob("0 * * * *", JobUnbanExpired)       // Hourly at :00

	// Maintenance jobs
	// Daily wake-up bounds a missed unconfirmed publish to one day. The handler
	// consults geoip_metadata and downloads only when the current dataset is at
	// least seven days old.
	s.addJob(geoIPWakeupSchedule, JobUpdateGeoIP) // Daily at 03:15

	s.cron.Start()
	slog.Info("Scheduler started", "instance", s.instanceID)
}

func (s *Scheduler) addJob(schedule string, job Job) {
	_, err := s.cron.AddFunc(schedule, func() {
		ctx := s.context()
		if ctx.Err() != nil || !s.leader.IsLeader() {
			return
		}
		if err := s.pusher.Push(ctx, job); err != nil {
			// Scheduler messages are coalescible wake-ups. The next authoritative
			// DB scan recovers a missed tick, so retry belongs neither here nor in
			// a second middleware loop around the queue consumer.
			slog.Warn("Failed to push scheduled wake-up", "type", string(job), "error", err)
			return
		}
		slog.Info("Pushed scheduled job", "type", string(job))
	})
	if err != nil {
		slog.Error("Failed to add cron job", "schedule", schedule, "type", job, "error", err)
	}
}

func (s *Scheduler) context() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.runCtx == nil {
		return context.Background()
	}
	return s.runCtx
}

func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		s.mu.RLock()
		cancel := s.cancel
		s.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
		cronStopped := s.cron.Stop()
		s.leader.Stop()
		<-cronStopped.Done()
		s.leaderWG.Wait()
	})
}
