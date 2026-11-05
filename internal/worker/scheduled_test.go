package worker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/favicon"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/scheduler"
)

// scheduledPorts records every scheduled job's port calls.
type scheduledPorts struct {
	unreachable
	calls *calls
	err   error
}

func (p scheduledPorts) record(call string) error {
	p.calls.add(call)
	return p.err
}

func (p scheduledPorts) CleanupDangling(context.Context, time.Time) error {
	return p.record("CleanupDangling")
}
func (p scheduledPorts) ExpireStaleMeshOptimizationCandidates(context.Context, time.Time) (int64, error) {
	return 1, p.record("ExpireStaleMeshOptimizationCandidates")
}
func (p scheduledPorts) MarkUnboundReadyAssets(context.Context, time.Time, time.Time) error {
	return p.record("MarkUnboundReadyAssets")
}
func (p scheduledPorts) MarkDanglingReadyAssets(context.Context, time.Time, time.Time) error {
	return p.record("MarkDanglingReadyAssets")
}
func (p scheduledPorts) MarkExpiredTerminalGenerationAssets(context.Context, time.Time, time.Time) error {
	return p.record("MarkExpiredTerminalGenerationAssets")
}
func (p scheduledPorts) DeletePending(context.Context, time.Time) error {
	return p.record("DeletePending")
}
func (p scheduledPorts) DeleteExpiredUnready(context.Context, time.Time) error {
	return p.record("DeleteExpiredUnready")
}
func (p scheduledPorts) ExpiredUploads(context.Context, time.Time) ([]ExpiredUpload, error) {
	return nil, p.record("ExpiredUploads")
}
func (p scheduledPorts) DeleteExpiredLinks(context.Context, time.Time) (int64, error) {
	return 1, p.record("DeleteExpiredLinks")
}
func (p scheduledPorts) CleanupExpired(context.Context, time.Time, int) (int64, error) {
	return 1, p.record("CleanupExpired")
}
func (p scheduledPorts) PurgeExpired(context.Context) ([]QueueArchivePurge, error) {
	return []QueueArchivePurge{{Queue: "email.send", Deleted: 1}}, p.record("PurgeExpired")
}
func (p scheduledPorts) ScrubExpiredEmailProjections(context.Context, time.Time) (int64, error) {
	return 0, p.record("ScrubExpiredEmailProjections")
}
func (p scheduledPorts) EnqueueExpiredUnonboarded(context.Context, time.Time) (int, error) {
	return 0, p.record("EnqueueExpiredUnonboarded")
}
func (p scheduledPorts) DueScheduledDeletions(context.Context, time.Time) ([]ScheduledDeletion, error) {
	return nil, p.record("DueScheduledDeletions")
}
func (p scheduledPorts) ScheduledVersions(_ context.Context, kind LegalKind) ([]string, error) {
	return nil, p.record("ScheduledVersions " + string(kind))
}
func (p scheduledPorts) Refresh(context.Context, time.Time) (geoip.RefreshOutcome, error) {
	return geoip.RefreshCurrent, p.record("Refresh")
}
func (p scheduledPorts) DispatchDueEmailDeliveryRuns(_ context.Context, limit int) (int, error) {
	require.Equal(p.t, scheduledCampaignBatch, limit)
	return 0, p.record("DispatchDueEmailDeliveryRuns")
}
func (p scheduledPorts) ResumeActiveEmailDeliveryRuns(_ context.Context, limit int) (int, error) {
	require.Equal(p.t, scheduledCampaignBatch, limit)
	return 0, p.record("ResumeActiveEmailDeliveryRuns")
}

func newScheduledHandlers(t *testing.T, ports scheduledPorts, jobs *fakeJobs) (*Handlers, *mutationtest.UnitOfWork[Jobs]) {
	t.Helper()
	units := mutationtest.New[Jobs](jobs)
	return newTestHandlers(t, Deps{
		Units: units, FileMedia: ports, DanglingFiles: ports, PublicAssets: ports, OGCleanup: ports,
		FaviconCleanup: ports, Uploads: ports, ShareLinks: ports, AuthCodes: ports, QueueArchives: ports,
		MemberDeletions: ports, Legal: ports, Identities: identityPages{}, GeoIP: ports, Campaigns: ports,
	}), units
}

