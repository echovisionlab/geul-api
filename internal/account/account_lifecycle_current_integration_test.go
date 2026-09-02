//go:build integration

package account

import (
	"context"
	"net/url"
	"testing"
	"time"

	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type lifecycleIdentityManager struct {
	auth.IdentityManager
	db           *gorm.DB
	identity     *auth.Identity
	stateUpdates []string
	sessionCalls int
}

func (m *lifecycleIdentityManager) GetIdentity(_ context.Context, identityID string) (*auth.Identity, error) {
	if m.identity == nil || m.identity.ID != identityID {
		return nil, gorm.ErrRecordNotFound
	}
	return m.identity, nil
}

func (m *lifecycleIdentityManager) GetIdentityWithIncludeCredential(ctx context.Context, identityID, _ string) (*auth.Identity, error) {
	return m.GetIdentity(ctx, identityID)
}

func (m *lifecycleIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	return m.identity.CurrentEmail(), nil
}

func (m *lifecycleIdentityManager) SetIdentityState(ctx context.Context, identityID, state string) error {
	if _, err := m.GetIdentity(ctx, identityID); err != nil {
		return err
	}
	m.identity.State = state
	m.stateUpdates = append(m.stateUpdates, state)
	return m.db.WithContext(ctx).Table("kratos.identities").Where("id = ?::uuid", identityID).Update("state", state).Error
}

func (m *lifecycleIdentityManager) DeleteIdentitySessions(context.Context, string) error {
	m.sessionCalls++
	return nil
}

type lifecycleEmailPublisher struct{ jobs []*managev1.SendEmailEvent }

func (p *lifecycleEmailPublisher) PublishSendEmail(_ context.Context, job *managev1.SendEmailEvent) error {
	p.jobs = append(p.jobs, job)
	return nil
}

type lifecycleDeletionPublisher struct {
	commands []*managev1.UserDeleteIdentityCommand
}

func (p *lifecycleDeletionPublisher) PublishUserDeleteIdentity(_ context.Context, command *managev1.UserDeleteIdentityCommand) error {
	p.commands = append(p.commands, command)
	return nil
}

func (p *lifecycleDeletionPublisher) PublishUserDeleteAvatar(_ context.Context, _ *managev1.UserDeleteAvatarCommand) error {
	return nil
}

func TestAccountLifecycleDeletionCanCancelAndRecoverCurrentPair(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		db, manager, spiceDB, memberID, identityID, email := setupAccountLifecyclePair(t, policyv1.Role.User())
		publisher := &lifecycleEmailPublisher{}
		lifecycle := NewAuditedAccountLifecycleService(
			db, manager, spiceDB, "https://www.example.test", publisher, apitelemetry.NewDurableWriter(db), WithLifecycleMemberDeletion(memberDeletionIntegrationAdapter{}), WithLifecycleMemberEmailProjection(memberEmailProjectionIntegration{}),
		)
		ctx := accountLifecycleAuditContext(t, memberID)

		_, err := lifecycle.RequestDeletion(ctx, memberID, identityID)
		require.NoError(t, err)
		require.Len(t, publisher.jobs, 1)
		confirmToken := lifecycleURLToken(t, publisher.jobs[0].GetTemplateData()["confirm_url"])
		_, err = lifecycle.ConfirmDeletion(ctx, confirmToken)
		require.NoError(t, err)
		require.Equal(t, auth.KratosStateInactive, manager.identity.State)
		require.Equal(t, 1, manager.sessionCalls)

		cancelToken := lifecycleURLToken(t, publisher.jobs[1].GetTemplateData()["cancel_url"])
		require.NoError(t, lifecycle.CancelDeletion(ctx, cancelToken))
		require.Equal(t, auth.KratosStateActive, manager.identity.State)
		var request model.UserDeletionRequest
		require.NoError(t, db.Where("member_id = ?::uuid", memberID).Take(&request).Error)
		require.Equal(t, accountLifecycleStateCancelled, request.LifecycleState)
		require.Nil(t, request.ScheduledAt)
		require.Equal(t, email, *request.NotificationEmail)
		requireAccountDeletionAuditTransitions(t, db, memberID, [][2]sharedtelemetry.AuditState{
			{sharedtelemetry.AuditStateNone, sharedtelemetry.AuditStateConfirmationPending},
			{sharedtelemetry.AuditStateConfirmationPending, sharedtelemetry.AuditStateScheduled},
			{sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateCancelled},
		})
	})

	t.Run("recover", func(t *testing.T) {
		db, manager, spiceDB, memberID, identityID, email := setupAccountLifecyclePair(t, policyv1.Role.User())
		publisher := &lifecycleEmailPublisher{}
		lifecycle := NewAuditedAccountLifecycleService(
			db, manager, spiceDB, "https://www.example.test", publisher, apitelemetry.NewDurableWriter(db), WithLifecycleMemberDeletion(memberDeletionIntegrationAdapter{}), WithLifecycleMemberEmailProjection(memberEmailProjectionIntegration{}),
		)
		ctx := accountLifecycleAuditContext(t, memberID)
		_, err := lifecycle.RequestDeletion(ctx, memberID, identityID)
		require.NoError(t, err)
		_, err = lifecycle.ConfirmDeletion(ctx, lifecycleURLToken(t, publisher.jobs[0].GetTemplateData()["confirm_url"]))
		require.NoError(t, err)

		require.NoError(t, lifecycle.RequestRecovery(ctx, email))
		recoveryToken := lifecycleURLToken(t, publisher.jobs[2].GetTemplateData()["confirm_url"])
		require.NoError(t, lifecycle.ConfirmRecovery(ctx, recoveryToken))
		require.Equal(t, auth.KratosStateActive, manager.identity.State)
		var request model.UserDeletionRequest
		require.NoError(t, db.Where("member_id = ?::uuid", memberID).Take(&request).Error)
		require.Equal(t, accountLifecycleStateRecovered, request.LifecycleState)
		require.Nil(t, request.ScheduledAt)
		requireAccountDeletionAuditTransitions(t, db, memberID, [][2]sharedtelemetry.AuditState{
			{sharedtelemetry.AuditStateNone, sharedtelemetry.AuditStateConfirmationPending},
			{sharedtelemetry.AuditStateConfirmationPending, sharedtelemetry.AuditStateScheduled},
			{sharedtelemetry.AuditStateRecoveryConfirmationPending, sharedtelemetry.AuditStateRecovered},
		})
	})
}

