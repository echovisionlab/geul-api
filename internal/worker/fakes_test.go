package worker

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/emaildelivery"
	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// newTestHandlers returns handlers over deps, with every unset required
// port standing in as a collaborator that fails the test when reached, and
// a unit of work over a fresh fakeJobs when Units is unset.
func newTestHandlers(t *testing.T, deps Deps) *Handlers {
	t.Helper()
	unexpected := unreachable{t: t}
	if deps.Units == nil {
		deps.Units = mutationtest.New[Jobs](newFakeJobs())
	}
	if deps.FileMedia == nil {
		deps.FileMedia = unexpected
	}
	if deps.DanglingFiles == nil {
		deps.DanglingFiles = unexpected
	}
	if deps.PublicAssets == nil {
		deps.PublicAssets = unexpected
	}
	if deps.OGCleanup == nil {
		deps.OGCleanup = unexpected
	}
	if deps.FaviconCleanup == nil {
		deps.FaviconCleanup = unexpected
	}
	if deps.Uploads == nil {
		deps.Uploads = unexpected
	}
	if deps.UploadStorage == nil {
		deps.UploadStorage = unexpected
	}
	if deps.FileIngest == nil {
		deps.FileIngest = unexpected
	}
	if deps.ShareLinks == nil {
		deps.ShareLinks = unexpected
	}
	if deps.AuthCodes == nil {
		deps.AuthCodes = unexpected
	}
	if deps.QueueArchives == nil {
		deps.QueueArchives = unexpected
	}
	if deps.MemberDeletions == nil {
		deps.MemberDeletions = unexpected
	}
	if deps.Legal == nil {
		deps.Legal = unexpected
	}
	if deps.Identities == nil {
		deps.Identities = unexpected
	}
	if deps.TimedBans == nil {
		deps.TimedBans = unexpected
	}
	if deps.GeoIP == nil {
		deps.GeoIP = unexpected
	}
	if deps.Campaigns == nil {
		deps.Campaigns = unexpected
	}
	if deps.EmailDelivery == nil {
		deps.EmailDelivery = unexpected
	}
	if deps.BulkEmail == nil {
		deps.BulkEmail = unexpected
	}
	handlers, err := NewHandlers(deps)
	require.NoError(t, err)
	return handlers
}

var errUnreachable = errors.New("unexpected collaborator call")

// unreachable stands in for every port a test does not expect the handler
// to reach.
type unreachable struct{ t *testing.T }

func (u unreachable) fail(method string) error {
	u.t.Helper()
	u.t.Errorf("unexpected call to %s", method)
	return errUnreachable
}