func TestHandleScheduledRoutesEachJobToItsOwner(t *testing.T) {
	for job, want := range map[scheduler.Job][]string{
		scheduler.JobCleanupDangling: {"CleanupDangling", "ExpireStaleMeshOptimizationCandidates"},
		scheduler.JobCleanupPublicAssets: {
			"MarkUnboundReadyAssets", "MarkDanglingReadyAssets", "MarkExpiredTerminalGenerationAssets",
			"DeletePending", "DeleteExpiredUnready",
		},
		scheduler.JobCleanupIncomplete:         {"ExpiredUploads"},
		scheduler.JobCleanupShareLinks:         {"DeleteExpiredLinks"},
		scheduler.JobCleanupAuthCodeIssuance:   {"CleanupExpired"},
		scheduler.JobCleanupPGMQArchives:       {"PurgeExpired"},
		scheduler.JobProcessUserDeletions:      {"ScrubExpiredEmailProjections", "EnqueueExpiredUnonboarded", "DueScheduledDeletions"},
		scheduler.JobActivateTerms:             {"ScheduledVersions terms"},
		scheduler.JobActivatePrivacy:           {"ScheduledVersions privacy"},
		scheduler.JobUnbanExpired:              nil, // identityPages, an empty directory
		scheduler.JobUpdateGeoIP:               {"Refresh"},
		scheduler.JobProcessScheduledCampaigns: {"DispatchDueEmailDeliveryRuns", "ResumeActiveEmailDeliveryRuns"},
		scheduler.JobProcessScheduledPosts:     {"LockDuePosts"},
	} {
		t.Run(string(job), func(t *testing.T) {
			recorded := &calls{}
			jobs := newFakeJobs()
			jobs.calls = recorded
			handlers, _ := newScheduledHandlers(t, scheduledPorts{unreachable: unreachable{t: t}, calls: recorded}, jobs)

			require.NoError(t, handlers.HandleScheduled(t.Context(), job))
			require.Equal(t, want, recorded.all())
		})
	}
}

func TestHandleScheduledRejectsAnUnknownJob(t *testing.T) {
	handlers := newTestHandlers(t, Deps{})

	require.ErrorContains(t, handlers.HandleScheduled(t.Context(), scheduler.Job("missing")), `unknown scheduler job: "missing"`)
}

func TestScheduledCleanupsRunEveryStepAndReportEveryFailure(t *testing.T) {
	failed := errors.New("cleanup step failed")
	for job, steps := range map[scheduler.Job]int{
		scheduler.JobCleanupDangling:     2,
		scheduler.JobCleanupPublicAssets: 5,
	} {
		t.Run(string(job), func(t *testing.T) {
			recorded := &calls{}
			handlers, _ := newScheduledHandlers(t, scheduledPorts{unreachable: unreachable{t: t}, calls: recorded, err: failed}, newFakeJobs())

			err := handlers.HandleScheduled(t.Context(), job)

			require.ErrorIs(t, err, failed)
			require.Len(t, recorded.all(), steps, "a failed step does not skip the next")
		})
	}
}

// cutoffs records the cutoffs and times each public asset step got.
type cutoffs struct {
	unreachable
	got map[string][2]time.Time
}

func (c cutoffs) MarkUnboundReadyAssets(_ context.Context, cutoff, now time.Time) error {
	c.got["MarkUnboundReadyAssets"] = [2]time.Time{cutoff, now}
	return nil
}
func (c cutoffs) MarkDanglingReadyAssets(_ context.Context, cutoff, now time.Time) error {
	c.got["MarkDanglingReadyAssets"] = [2]time.Time{cutoff, now}
	return nil
}
func (c cutoffs) MarkExpiredTerminalGenerationAssets(_ context.Context, cutoff, now time.Time) error {
	c.got["MarkExpiredTerminalGenerationAssets"] = [2]time.Time{cutoff, now}
	return nil
}
func (c cutoffs) DeletePending(_ context.Context, now time.Time) error {
	c.got["DeletePending"] = [2]time.Time{now, now}
	return nil
}
func (c cutoffs) DeleteExpiredUnready(_ context.Context, cutoff time.Time) error {
	c.got["DeleteExpiredUnready"] = [2]time.Time{cutoff, cutoff}
	return nil
}

func TestPublicAssetCleanupMarksEachKindAtItsRetention(t *testing.T) {
	ports := cutoffs{unreachable: unreachable{t: t}, got: map[string][2]time.Time{}}
	handlers := newTestHandlers(t, Deps{PublicAssets: ports, OGCleanup: ports, FaviconCleanup: ports})
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	utc := now.UTC()

	require.NoError(t, handlers.handleCleanupPublicAssets(t.Context(), now))

	require.Equal(t, map[string][2]time.Time{
		"MarkUnboundReadyAssets":              {utc.Add(-og.UnboundAssetRetention), utc},
		"MarkDanglingReadyAssets":             {utc.Add(-favicon.DanglingAssetRetention), utc},
		"MarkExpiredTerminalGenerationAssets": {utc.Add(-og.TerminalAssetRetention), utc},
		"DeletePending":                       {utc, utc},
		"DeleteExpiredUnready":                {utc.Add(-mediaasset.UnreadyPublicAssetRetention), utc.Add(-mediaasset.UnreadyPublicAssetRetention)},
	}, ports.got)
}

