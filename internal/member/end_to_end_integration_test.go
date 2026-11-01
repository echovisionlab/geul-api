//go:build integration

package member_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/echovisionlab/geul-api/internal/mutation"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	memberadapter "github.com/echovisionlab/geul-api/internal/adapters/member"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/member"
	memberpostgres "github.com/echovisionlab/geul-api/internal/member/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func TestMain(m *testing.M) {
	suite, err := testutil.StartOryIntegrationSuite(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "start member Ory integration suite:", err)
		os.Exit(1)
	}
	testutil.ActivateOryIntegrationSuite(suite)
	code := m.Run()
	testutil.DeactivateOryIntegrationSuite(suite)
	if closeErr := suite.Close(); closeErr != nil && code == 0 {
		fmt.Fprintln(os.Stderr, "cleanup member Ory integration suite:", closeErr)
		code = 1
	}
	os.Exit(code)
}

type unusedWelcomeEmails struct{}

func (unusedWelcomeEmails) PublishSendEmailWithExecutor(context.Context, eventpkg.DBTX, *managev1.SendEmailEvent) error {
	return fmt.Errorf("these tests enqueue no welcome email")
}

func integrationUnits(t *testing.T, db *gorm.DB, identities auth.IdentityGetter) (mutation.UnitOfWork[member.Store], memberadapter.AccountEmailProjection) {
	t.Helper()
	emails := memberadapter.AccountEmailProjection{DB: db, Identity: identities}
	units, err := memberpostgres.NewUnits(db, memberpostgres.Ports{WelcomeEmails: unusedWelcomeEmails{}, WelcomeRecipients: emails})
	require.NoError(t, err)
	return units, emails
}