func accountLifecycleAuditContext(t *testing.T, memberID string) context.Context {
	t.Helper()
	requestContext, err := sharedtelemetry.NewPropagatedRequestContext(
		uuid.NewString(), sharedtelemetry.MemberActor{MemberID: memberID},
	)
	require.NoError(t, err)
	return sharedtelemetry.WithRequestContext(t.Context(), requestContext)
}

func requireAccountDeletionAuditTransitions(
	t *testing.T,
	db *gorm.DB,
	memberID string,
	want [][2]sharedtelemetry.AuditState,
) {
	t.Helper()
	var records []struct {
		Action        string
		ActorKind     string
		ActorMemberID string
		ChangedFields pq.StringArray `gorm:"type:text[]"`
		PreviousState string
		NewState      string
	}
	require.NoError(t, db.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id,
		       ARRAY(SELECT jsonb_array_elements_text(attributes->'changed_fields')) AS changed_fields,
		       attributes->>'previous_state' AS previous_state,
		       attributes->>'new_state' AS new_state
		FROM public.domain_audit
		WHERE target_type = 'account' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, memberID).Scan(&records).Error)
	require.Len(t, records, len(want))
	for index, transition := range want {
		require.Equal(t, string(sharedtelemetry.AuditAccountUpdated), records[index].Action)
		require.Equal(t, string(sharedtelemetry.ActorKindMember), records[index].ActorKind)
		require.Equal(t, memberID, records[index].ActorMemberID)
		require.Equal(t, pq.StringArray{"deletion_state"}, records[index].ChangedFields)
		require.Equal(t, string(transition[0]), records[index].PreviousState)
		require.Equal(t, string(transition[1]), records[index].NewState)
	}
}