func (u unreachable) HandleFileDelete(context.Context, *managev1.FileDeleteEvent) error {
	return u.fail("HandleFileDelete")
}
func (u unreachable) HandleTranscodeProgress(context.Context, []byte) error {
	return u.fail("HandleTranscodeProgress")
}
func (u unreachable) HandleTranscodeComplete(context.Context, []byte) error {
	return u.fail("HandleTranscodeComplete")
}
func (u unreachable) HandleWaveformProgress(context.Context, []byte) error {
	return u.fail("HandleWaveformProgress")
}
func (u unreachable) HandleWaveformComplete(context.Context, []byte) error {
	return u.fail("HandleWaveformComplete")
}
func (u unreachable) HandleWaveformFail(context.Context, []byte) error {
	return u.fail("HandleWaveformFail")
}
func (u unreachable) HandleMeshOptimizationProgress(context.Context, []byte) error {
	return u.fail("HandleMeshOptimizationProgress")
}
func (u unreachable) HandleMeshOptimizationComplete(context.Context, []byte) error {
	return u.fail("HandleMeshOptimizationComplete")
}
func (u unreachable) HandleMeshOptimizationFail(context.Context, []byte) error {
	return u.fail("HandleMeshOptimizationFail")
}
func (u unreachable) ExpireStaleMeshOptimizationCandidates(context.Context, time.Time) (int64, error) {
	return 0, u.fail("ExpireStaleMeshOptimizationCandidates")
}
func (u unreachable) CleanupDangling(context.Context, time.Time) error {
	return u.fail("CleanupDangling")
}
func (u unreachable) DeletePending(context.Context, time.Time) error {
	return u.fail("DeletePending")
}
func (u unreachable) DeleteExpiredUnready(context.Context, time.Time) error {
	return u.fail("DeleteExpiredUnready")
}
func (u unreachable) MarkUnboundReadyAssets(context.Context, time.Time, time.Time) error {
	return u.fail("MarkUnboundReadyAssets")
}
func (u unreachable) MarkExpiredTerminalGenerationAssets(context.Context, time.Time, time.Time) error {
	return u.fail("MarkExpiredTerminalGenerationAssets")
}
func (u unreachable) MarkDanglingReadyAssets(context.Context, time.Time, time.Time) error {
	return u.fail("MarkDanglingReadyAssets")
}
func (u unreachable) ExpiredUploads(context.Context, time.Time) ([]ExpiredUpload, error) {
	return nil, u.fail("ExpiredUploads")
}
func (u unreachable) ClaimExpiredUpload(context.Context, string, time.Time) (bool, error) {
	return false, u.fail("ClaimExpiredUpload")
}
func (u unreachable) DeleteAbortedUpload(context.Context, string, time.Time) (int64, error) {
	return 0, u.fail("DeleteAbortedUpload")
}
func (u unreachable) AbortMultipartUpload(context.Context, string, string) error {
	return u.fail("AbortMultipartUpload")
}
func (u unreachable) CleanupClientMediaStaging(context.Context, string, string) error {
	return u.fail("CleanupClientMediaStaging")
}
func (u unreachable) PublishFileIngest(context.Context, proto.Message) error {
	return u.fail("PublishFileIngest")
}
func (u unreachable) DeleteExpiredLinks(context.Context, time.Time) (int64, error) {
	return 0, u.fail("DeleteExpiredLinks")
}
func (u unreachable) CleanupExpired(context.Context, time.Time, int) (int64, error) {
	return 0, u.fail("CleanupExpired")
}
func (u unreachable) PurgeExpired(context.Context) ([]QueueArchivePurge, error) {
	return nil, u.fail("PurgeExpired")
}
func (u unreachable) ScrubExpiredEmailProjections(context.Context, time.Time) (int64, error) {
	return 0, u.fail("ScrubExpiredEmailProjections")
}
func (u unreachable) EnqueueExpiredUnonboarded(context.Context, time.Time) (int, error) {
	return 0, u.fail("EnqueueExpiredUnonboarded")
}
func (u unreachable) DueScheduledDeletions(context.Context, time.Time) ([]ScheduledDeletion, error) {
	return nil, u.fail("DueScheduledDeletions")
}
func (u unreachable) DispatchScheduledDeletion(context.Context, string, time.Time) error {
	return u.fail("DispatchScheduledDeletion")
}
func (u unreachable) DeleteIdentity(context.Context, *managev1.UserDeleteIdentityCommand) error {
	return u.fail("DeleteIdentity")
}
func (u unreachable) DeleteAvatar(context.Context, string, string) error {
	return u.fail("DeleteAvatar")
}
func (u unreachable) ScheduledVersions(context.Context, LegalKind) ([]string, error) {
	return nil, u.fail("ScheduledVersions")
}
func (u unreachable) DispatchEffectiveNotice(context.Context, LegalKind, string, time.Time, EffectiveNotice) {
	_ = u.fail("DispatchEffectiveNotice")
}
func (u unreachable) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, u.fail("ListIdentities")
}
func (u unreachable) ClearExpiredTimedBan(context.Context, string, time.Time) (bool, error) {
	return false, u.fail("ClearExpiredTimedBan")
}
func (u unreachable) Refresh(context.Context, time.Time) (geoip.RefreshOutcome, error) {
	return "", u.fail("Refresh")
}
func (u unreachable) DispatchDueEmailDeliveryRuns(context.Context, int) (int, error) {
	return 0, u.fail("DispatchDueEmailDeliveryRuns")
}
func (u unreachable) ResumeActiveEmailDeliveryRuns(context.Context, int) (int, error) {
	return 0, u.fail("ResumeActiveEmailDeliveryRuns")
}
func (u unreachable) Deliver(context.Context, *managev1.SendEmailEvent) (emaildelivery.DeliveryResult, error) {
	return emaildelivery.DeliveryResult{}, u.fail("Deliver")
}
func (u unreachable) Handle(context.Context, *managev1.SendBulkEmailBatchEvent) error {
	return u.fail("Handle")
}

