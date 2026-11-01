package menu_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/identitystate/identitystatetest"
	"github.com/echovisionlab/geul-api/internal/menu"
	"github.com/echovisionlab/geul-api/internal/menu/menutest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// menuFixture is the Menu use cases on the fakes, called by one member.
type menuFixture struct {
	service    *menu.Service
	store      *menutest.Store
	units      *mutationtest.UnitOfWork[menu.Store]
	checker    *authztest.Checker
	fence      *identitystatetest.Fence
	audit      *audittest.Recorder
	ctx        context.Context
	identityID string
	memberID   string
}

// newMenuFixture lets the member do everything; a test denies what it needs.
func newMenuFixture(t *testing.T) *menuFixture {
	t.Helper()
	store, units := menutest.New()
	f := &menuFixture{
		store: store, units: units, checker: authztest.AllowAll(),
		fence: &identitystatetest.Fence{}, audit: &audittest.Recorder{},
	}
	f.ctx, f.identityID, f.memberID = memberContext(t)
	f.service = f.newService(t, f.audit)
	return f
}

// newService returns the use cases over the same fakes with another audit
// appender.
func (f *menuFixture) newService(t *testing.T, audit *audittest.Recorder) *menu.Service {
	t.Helper()
	service, err := menu.NewService(menu.Deps{Units: f.units, SpiceDB: f.checker, Fence: f.fence, Audit: audit})
	require.NoError(t, err)
	return service
}

// memberContext is an authenticated member request with a request context,
// so mutations attribute their audit records to the member.
func memberContext(t *testing.T) (context.Context, string, string) {
	t.Helper()
	identityID, memberID := uuid.NewString(), uuid.NewString()
	request, err := sharedtelemetry.NewPropagatedRequestContext(uuid.NewString(), sharedtelemetry.MemberActor{IdentityID: identityID, MemberID: memberID, SessionID: uuid.NewString()})
	require.NoError(t, err)
	ctx := auth.WithUser(sharedtelemetry.WithRequestContext(t.Context(), request), &auth.UserInfo{
		IdentityID: auth.IdentityID(identityID), MemberID: auth.MemberID(memberID),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
	return ctx, identityID, memberID
}

// createMenu creates a Menu of custom items through the use case and
// returns it with its content document revision.
func (f *menuFixture) createMenu(t *testing.T, name string, items ...menu.Item) (menu.Menu, string) {
	t.Helper()
	created, err := f.service.CreateMenu(f.ctx, menu.CreateMenuCommand{Name: name, Items: items})
	require.NoError(t, err)
	return created, f.revision(t, created.ID)
}

// revision is the stored content document revision of a Menu.
func (f *menuFixture) revision(t *testing.T, menuID string) string {
	t.Helper()
	root, ok := f.store.Stored(menuID)
	require.True(t, ok)
	document, ok := f.store.StoredDocument(root.ContentDocumentID)
	require.True(t, ok)
	return document.Revision
}

// labels are the stored labels of one locale; nil when it has no row.
func (f *menuFixture) labels(t *testing.T, menuID, locale string) map[string]string {
	t.Helper()
	values, ok := f.store.StoredLocale(menuID, locale)
	if !ok {
		return nil
	}
	return values.Labels
}

// lastSteps are the steps of the last transaction.
func (f *menuFixture) lastSteps() []mutation.Step {
	steps := f.units.Steps()
	for i := len(steps) - 2; i >= 0; i-- {
		if steps[i] == mutation.StepCommit || steps[i] == mutation.StepRollback {
			return steps[i+1:]
		}
	}
	return steps
}

func customItem(id, url string) menu.Item {
	return menu.Item{ID: id, Label: id, LinkType: menu.LinkCustom, URL: &url}
}

func stringPointer(value string) *string { return &value }