func TestScheduleImmediateUserDeletionRejectsLastActiveAdmin(t *testing.T) {
	db, manager, spiceDB, memberID, identityID, _ := setupAccountLifecyclePair(t, policyv1.Role.Admin())
	publisher := &lifecycleDeletionPublisher{}

	err := scheduleImmediateUserDeletion(t.Context(), db, manager, publisher, spiceDB, memberDeletionIntegrationAdapter{}, memberID, identityID, nil, memberEmailProjectionIntegration{})

	require.ErrorIs(t, err, ErrLastActiveAdminDeletion)
	require.Empty(t, publisher.commands)
	require.Equal(t, auth.KratosStateActive, manager.identity.State)
}

func TestRequestDeletionSynchronizesRemovedPrimaryBeforeSnapshot(t *testing.T) {
	db, manager, spiceDB, memberID, identityID, _ := setupAccountLifecyclePair(t, policyv1.Role.User())
	fallbackEmail := "fallback-" + identityID + "@example.test"
	manager.identity.Traits = map[string]interface{}{"email": fallbackEmail}
	manager.identity.VerifiableAddresses = []auth.VerifiableAddress{{
		Via: "email", Value: fallbackEmail, Verified: true,
	}}
	manager.identity.Credentials = map[string]auth.Credential{
		"code": {Type: "code", Identifiers: []string{fallbackEmail}},
	}
	publisher := &lifecycleEmailPublisher{}
	lifecycle := NewAccountLifecycleService(db, manager, spiceDB, "https://www.example.test", publisher, WithLifecycleMemberDeletion(memberDeletionIntegrationAdapter{}), WithLifecycleMemberEmailProjection(memberEmailProjectionIntegration{}))

	_, err := lifecycle.RequestDeletion(t.Context(), memberID, identityID)
	require.NoError(t, err)
	require.Len(t, publisher.jobs, 1)
	require.Equal(t, fallbackEmail, publisher.jobs[0].GetRecipient())

	var request model.UserDeletionRequest
	require.NoError(t, db.Where("member_id = ?::uuid", memberID).Take(&request).Error)
	require.Equal(t, fallbackEmail, *request.NotificationEmail)
	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Equal(t, fallbackEmail, *member.PrimaryEmail)
	require.Equal(t, []string{fallbackEmail}, []string(member.AvailableEmails))
}

func setupAccountLifecyclePair(t *testing.T, role policyv1.RoleID) (*gorm.DB, *lifecycleIdentityManager, *auth.SpiceDBClient, string, string, string) {
	t.Helper()
	stack := testutil.SetupOryStack(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	email := "lifecycle-" + identityID + "@example.test"
	name := "Lifecycle member"
	testutil.SeedKratosIdentityFixture(t, stack.DB, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, Name: name, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, stack.DB.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, stack.DB.Exec(
		"INSERT INTO account_identity (id) VALUES (?::uuid)",
		identityID,
	).Error)
	require.NoError(t, stack.DB.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, string_to_array(?, ','))
	`, memberID, identityID, name, email, email).Error)
	identity := &auth.Identity{
		ID: identityID, ExternalID: memberID, State: auth.KratosStateActive,
		Traits: map[string]interface{}{"email": email},
		Credentials: map[string]auth.Credential{
			"code": {Type: "code", Identifiers: []string{email}},
		},
		VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: email, Verified: true}},
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), subject, role)
	require.NoError(t, err)
	return stack.DB, &lifecycleIdentityManager{db: stack.DB, identity: identity}, stack.SpiceDBClient, memberID, identityID, email
}

func lifecycleURLToken(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	token := parsed.Query().Get("token")
	require.NotEmpty(t, token)
	return token
}
