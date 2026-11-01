package emailauthoring_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/emailauthoring/emailauthoringtest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// harness is the Email Authoring use cases over the in-memory Store.
type harness struct {
	store    *emailauthoringtest.Store
	units    *mutationtest.UnitOfWork[emailauthoring.Store]
	checker  *authztest.Checker
	audit    *audittest.Recorder
	queue    *testEmailQueue
	service  *emailauthoring.Service
	internal *emailauthoring.InternalService
	admin    principal
}

type testEmailQueue struct {
	queued []emailauthoring.TestEmail
	err    error
}

func (q *testEmailQueue) QueueTestEmail(_ context.Context, email emailauthoring.TestEmail) error {
	q.queued = append(q.queued, email)
	return q.err
}

type locales struct{}

func (locales) NormalizeSupportedLocale(value string) *string {
	return localization.NormalizeSupportedLocale(value)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	store, units := emailauthoringtest.New()
	h := &harness{
		store: store, units: units, checker: authztest.AllowAll(),
		audit: &audittest.Recorder{}, queue: &testEmailQueue{}, admin: newPrincipal(t),
	}
	var err error
	h.service, err = emailauthoring.NewService(emailauthoring.Deps{
		Units: units, SpiceDB: h.checker, Fence: &identitystatetest.Fence{}, Audit: h.audit,
		TestEmails: h.queue, Locales: locales{}, SiteOrigin: "https://example.com/",
	})
	require.NoError(t, err)
	h.internal, err = emailauthoring.NewInternalService(emailauthoring.InternalDeps{
		Units: units, SpiceDB: h.checker, Fence: &identitystatetest.Fence{}, Audit: h.audit,
	})
	require.NoError(t, err)
	return h
}

// principal is an authenticated, onboarded Member with the request context
// of a member actor, as the Connect gateway hands it to a use case.
type principal struct {
	identityID string
	memberID   string
	ctx        context.Context
}

func newPrincipal(t *testing.T) principal {
	t.Helper()
	identityID, memberID, sessionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: identityID, MemberID: memberID, SessionID: sessionID,
	})
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(sessionID), Authenticated: true, Onboarded: true,
	})
	return principal{identityID: identityID, memberID: memberID, ctx: ctx}
}

func (p principal) user() *auth.UserInfo { return auth.GetUser(p.ctx) }

// collabContext is the request context editor-collab calls with: its own
// system actor and no principal.
func collabContext(t *testing.T) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"})
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), request)
}

func codeOf(err error) connect.Code { return errs.Wrap(err).Code() }

// checkOf is the Check of a generated descriptor's two results, to compare
// with authztest.Checker.Checks: checkOf(id)(policyv1.EmailTemplate.Edit(templateID)).
func checkOf(identityID string) func(policyv1.Can, error) authztest.Check {
	return func(can policyv1.Can, err error) authztest.Check { return authztest.Of(identityID, can, err) }
}

// wrapped is err as the Connect boundary answers it.
func wrapped(err error) error { return errs.Wrap(err) }

func lastStep(units *mutationtest.UnitOfWork[emailauthoring.Store]) mutation.Step {
	steps := units.Steps()
	return steps[len(steps)-1]
}

func TestNewServiceNamesEveryMissingCollaborator(t *testing.T) {
	t.Parallel()
	_, err := emailauthoring.NewService(emailauthoring.Deps{})
	require.EqualError(t, err, "email authoring dependencies are required: Audit, Fence, Locales, SpiceDB, TestEmails, Units")
	_, err = emailauthoring.NewInternalService(emailauthoring.InternalDeps{})
	require.EqualError(t, err, "internal email authoring dependencies are required: Audit, Fence, SpiceDB, Units")
}

func TestAdministratorUseCasesRequireTheAdminDecision(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.checker = &authztest.Checker{}
	service, err := emailauthoring.NewService(emailauthoring.Deps{
		Units: h.units, SpiceDB: h.checker, Fence: &identitystatetest.Fence{}, Audit: h.audit,
		TestEmails: h.queue, Locales: locales{},
	})
	require.NoError(t, err)
	template := h.store.PutTemplate(emailauthoring.Template{Key: "welcome_note", Name: "Welcome"}, "Hi")

	_, err = service.ListEmailTemplates(h.admin.ctx, emailauthoring.ListEmailTemplatesQuery{})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	_, err = service.CreateEmailTemplate(h.admin.ctx, emailauthoring.CreateEmailTemplateCommand{Key: "new_one", Name: "New", Subject: "S", SourceLocale: "en"})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	require.EqualError(t, errs.Wrap(err), "permission_denied: admin access required")
	err = service.DeleteEmailTemplate(h.admin.ctx, emailauthoring.DeleteEmailTemplateCommand{ID: template.ID})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	_, stored := h.store.Template(template.ID)
	require.True(t, stored)
	require.Empty(t, h.audit.Records())
	require.Contains(t, h.checker.Checks(), checkOf(h.admin.identityID)(policyv1.EmailTemplate.Delete(template.ID)))
}

func TestAnInactiveAdministratorCannotMutate(t *testing.T) {
	t.Parallel()
	store, units := emailauthoringtest.New()
	fence := &identitystatetest.Fence{}
	admin := newPrincipal(t)
	fence.Deactivate(admin.identityID)
	service, err := emailauthoring.NewService(emailauthoring.Deps{
		Units: units, SpiceDB: authztest.AllowAll(), Fence: fence, Audit: &audittest.Recorder{},
		TestEmails: &testEmailQueue{}, Locales: locales{},
	})
	require.NoError(t, err)
	_, err = service.CreateEmailLayout(admin.ctx, emailauthoring.CreateEmailLayoutCommand{
		Name: "Layout", Key: "layout", HTMLContent: "<main>{{content}}</main>", SourceLocale: "en",
	})
	require.Equal(t, connect.CodePermissionDenied, codeOf(err))
	_, total, err := store.ListLayouts(t.Context(), emailauthoring.ListQuery{})
	require.NoError(t, err)
	require.Zero(t, total)
}

func recordActions(records []sharedtelemetry.AuditRecord) []sharedtelemetry.AuditAction {
	actions := make([]sharedtelemetry.AuditAction, 0, len(records))
	for _, record := range records {
		actions = append(actions, record.Action)
	}
	return actions
}

var errDown = errors.New("down")

func requireRolledBack(t *testing.T, units *mutationtest.UnitOfWork[emailauthoring.Store]) {
	t.Helper()
	require.Equal(t, mutation.StepRollback, lastStep(units))
	require.True(t, slices.Contains(units.Steps(), mutation.StepChange))
}