// retention answers the single-step cleanups.
type retention struct {
	unreachable
	limits []int
	err    error
}

func (r *retention) DeleteExpiredLinks(context.Context, time.Time) (int64, error) { return 0, r.err }
func (r *retention) CleanupExpired(_ context.Context, _ time.Time, limit int) (int64, error) {
	r.limits = append(r.limits, limit)
	return 0, r.err
}
func (r *retention) PurgeExpired(context.Context) ([]QueueArchivePurge, error) { return nil, r.err }

func TestSingleStepCleanupsReturnTheirOwnersFailure(t *testing.T) {
	failed := errors.New("owner failed")
	for _, job := range []scheduler.Job{scheduler.JobCleanupShareLinks, scheduler.JobCleanupAuthCodeIssuance, scheduler.JobCleanupPGMQArchives} {
		t.Run(string(job), func(t *testing.T) {
			ports := &retention{unreachable: unreachable{t: t}, err: failed}
			handlers := newTestHandlers(t, Deps{ShareLinks: ports, AuthCodes: ports, QueueArchives: ports})

			require.ErrorIs(t, handlers.HandleScheduled(t.Context(), job), failed)
		})
	}
}

func TestAuthCodeIssuanceCleanupIsBounded(t *testing.T) {
	ports := &retention{unreachable: unreachable{t: t}}
	handlers := newTestHandlers(t, Deps{AuthCodes: ports})

	require.NoError(t, handlers.HandleScheduled(t.Context(), scheduler.JobCleanupAuthCodeIssuance))
	require.Equal(t, []int{authCodeIssuanceCleanupLimit}, ports.limits)
}

type campaignRuns struct {
	unreachable
	calls         *calls
	dispatchError error
}

func (c campaignRuns) DispatchDueEmailDeliveryRuns(context.Context, int) (int, error) {
	c.calls.add("dispatch")
	return 0, c.dispatchError
}
func (c campaignRuns) ResumeActiveEmailDeliveryRuns(context.Context, int) (int, error) {
	c.calls.add("resume")
	return 0, nil
}

func TestScheduledCampaignsResumeOnlyAfterTheDueRunsDispatched(t *testing.T) {
	failed := errors.New("dispatch failed")
	recorded := &calls{}
	handlers := newTestHandlers(t, Deps{Campaigns: campaignRuns{unreachable: unreachable{t: t}, calls: recorded, dispatchError: failed}})

	require.ErrorIs(t, handlers.HandleScheduled(t.Context(), scheduler.JobProcessScheduledCampaigns), failed)
	require.Equal(t, []string{"dispatch"}, recorded.all())
}

func TestScheduledPostsPublishInOneJobTransaction(t *testing.T) {
	jobs := newFakeJobs()
	jobs.duePosts = []string{"post-1", "post-2"}
	units := mutationtest.New[Jobs](jobs)
	handlers := newTestHandlers(t, Deps{Units: units})

	require.NoError(t, handlers.HandleScheduled(t.Context(), scheduler.JobProcessScheduledPosts))

	require.Equal(t, map[string]bool{"post-1": true, "post-2": true}, jobs.published)
	require.Equal(t, []mutation.Step{mutation.StepCommit}, units.Steps())
}

func TestScheduledPostsRollBackWhenAPublishFails(t *testing.T) {
	failed := errors.New("publish failed")
	jobs := newFakeJobs()
	jobs.duePosts = []string{"post-1"}
	jobs.publish = failed
	units := mutationtest.New[Jobs](jobs)
	handlers := newTestHandlers(t, Deps{Units: units})

	require.ErrorIs(t, handlers.HandleScheduled(t.Context(), scheduler.JobProcessScheduledPosts), failed)
	require.Empty(t, jobs.published)
	require.Equal(t, []mutation.Step{mutation.StepRollback}, units.Steps())
}

type geoIPOutcome struct {
	unreachable
	outcome geoip.RefreshOutcome
	err     error
}

func (g geoIPOutcome) Refresh(context.Context, time.Time) (geoip.RefreshOutcome, error) {
	return g.outcome, g.err
}