// calls records the port calls a test expects, in order.
type calls struct {
	mu   sync.Mutex
	list []string
}

func (c *calls) add(call string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, call)
}

func (c *calls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.list)
}

// fakeJobs is the job transaction's store in memory: the due Posts, the
// expired metadata AI jobs and the scheduled legal versions a transaction
// changes, rolled back with it.
type fakeJobs struct {
	calls *calls

	// duePosts are the scheduled Posts that are due; published those a
	// committed run published.
	duePosts  []string
	published map[string]bool
	publish   error

	// recovered is how many expired metadata AI jobs a recovery
	// republishes; recoveryLimits the limits each recovery got.
	recovered      int
	recoveryLimits []int
	recovery       error

	// activations are the legal versions activated; due answers whether a
	// version is due, activate and requestOG fail the steps.
	activations map[string]bool
	due         bool
	activate    error
	requestOG   error
	ogRequests  []string
	sealed      EffectiveNotice
	eventKeys   []string
	templates   []map[string]string
}

func newFakeJobs() *fakeJobs {
	return &fakeJobs{calls: &calls{}, published: map[string]bool{}, activations: map[string]bool{}, due: true}
}

func (f *fakeJobs) Savepoint() func() {
	published := maps.Clone(f.published)
	activations := maps.Clone(f.activations)
	ogRequests := slices.Clone(f.ogRequests)
	return func() {
		f.published, f.activations, f.ogRequests = published, activations, ogRequests
	}
}

func (f *fakeJobs) ScheduledPosts() post.Joined {
	return post.Joined{Store: fakeScheduledPosts{jobs: f}}
}

func (f *fakeJobs) MetadataAIRecovery() MetadataAIRecovery { return fakeMetadataAIRecovery{jobs: f} }

func (f *fakeJobs) LegalActivation() LegalActivation { return fakeLegalActivation{jobs: f} }

// fakeScheduledPosts implements the two Post Store methods the scheduled
// publishing calls; any other call panics on the nil Store.
type fakeScheduledPosts struct {
	post.Store
	jobs *fakeJobs
}

func (p fakeScheduledPosts) LockDuePosts(_ context.Context, limit int) ([]post.Post, error) {
	p.jobs.calls.add("LockDuePosts")
	var due []post.Post
	for _, id := range p.jobs.duePosts {
		if !p.jobs.published[id] && len(due) < limit {
			due = append(due, post.Post{ID: id})
		}
	}
	return due, nil
}

func (p fakeScheduledPosts) PublishDuePost(_ context.Context, postID string, _ *time.Time, _ time.Time) (bool, error) {
	p.jobs.calls.add("PublishDuePost " + postID)
	if p.jobs.publish != nil {
		return false, p.jobs.publish
	}
	p.jobs.published[postID] = true
	return true, nil
}

type fakeMetadataAIRecovery struct{ jobs *fakeJobs }

func (r fakeMetadataAIRecovery) RecoverExpiredJobs(_ context.Context, limit int) (int, error) {
	r.jobs.calls.add("RecoverExpiredJobs")
	r.jobs.recoveryLimits = append(r.jobs.recoveryLimits, limit)
	return r.jobs.recovered, r.jobs.recovery
}

type fakeLegalActivation struct{ jobs *fakeJobs }

func (l fakeLegalActivation) ActivateDue(_ context.Context, kind LegalKind, documentID, eventKey string, templateData map[string]string, _ time.Time) (EffectiveNotice, bool, error) {
	l.jobs.calls.add("ActivateDue " + string(kind) + " " + documentID)
	l.jobs.eventKeys = append(l.jobs.eventKeys, eventKey)
	l.jobs.templates = append(l.jobs.templates, templateData)
	if l.jobs.activate != nil {
		return nil, false, l.jobs.activate
	}
	if !l.jobs.due {
		return nil, false, nil
	}
	l.jobs.activations[documentID] = true
	return l.jobs.sealed, true, nil
}

func (l fakeLegalActivation) RequestOG(_ context.Context, kind LegalKind, documentID, reason string) error {
	l.jobs.calls.add("RequestOG " + string(kind) + " " + documentID + " " + reason)
	if l.jobs.requestOG != nil {
		return l.jobs.requestOG
	}
	l.jobs.ogRequests = append(l.jobs.ogRequests, documentID)
	return nil
}
