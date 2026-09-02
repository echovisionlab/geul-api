//go:build integration

package account

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestAccountNewsletterSubscriptionAuditRecordsOnlyCommittedTransitionsIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	member := stack.CreateUser(t, policyv1.Role.User().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		apitelemetry.NewDurableWriter(stack.DB),
		WithNewsletterSubscription(memberNewsletterSubscriptionIntegration{}),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)
	memberCtx := auditedOryMemberContext(t, member)
	adminCtx := auditedOryMemberContext(t, admin)

	request := connect.NewRequest(&managev1.SetMyNewsletterSubscriptionRequest{Subscribed: true})
	_, err := service.SetMyNewsletterSubscription(memberCtx, request)
	require.NoError(t, err)
	_, err = service.SetMyNewsletterSubscription(memberCtx, request)
	require.NoError(t, err)
	_, err = service.UnsubscribeAccountFromNewsletter(adminCtx, connect.NewRequest(&managev1.UnsubscribeAccountFromNewsletterRequest{MemberId: member.MemberID}))
	require.NoError(t, err)
	_, err = service.UnsubscribeAccountFromNewsletter(adminCtx, connect.NewRequest(&managev1.UnsubscribeAccountFromNewsletterRequest{MemberId: member.MemberID}))
	require.NoError(t, err)

	var rows []struct {
		ActorMemberID string `gorm:"column:actor_member_id"`
		Attributes    []byte `gorm:"column:attributes"`
	}
	require.NoError(t, stack.DB.Raw(`
		SELECT actor_member_id::text AS actor_member_id, attributes
		FROM public.domain_audit
		WHERE action = ? AND target_type = 'account' AND target_id = ?
		ORDER BY occurred_at, audit_id
	`, sharedtelemetry.AuditAccountUpdated, member.MemberID).Scan(&rows).Error)
	require.Len(t, rows, 2)
	require.Equal(t, member.MemberID, rows[0].ActorMemberID)
	require.Equal(t, admin.MemberID, rows[1].ActorMemberID)
	for index, transition := range [][2]string{{"unsubscribed", "subscribed"}, {"subscribed", "unsubscribed"}} {
		attributes := map[string]any{}
		require.NoError(t, json.Unmarshal(rows[index].Attributes, &attributes))
		require.Equal(t, []any{"newsletter_subscription"}, attributes["changed_fields"])
		require.Equal(t, transition[0], attributes["previous_state"])
		require.Equal(t, transition[1], attributes["new_state"])
		require.Len(t, attributes, 3)
	}
}

func TestAccountNewsletterSubscriptionAuditFailureRollsBackIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	member := stack.CreateUser(t, policyv1.Role.User().ID())
	service := NewAuditedAccountService(
		stack.DB,
		stack.KratosClient,
		stack.SpiceDBClient,
		"https://www.example.test",
		accountRoleNoopLifecyclePublisher{},
		failingDomainAuditAppender{},
		WithNewsletterSubscription(memberNewsletterSubscriptionIntegration{}),
		WithMemberEmailProjection(memberEmailProjectionIntegration{}),
	)

	_, err := service.SetMyNewsletterSubscription(
		auditedOryMemberContext(t, member),
		connect.NewRequest(&managev1.SetMyNewsletterSubscriptionRequest{Subscribed: true}),
	)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	state, stateErr := newsletterSubscriptionState(t.Context(), stack.DB, member.IdentityID)
	require.NoError(t, stateErr)
	require.False(t, state.GetSubscribed())
}