func TestGeoIPUpdateSucceedsOnEveryOutcomeAndFailsOnlyOnError(t *testing.T) {
	for _, outcome := range []geoip.RefreshOutcome{geoip.RefreshCurrent, geoip.RefreshProviderUnconfigured, geoip.RefreshUpdated, geoip.RefreshBecameCurrent} {
		handlers := newTestHandlers(t, Deps{GeoIP: geoIPOutcome{unreachable: unreachable{t: t}, outcome: outcome}})
		require.NoError(t, handlers.HandleScheduled(t.Context(), scheduler.JobUpdateGeoIP), outcome)
	}
	failed := errors.New("download failed")
	handlers := newTestHandlers(t, Deps{GeoIP: geoIPOutcome{unreachable: unreachable{t: t}, err: failed}})
	require.ErrorIs(t, handlers.HandleScheduled(t.Context(), scheduler.JobUpdateGeoIP), failed)
}

// deletions plays Account's scheduled deletion steps.
type deletions struct {
	unreachable
	calls       *calls
	scrub       error
	unonboarded error
	due         []ScheduledDeletion
	list        error
	dispatch    map[string]error
}

func (d *deletions) ScrubExpiredEmailProjections(context.Context, time.Time) (int64, error) {
	d.calls.add("scrub")
	return 0, d.scrub
}
func (d *deletions) EnqueueExpiredUnonboarded(context.Context, time.Time) (int, error) {
	d.calls.add("unonboarded")
	return 0, d.unonboarded
}
func (d *deletions) DueScheduledDeletions(context.Context, time.Time) ([]ScheduledDeletion, error) {
	d.calls.add("due")
	return d.due, d.list
}
func (d *deletions) DispatchScheduledDeletion(_ context.Context, requestID string, _ time.Time) error {
	d.calls.add("dispatch " + requestID)
	return d.dispatch[requestID]
}

func TestUserDeletionsStopWhenTheEmailScrubFails(t *testing.T) {
	failed := errors.New("scrub failed")
	ports := &deletions{unreachable: unreachable{t: t}, calls: &calls{}, scrub: failed}
	handlers := newTestHandlers(t, Deps{MemberDeletions: ports})

	err := handlers.handleProcessUserDeletions(t.Context(), time.Now())

	require.ErrorIs(t, err, failed)
	require.ErrorContains(t, err, "scrub expired Member email projections")
	require.Equal(t, []string{"scrub"}, ports.calls.all())
}

func TestUserDeletionsDispatchEveryDueRequestPastAFailedOne(t *testing.T) {
	unonboardedFailed := errors.New("unonboarded dispatch failed")
	ports := &deletions{
		unreachable: unreachable{t: t}, calls: &calls{}, unonboarded: unonboardedFailed,
		due: []ScheduledDeletion{{ID: "blocked"}, {ID: "failed"}, {ID: "accepted"}},
		dispatch: map[string]error{
			"blocked": account.ErrLastActiveAdminDeletion,
			"failed":  errors.New("publish failed"),
		},
	}
	handlers := newTestHandlers(t, Deps{MemberDeletions: ports})

	err := handlers.handleProcessUserDeletions(t.Context(), time.Now())

	require.ErrorIs(t, err, unonboardedFailed, "the unonboarded failure is the job's result")
	require.ErrorContains(t, err, "dispatch expired unonboarded Member deletions")
	require.Equal(t, []string{"scrub", "unonboarded", "due", "dispatch blocked", "dispatch failed", "dispatch accepted"}, ports.calls.all())
}

func TestUserDeletionsReportBothTheListAndTheUnonboardedFailure(t *testing.T) {
	unonboardedFailed := errors.New("unonboarded dispatch failed")
	listFailed := errors.New("list failed")
	ports := &deletions{unreachable: unreachable{t: t}, calls: &calls{}, unonboarded: unonboardedFailed, list: listFailed}
	handlers := newTestHandlers(t, Deps{MemberDeletions: ports})

	err := handlers.handleProcessUserDeletions(t.Context(), time.Now())

	require.ErrorIs(t, err, unonboardedFailed)
	require.ErrorIs(t, err, listFailed)
}

func TestUserDeletionsSucceedWithoutDueRequests(t *testing.T) {
	ports := &deletions{unreachable: unreachable{t: t}, calls: &calls{}}
	handlers := newTestHandlers(t, Deps{MemberDeletions: ports})

	require.NoError(t, handlers.handleProcessUserDeletions(t.Context(), time.Now()))
	require.Equal(t, []string{"scrub", "unonboarded", "due"}, ports.calls.all())
}
