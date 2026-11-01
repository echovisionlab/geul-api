//go:build integration

package form_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/form"
	formpostgres "github.com/echovisionlab/geul-api/internal/form/postgres"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/securityaccess"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests keep what only the whole stack shows: the Form policy
// relationships SpiceDB holds and their compensation, and authority
// rechecked over real Kratos rows after waiting on the Form root lock. Every
// other Form scenario runs on the fake Store, and the form tables on the
// Store contract.

func TestMain(m *testing.M) {
	flag.Parse()
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "start Form integration suite: %v\n", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if err := suite.Close(); err != nil && code == 0 {
		fmt.Fprintf(os.Stderr, "close Form integration suite: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

// newStackForms composes the Form use cases the server composes over db;
// audit appends the domain audit.
func newStackForms(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, audit stackAudit) *form.ConnectHandler {
	t.Helper()
	cdnDomain := "https://cdn.example.test"
	contents, err := contentblock.NewGeneratedStore(noFileReuse{})
	require.NoError(t, err)
	planner := og.NewPlanner(db, cdnDomain, renderConfig{}, formogadapter.NewProjection())
	units, err := formpostgres.NewUnits(db, formpostgres.Ports{
		ContentBlocks: contents, Signals: &mq.Publisher{}, Assets: formadapter.NewAssets(cdnDomain),
		OG:     formogadapter.NewOG(cdnDomain, og.NewRefresher(planner, og.NewResolver(formogadapter.NewRequests()))),
		Routes: formadapter.NewRoutes(), Collaboration: formadapter.NewCollaboration(spiceDB),
		Members: memberadapter.PreferredLocales{}, ShareLinks: sharelinkadapter.FormLinks{},
	})
	require.NoError(t, err)
	service, err := form.NewService(form.Deps{
		Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Audit: audit,
		Password: crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16}),
	})
	require.NoError(t, err)
	return form.NewConnectHandler(service)
}

func TestFormPolicyRelationshipsFollowTheFormIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	principal := auth.GetUser(adminCtx)
	require.NotNil(t, principal)
	identityID := principal.IdentityID.String()
	ctx := testutil.NewAuditContext(t, identityID, principal.MemberID.String())
	writer := apitelemetry.NewDurableWriter(db)
	forms := newStackForms(t, db, spiceDB, stackAudit{security: writer, audit: writer})

	created, err := forms.CreateForm(ctx, connect.NewRequest(&managev1.CreateFormRequest{Title: "Policy Form", Schema: stackSchema()}))
	require.NoError(t, err)
	formID := created.Msg.Id
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.Form.Manage, formID, identityID, true)

	// A deletion whose audit fails rolls the row back and restores the
	// policy it removed.
	failing := newStackForms(t, db, spiceDB, stackAudit{security: writer, audit: failingAudit{}})
	_, err = failing.DeleteForm(ctx, connect.NewRequest(&managev1.DeleteFormRequest{Id: formID}))
	require.Error(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.Form.Manage, formID, identityID, true)
	_, err = forms.GetForm(ctx, connect.NewRequest(&managev1.GetFormRequest{Id: formID}))
	require.NoError(t, err)

	_, err = forms.DeleteForm(ctx, connect.NewRequest(&managev1.DeleteFormRequest{Id: formID}))
	require.NoError(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.Form.Manage, formID, identityID, false)
	var remaining int64
	require.NoError(t, db.Table("form").Where("id = ?", formID).Count(&remaining).Error)
	require.Zero(t, remaining)
}

func TestFormAdminMutationRechecksAuthorityAfterRootLockIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	principal := auth.GetUser(adminCtx)
	require.NotNil(t, principal)
	ctx := testutil.NewAuditContext(t, principal.IdentityID.String(), principal.MemberID.String())
	ctx = auth.WithUser(ctx, principal)
	writer := apitelemetry.NewDurableWriter(db)
	forms := newStackForms(t, db, spiceDB, stackAudit{security: writer, audit: writer})

	slug := "authority-fence-" + testutil.IntegrationUUID()[:8]
	created, err := forms.CreateForm(ctx, connect.NewRequest(&managev1.CreateFormRequest{
		Title: "Authority fence Form", Slug: &slug, Schema: stackSchema(),
	}))
	require.NoError(t, err)

	lockTx := db.Begin()
	require.NoError(t, lockTx.Error)
	t.Cleanup(func() { _ = lockTx.Rollback().Error })
	require.NoError(t, lockTx.Exec("SELECT id FROM form WHERE id = ?::uuid FOR UPDATE", created.Msg.Id).Error)

	result := make(chan error, 1)
	go func() {
		isPublic := true
		_, updateErr := forms.UpdateForm(ctx, connect.NewRequest(&managev1.UpdateFormRequest{Id: created.Msg.Id, IsPublic: &isPublic}))
		result <- updateErr
	}()
	testutil.RequireLockWaiters(t, db, 1)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, principal.IdentityID.String(), policyv1.Role.User())
	require.NoError(t, lockTx.Commit().Error)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(<-result))

	var isPublic bool
	require.NoError(t, db.Table("form").Select("is_public").Where("id = ?", created.Msg.Id).Scan(&isPublic).Error)
	require.False(t, isPublic)
}

func stackSchema() []byte {
	return []byte(`{"id":"schema-1","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email address","type":"email"}]}]}`)
}

// stackAudit records personal data access through security and the Form
// domain audit through audit.
type stackAudit struct {
	security securityaccess.Appender
	audit    interface {
		AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error
	}
}

func (a stackAudit) AppendSecurityAccess(ctx context.Context, record sharedtelemetry.SecurityAccessRecord) error {
	return a.security.AppendSecurityAccess(ctx, record)
}

func (a stackAudit) AppendDomainAuditInTransaction(ctx context.Context, tx *gorm.DB, record sharedtelemetry.AuditRecord) error {
	return a.audit.AppendDomainAuditInTransaction(ctx, tx, record)
}

type failingAudit struct{}

func (failingAudit) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("form audit unavailable")
}

type renderConfig struct{}

func (renderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":"","primary_color":"#b02d23"}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

type noFileReuse struct{}

func (noFileReuse) AuthorizeFileReuse(context.Context, *gorm.DB, contentblock.Document, contentblock.FullBlock, contentblock.FileReference, contentblock.File) error {
	return nil
}
