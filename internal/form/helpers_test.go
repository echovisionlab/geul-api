package form_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/form/formtest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

const (
	sourceSchema = `{"id":"source-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email","type":"email","required":true}]}]}`
	targetSchema = `{"id":"source-schema","steps":[{"id":"step-1","title":"문의","fields":[{"id":"field-email","key":"email","label":"이메일","type":"email","required":true}]}]}`
)

type harness struct {
	store    *formtest.Store
	units    *mutationtest.UnitOfWork[form.Store]
	checker  *authztest.Checker
	fence    *identitystatetest.Fence
	audit    *audittest.Recorder
	service  *form.Service
	forms    *form.ConnectHandler
	internal *form.InternalConnectHandler
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, authztest.AllowAll())
}

func newHarnessWith(t *testing.T, checker *authztest.Checker) *harness {
	t.Helper()
	store, units := formtest.New()
	h := &harness{store: store, units: units, checker: checker, fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{}}
	service, err := form.NewService(form.Deps{
		Units: units, SpiceDB: checker, Fence: h.fence, Audit: h.audit,
		Password: crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}),
	})
	require.NoError(t, err)
	h.service, h.forms, h.internal = service, form.NewConnectHandler(service), form.NewInternalConnectHandler(service)
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

// context is a request of the principal, which the Form audit attributes
// its records to.
func (p principal) context(t *testing.T) context.Context {
	t.Helper()
	sessionID := uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{
		IdentityID: p.identityID, MemberID: p.memberID, SessionID: sessionID,
	})
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(p.identityID), MemberID: auth.MemberID(p.memberID), SessionID: auth.SessionID(sessionID),
		Authenticated: true, Onboarded: true,
	})
}

// accessContext is a request of the principal from a known address, which a
// personal data access record names.
func (p principal) accessContext(t *testing.T) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPublicRequestContext("198.51.100.23")
	require.NoError(t, err)
	return auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(p.identityID), MemberID: auth.MemberID(p.memberID), SessionID: auth.SessionID(uuid.NewString()),
		Authenticated: true, Onboarded: true,
	})
}

// collabContext is a request editor-collab makes as itself.
func collabContext(t *testing.T) context.Context {
	t.Helper()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.SystemActor{ServiceName: "geul-collab"})
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), request)
}

// seedForm stores a draft Form with an English source and a Korean target
// locale.
func (h *harness) seedForm(t *testing.T, change func(*form.Form)) form.Form {
	t.Helper()
	at := time.Unix(1_700_100_000, 0).UTC()
	value := form.Form{SourceLocale: "en", Status: form.StatusDraft, CreatedAt: at, UpdatedAt: &at}
	if change != nil {
		change(&value)
	}
	stored := h.store.PutForm(value)
	h.store.PutLocale(stored.ID, "en", formtest.StoredLocale{Title: new("Contact"), ContentJSON: []byte(sourceSchema), ContentText: new("Contact\nEmail"), UpdatedAt: at})
	h.store.PutLocale(stored.ID, "ko", formtest.StoredLocale{Title: new("문의 양식"), ContentJSON: []byte(targetSchema), ContentText: new("문의\n이메일"), UpdatedAt: at})
	return stored
}

func requireCode(t *testing.T, err error, code connect.Code, message string) {
	t.Helper()
	require.Error(t, err)
	wrapped := errs.Wrap(err)
	require.Equal(t, code, wrapped.Code(), err.Error())
	if message != "" {
		require.Equal(t, message, wrapped.Message())
	}
}

func lastStep(units *mutationtest.UnitOfWork[form.Store]) mutation.Step {
	steps := units.Steps()
	return steps[len(steps)-1]
}
