package page_test

import (
	"context"
	"testing"

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
	"github.com/echovisionlab/geul-api/internal/page"
	"github.com/echovisionlab/geul-api/internal/page/pagetest"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is one Page use-case test: the service over the fake store, its
// unit of work and the fakes of every other port.
type harness struct {
	service *page.Service
	store   *pagetest.Store
	units   *mutationtest.UnitOfWork[page.Store]
	checker *pagetest.Authorizer
	fence   *identitystatetest.Fence
	audit   *audittest.Recorder
	files   *featuredImages
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, units := pagetest.New()
	h := &harness{
		store: store, units: units, checker: &pagetest.Authorizer{Checker: authztest.AllowAll()},
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}, files: &featuredImages{},
	}
	store.Fence = h.fence
	service, err := page.NewService(page.Deps{Units: units, SpiceDB: h.checker, Fence: h.fence, Audit: h.audit, Files: h.files})
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
	requestContext, err := sharedtelemetry.NewPublicRequestContext("192.0.2.80")
	require.NoError(t, err)
	ctx := sharedtelemetry.WithRequestContext(t.Context(), requestContext)
	ctx = sharedtelemetry.WithActor(ctx, sharedtelemetry.MemberActor{IdentityID: p.identityID, MemberID: p.memberID, SessionID: uuid.NewString()})
	return auth.WithUser(ctx, p.user())
}

// member stores p as a Member with an active Identity.
func (h *harness) member(p principal) principal {
	h.store.PutMember(p.memberID, "Member "+p.memberID[:8], p.identityID)
	return p
}

// seedPage stores a draft Page with its source title and document.
func (h *harness) seedPage(title string, change ...func(*page.Page)) page.Page {
	value := page.Page{}
	for _, apply := range change {
		apply(&value)
	}
	return h.store.PutPage(value, title)
}

func (h *harness) documentID(t *testing.T, pageID string) uuid.UUID {
	t.Helper()
	value, ok := h.store.Page(pageID)
	require.True(t, ok)
	return uuid.MustParse(*value.ContentDocumentID)
}

func (h *harness) revision(t *testing.T, pageID string) uuid.UUID {
	t.Helper()
	document, ok := h.store.Document(h.documentID(t, pageID))
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

func lastStep(t *testing.T, units *mutationtest.UnitOfWork[page.Store]) mutation.Step {
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

// featuredImages delivers every featured image it is asked for and records
// the requests.
type featuredImages struct {
	requests []string
}

func (f *featuredImages) ResolveAuthorizedPageFeaturedImage(_ context.Context, pageID, fileID string) (*commonv1.MediaDelivery, error) {
	f.requests = append(f.requests, pageID+":"+fileID)
	if fileID == "" {
		return nil, nil
	}
	return &commonv1.MediaDelivery{FileId: fileID}, nil
}

func text(value string) *string { return &value }
