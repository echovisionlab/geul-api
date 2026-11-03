package release_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/release/releasetest"

	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"

	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is one Release use-case test: the service over the fake store,
// its unit of work and the fakes of every other port.
type harness struct {
	service   *release.Service
	store     *releasetest.Store
	units     *mutationtest.UnitOfWork[release.Store]
	checker   *authztest.Checker
	fence     *identitystatetest.Fence
	audit     *audittest.Recorder
	members   *members
	uploads   *uploads
	jobs      *jobs
	principal principal
}

type harnessOption func(*harness)

func withChecker(checker *authztest.Checker) harnessOption {
	return func(h *harness) { h.checker = checker }
}

func withAudit(recorder *audittest.Recorder) harnessOption {
	return func(h *harness) { h.audit = recorder }
}

func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()
	store, units := releasetest.New()
	h := &harness{
		store: store, units: units, checker: authztest.AllowAll(), fence: &identitystatetest.Fence{},
		audit: &audittest.Recorder{}, members: &members{summaries: map[string]*memberref.Summary{}},
		uploads: &uploads{}, jobs: &jobs{}, principal: newPrincipal(),
	}
	for _, option := range options {
		option(h)
	}
	store.Members[h.principal.memberID] = releasetest.Member{Nickname: "Editor"}
	service, err := release.NewService(release.Deps{
		Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Members: h.members,
		TrackFiles: h.uploads, Transcodes: h.jobs, WaveformJobs: h.jobs,
	})
	require.NoError(t, err)
	h.service = service
	return h
}

// principal is a signed-in Member.
type principal struct {
	identityID string
	memberID   string
}

func newPrincipal() principal {
	return principal{identityID: uuid.NewString(), memberID: uuid.NewString()}
}

func (p principal) user() *auth.UserInfo {
	return &auth.UserInfo{
		IdentityID: auth.IdentityID(p.identityID), MemberID: auth.MemberID(p.memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	}
}

// context carries p and the request correlation a request audit needs.
func (p principal) context(t *testing.T) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.81")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	ctx = sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{IdentityID: p.identityID, MemberID: p.memberID, SessionID: uuid.NewString()})
	return auth.WithUser(ctx, p.user())
}

func (h *harness) ctx(t *testing.T) context.Context {
	t.Helper()
	return h.principal.context(t)
}

// seedRelease stores a draft English album with its source title and an
// empty content document, and returns it.
func (h *harness) seedRelease(t *testing.T, title string, change ...func(*release.Release)) release.Release {
	t.Helper()
	documentID := uuid.New()
	documentText := documentID.String()
	now := time.Now().UTC().Truncate(time.Microsecond)
	value := release.Release{
		ID: uuid.NewString(), ContentDocumentID: &documentText, SourceLocale: "en", Type: release.TypeAlbum,
		Status: release.StatusDraft, CreatedAt: now, UpdatedAt: now,
	}
	for _, apply := range change {
		apply(&value)
	}
	h.store.PutRelease(value)
	h.store.PutDocument(releasetest.Document{ID: documentID, Profile: "compact", Revision: uuid.New(), SourceLocale: value.SourceLocale})
	h.store.PutLocale(value.ID, release.LocaleMetadata{Locale: value.SourceLocale, Title: &title, UpdatedAt: now}, nil)
	return value
}

// seedTrack stores a Track of the Release numbered number.
func (h *harness) seedTrack(t *testing.T, releaseID string, number int, change ...func(*release.Track)) release.Track {
	t.Helper()
	track := release.Track{
		ID: uuid.NewString(), ReleaseID: releaseID, TrackNumber: number, Title: "Track",
		Metadata: map[string]any{}, CreatedAt: time.Now().UTC(), DownloadAccess: release.DownloadAccessDisabled,
	}
	for _, apply := range change {
		apply(&track)
	}
	h.store.PutTrack(track)
	return track
}

func (h *harness) revision(t *testing.T, releaseID string) uuid.UUID {
	t.Helper()
	value, ok := h.store.Release(releaseID)
	require.True(t, ok)
	document, ok := h.store.Document(uuid.MustParse(*value.ContentDocumentID))
	require.True(t, ok)
	return document.Revision
}

func requireCode(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), wrapped.Error())
	if message != "" {
		require.Equal(t, message, wrapped.Message())
	}
}

func lastStep(t *testing.T, units *mutationtest.UnitOfWork[release.Store]) mutation.Step {
	t.Helper()
	steps := units.Steps()
	require.NotEmpty(t, steps)
	return steps[len(steps)-1]
}

func auditActions(recorder *audittest.Recorder) []sharedtelemetry.AuditAction {
	var actions []sharedtelemetry.AuditAction
	for _, record := range recorder.Records() {
		actions = append(actions, record.Action)
	}
	return actions
}

func can(t *testing.T, built func(string) (policyv1.Can, error), id string) policyv1.Can {
	t.Helper()
	value, err := built(id)
	require.NoError(t, err)
	return value
}

func text(value string) *string { return &value }

// members is the Member summary fake.
type members struct {
	summaries map[string]*memberref.Summary
}

func (m *members) LoadMemberSummaries(_ context.Context, memberIDs []string) (map[string]*memberref.Summary, error) {
	summaries := map[string]*memberref.Summary{}
	for _, id := range memberIDs {
		if summary := m.summaries[id]; summary != nil {
			summaries[id] = summary
		}
	}
	return summaries, nil
}

// uploads is the File domain's Track upload cleanup fake: cleanup runs
// before each deletion pass and may answer an error or change the store.
type uploads struct {
	mu      sync.Mutex
	cleaned []string
	cleanup func(trackID string) error
}

func (u *uploads) CleanupTrackUploadSessions(_ context.Context, trackID, _ string) error {
	u.mu.Lock()
	u.cleaned = append(u.cleaned, trackID)
	cleanup := u.cleanup
	u.mu.Unlock()
	if cleanup != nil {
		return cleanup(trackID)
	}
	return nil
}

func (u *uploads) cleanedTracks() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.cleaned...)
}

// jobs records the transcode and waveform work cancelled after a deletion.
type jobs struct {
	mu        sync.Mutex
	waveforms [][]string
	files     map[string][]*string
	err       error
}

func (j *jobs) CancelDeletedTracks(_ context.Context, trackIDs []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.waveforms = append(j.waveforms, append([]string(nil), trackIDs...))
	return j.err
}

func (j *jobs) CancelDeletedTrackFiles(_ context.Context, trackID string, fileIDs ...*string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.files == nil {
		j.files = map[string][]*string{}
	}
	j.files[trackID] = fileIDs
}

var errStoreDown = errors.New("store down")