// seedIntegrationMember stores a Kratos Identity in the shared Kratos, its
// account anchor, an onboarded Member linked to it both ways, and its
// SpiceDB role.
func seedIntegrationMember(t *testing.T, db *gorm.DB, spiceDB *auth.SpiceDBClient, nickname string, role policyv1.RoleID) (string, string) {
	t.Helper()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	email := memberID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: email, Name: nickname})
	require.NoError(t, db.Exec(`UPDATE kratos.identities SET external_id = ?::text WHERE id = ?::uuid`, memberID, identityID).Error)
	require.NoError(t, db.Exec(`INSERT INTO account_identity (id) VALUES (?::uuid)`, identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails, social_links)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text], '{}'::jsonb)
	`, memberID, identityID, nickname, email, email).Error)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = spiceDB.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
	return memberID, identityID
}

// An administrator whose Identity was deactivated or banned keeps a session
// and the SpiceDB role until both catch up. Member administrator mutations
// check the identity fence over the real Kratos rows inside their
// transaction, so such a stale administrator is rejected although the early
// SpiceDB-only check still passes.
func TestMemberAdminMutationsRejectStaleAdministratorIdentityIntegration(t *testing.T) {
	t.Parallel()
	for name, stale := range map[string]string{
		"deactivated identity": `UPDATE kratos.identities SET state = 'inactive' WHERE id = ?::uuid`,
		"banned identity":      `UPDATE kratos.identities SET metadata_admin = '{"banned": true}'::jsonb WHERE id = ?::uuid`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db := testutil.NewIntegrationDB(t)
			spiceDB := testutil.SetupOryStack(t).SpiceDBClient
			adminID, adminIdentityID := seedIntegrationMember(t, db, spiceDB, "admin-"+uuid.NewString(), policyv1.Role.Admin())
			targetID, _ := seedIntegrationMember(t, db, spiceDB, "stale-admin-target", policyv1.Role.User())
			tag := model.UserTag{ID: uuid.NewString(), Name: "existing"}
			require.NoError(t, db.Create(&tag).Error)
			units, _ := integrationUnits(t, db, nil)
			service, err := member.NewService(member.Deps{
				Units: units, SpiceDB: spiceDB, Fence: identitystate.PostgresFence{}, Identity: &fakeIdentity{identities: map[string]*auth.Identity{}},
				AvatarFiles: memberadapter.AvatarFiles{}, Accounts: memberadapter.Accounts{DB: db, SpiceDB: spiceDB},
				Audit: apitelemetry.NewDurableWriter(db),
			})
			require.NoError(t, err)
			handler := member.NewConnectHandler(service)
			require.NoError(t, db.Exec(stale, adminIdentityID).Error)
			ctx := principalContext(t, adminIdentityID, adminID, true)

			_, err = handler.CreateMemberTag(ctx, connect.NewRequest(&managev1.CreateMemberTagRequest{Name: "must not exist"}))
			requireAdminRequired(t, err)
			_, err = handler.DeleteMemberTag(ctx, connect.NewRequest(&managev1.DeleteMemberTagRequest{Id: tag.ID}))
			requireAdminRequired(t, err)
			_, err = handler.SetMemberTags(ctx, connect.NewRequest(&managev1.SetMemberTagsRequest{MemberId: targetID, TagIds: []string{tag.ID}}))
			requireAdminRequired(t, err)
			nickname := "renamed by a stale administrator"
			_, err = handler.UpdateMemberProfile(ctx, connect.NewRequest(&managev1.UpdateMemberProfileRequest{MemberId: targetID, Nickname: &nickname}))
			requireAdminRequired(t, err)

			var tags, mappings, audits int64
			require.NoError(t, db.Model(&model.UserTag{}).Count(&tags).Error)
			require.EqualValues(t, 1, tags)
			require.NoError(t, db.Model(&model.UserTagMapping{}).Count(&mappings).Error)
			require.Zero(t, mappings)
			var stored model.Member
			require.NoError(t, db.Where("id = ?::uuid", targetID).Take(&stored).Error)
			require.Equal(t, "stale-admin-target", stored.Nickname)
			require.NoError(t, db.Table("public.domain_audit").Count(&audits).Error)
			require.Zero(t, audits)
		})
	}
}

func requireAdminRequired(t *testing.T, err error) {
	t.Helper()
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	require.Equal(t, "admin access required", connectErr.Message())
}

type concurrentProvisioningIdentity struct {
	mu       sync.Mutex
	db       *gorm.DB
	identity auth.Identity
}

func (m *concurrentProvisioningIdentity) GetIdentity(_ context.Context, _ string) (*auth.Identity, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy := m.identity
	copy.Traits = map[string]interface{}{}
	for key, value := range m.identity.Traits {
		copy.Traits[key] = value
	}
	return &copy, nil
}

func (m *concurrentProvisioningIdentity) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (m *concurrentProvisioningIdentity) UpdateIdentityExternalID(ctx context.Context, identityID, memberID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.identity.ID != identityID {
		return fmt.Errorf("unexpected identity id %q", identityID)
	}
	if m.identity.ExternalID != "" && m.identity.ExternalID != memberID {
		return context.Canceled
	}
	result := m.db.WithContext(ctx).Exec(`UPDATE kratos.identities SET external_id = ?::text WHERE id = ?::uuid`, memberID, identityID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("identity %s was not found", identityID)
	}
	m.identity.ExternalID = memberID
	return nil
}

// Eight registration hooks of one Identity race on the provisioning
// advisory lock and the Member row lock; they converge on one Member, one
// account anchor and its SpiceDB relationships.
func TestProvisionRegistrationConcurrentHooksConvergeOnOneMemberIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.PrepareOryIntegrationConcurrentTest(t)
	db := stack.DB
	oryStack := testutil.SetupOryStack(t)
	spiceDB := oryStack.SpiceDBClient
	admin := oryStack.CreateUser(t, policyv1.Role.Admin().ID())
	identityID := uuid.NewString()
	memberEmail := "member-" + identityID + "@example.test"
	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (id, external_id, schema_id, traits, state, created_at, updated_at)
		VALUES (?::uuid, NULL, 'user', jsonb_build_object('email', ?::text), 'active', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, identityID, memberEmail).Error)
	identity := &concurrentProvisioningIdentity{db: db, identity: auth.Identity{
		ID: identityID, SchemaID: "user", State: auth.KratosStateActive,
		Traits:              map[string]interface{}{"email": memberEmail},
		VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: memberEmail, Verified: true}},
		Credentials:         map[string]auth.Credential{"code": {Type: "code", Identifiers: []string{memberEmail}}},
	}}
	units, emails := integrationUnits(t, db, identity)
	provisioner := member.NewMemberProvisioner(units, identity, spiceDB, emails, accountadapter.AccountDirectRoleTransition{})
	input := member.RegistrationMemberInput{IdentityID: identityID, Email: memberEmail, PreferredLocale: "ko-KR"}

	const workers = 8
	memberIDs := make(chan string, workers)
	failures := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			provisioned, err := provisioner.ProvisionRegistration(t.Context(), input)
			if err != nil {
				failures <- err
				return
			}
			memberIDs <- provisioned.ID
		}()
	}
	group.Wait()
	close(failures)
	close(memberIDs)
	for err := range failures {
		require.NoError(t, err)
	}
	var expected string
	for memberID := range memberIDs {
		if expected == "" {
			expected = memberID
		}
		require.Equal(t, expected, memberID)
	}
	require.NotEmpty(t, expected)

	var count int64
	require.NoError(t, db.Model(&model.Member{}).Where("account_identity_id = ?", identityID).Count(&count).Error)
	require.Equal(t, int64(1), count)
	var provisioned model.Member
	require.NoError(t, db.Where("id = ?::uuid", expected).Take(&provisioned).Error)
	require.Equal(t, provisioned.ID, provisioned.Nickname)
	require.False(t, provisioned.Onboarded)
	require.Equal(t, "ko", *provisioned.PreferredLocale)
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	role, found, err := spiceDB.ReadDirectGlobalRole(t.Context(), subject)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, policyv1.Role.User(), role)
	adminActor, err := policyv1.NewAccountIdentityActor(admin.IdentityID)
	require.NoError(t, err)
	memberViewCan, err := policyv1.Member.View(expected)
	require.NoError(t, err)
	memberView, err := spiceDB.CheckActorCan(t.Context(), adminActor, memberViewCan)
	require.NoError(t, err)
	require.True(t, memberView)
}
