package post_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/echovisionlab/geul-api/internal/post"
	"github.com/echovisionlab/geul-api/internal/post/posttest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is one Post use-case test: the services over the fake store, its
// unit of work and the fakes of every other port.
type harness struct {
	service  *post.Service
	comments *post.CommentService
	store    *posttest.Store
	units    *mutationtest.UnitOfWork[post.Store]
	checker  *posttest.Authorizer
	fence    *identitystatetest.Fence
	audit    *audittest.Recorder
	files    *files
	members  *members
}

type harnessOption func(*harness)

func withAudit(recorder *audittest.Recorder) harnessOption {
	return func(h *harness) { h.audit = recorder }
}

func withChecker(checker *authztest.Checker) harnessOption {
	return func(h *harness) { h.checker = posttest.NewAuthorizer(checker) }
}

func newHarness(t *testing.T, options ...harnessOption) *harness {
	t.Helper()
	store, units := posttest.New()
	h := &harness{
		store: store, units: units, checker: posttest.NewAuthorizer(authztest.AllowAll()),
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, files: newFiles(), members: newMembers(),
	}
	for _, option := range options {
		option(h)
	}
	service, err := post.NewService(post.Deps{
		Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Files: h.files, Members: h.members,
	})
	require.NoError(t, err)
	comments, err := post.NewCommentService(post.CommentDeps{
		Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Members: h.members,
	})
	require.NoError(t, err)
	h.service, h.comments = service, comments
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

// member stores p as an active Member with a nickname.
func (h *harness) member(p principal, nickname string) principal {
	h.store.PutMember(p.memberID, posttest.Member{IdentityID: p.identityID, Nickname: nickname, Active: true})
	h.members.put(p.memberID, nickname)
	return p
}

// seedPost stores a Post titled title, authored by author when set.
func (h *harness) seedPost(title string, author string, change ...func(*post.Post)) post.Post {
	value := post.Post{}
	for _, apply := range change {
		apply(&value)
	}
	return h.store.PutPost(value, title, author)
}

func published(value *post.Post) {
	publishedAt := time.Now().Add(-time.Hour)
	value.Status, value.PublishedAt, value.CommentsEnabled = post.StatusPublished, &publishedAt, true
}

// requireCode checks err's Connect code and, when set, its message; why
// says what the case shows.
func requireCode(t *testing.T, err error, code connect.Code, message string, why ...string) {
	t.Helper()
	require.Error(t, err, why)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), "%s %v", wrapped.Error(), why)
	if message != "" {
		require.Equal(t, message, wrapped.Message())
	}
}

func lastStep(t *testing.T, units *mutationtest.UnitOfWork[post.Store]) mutation.Step {
	t.Helper()
	steps := units.Steps()
	require.NotEmpty(t, steps)
	return steps[len(steps)-1]
}

var errDown = errors.New("dependency down")

// members is the Member summary fake.
type members struct {
	mu        sync.Mutex
	summaries map[string]*commonv1.MemberSummary
	err       error
}

func newMembers() *members {
	return &members{summaries: map[string]*commonv1.MemberSummary{}}
}

func (m *members) put(memberID, nickname string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.summaries[memberID] = &commonv1.MemberSummary{Id: memberID, Nickname: nickname}
}

func (m *members) LoadMemberSummaries(_ context.Context, memberIDs []string) (map[string]*commonv1.MemberSummary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	summaries := map[string]*commonv1.MemberSummary{}
	for _, memberID := range memberIDs {
		if summary, ok := m.summaries[memberID]; ok {
			summaries[memberID] = summary
		}
	}
	return summaries, nil
}

// files is the File service fake: it resolves the deliveries it knows and
// records the Files it deletes.
type files struct {
	mu         sync.Mutex
	deliveries map[string]*commonv1.MediaDelivery
	deleted    []string
	err        error
}

func newFiles() *files { return &files{deliveries: map[string]*commonv1.MediaDelivery{}} }

func (f *files) put(fileID string) *commonv1.MediaDelivery {
	f.mu.Lock()
	defer f.mu.Unlock()
	delivery := &commonv1.MediaDelivery{FileId: fileID}
	f.deliveries[fileID] = delivery
	return delivery
}

func (f *files) DeleteFileByID(_ context.Context, fileID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, fileID)
	return f.err
}

func (f *files) ResolveAuthorizedPostFeaturedImage(_ context.Context, _ string, fileID string) (*commonv1.MediaDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.deliveries[fileID], nil
}

func (f *files) ResolvePublicDisplayMedia(_ context.Context, fileIDs []string) (map[string]*commonv1.MediaDelivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	deliveries := map[string]*commonv1.MediaDelivery{}
	for _, fileID := range fileIDs {
		if delivery, ok := f.deliveries[fileID]; ok {
			deliveries[fileID] = delivery
		}
	}
	return deliveries, nil
}
