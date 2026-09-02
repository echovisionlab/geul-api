package work

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type workAuthorizationRecordingChecker struct {
	calls    int
	decision policyv1.AuthorizationDecision
}

func (c *workAuthorizationRecordingChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.calls++
	c.decision = decision
	return true, nil
}

func (c *workAuthorizationRecordingChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return true, nil
}

func TestWorkLifecyclePermissionSelectsExactSinglePermission(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status string
		normal workAction
		use    workAuthorizationUse
		want   workAction
	}{
		{name: "draft publish", status: managev1.WorkStatus_WORK_STATUS_DRAFT.String(), normal: policyv1.Work.Publish, use: workAuthorizationMutation, want: policyv1.Work.Publish},
		{name: "archived editor read", status: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), normal: policyv1.Work.Edit, use: workAuthorizationRead, want: policyv1.Work.ViewArchived},
		{name: "archived publish mutation", status: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), normal: policyv1.Work.Publish, use: workAuthorizationMutation, want: policyv1.Work.EditArchived},
		{name: "archived manage mutation", status: managev1.WorkStatus_WORK_STATUS_ARCHIVED.String(), normal: policyv1.Work.Manage, use: workAuthorizationMutation, want: policyv1.Work.EditArchived},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			const workID = "33333333-3333-4333-8333-333333333333"
			got, err := workLifecycleAction(test.status, test.normal, test.use)(workID)
			if err != nil {
				t.Fatal(err)
			}
			want, err := test.want(workID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Action().Permission() != want.Action().Permission() {
				t.Fatalf("permission = %q, want %q", got.Action().Permission(), want.Action().Permission())
			}
		})
	}
}

func TestRequireWorkPermissionForSubjectUsesOneExactObjectDecision(t *testing.T) {
	t.Parallel()
	workID := "33333333-3333-4333-8333-333333333333"
	ctx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("44444444-4444-4444-8444-444444444444"),
		SessionID:     auth.SessionID("55555555-5555-4555-8555-555555555555"),
		Authenticated: true,
	})
	checker := &workAuthorizationRecordingChecker{}
	want, err := policyv1.Work.EditArchived(workID)
	if err != nil {
		t.Fatal(err)
	}

	if err := requireWorkPermissionForCurrentActor(ctx, checker, workID, policyv1.Work.EditArchived); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 {
		t.Fatalf("permission checks = %d, want exactly 1", checker.calls)
	}
	if checker.decision.Resource() != want.Resource() {
		t.Fatalf("resource = %s:%s, want %s:%s", checker.decision.Resource().Type(), checker.decision.Resource().ID(), want.Resource().Type(), want.Resource().ID())
	}
	if checker.decision.Action().Permission() != want.Action().Permission() {
		t.Fatalf("permission = %q, want %q", checker.decision.Action().Permission(), want.Action().Permission())
	}
}

func TestRequireWorkGlobalActionsUseDomainCatalog(t *testing.T) {
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID:    auth.IdentityID("44444444-4444-4444-8444-444444444444"),
		SessionID:     auth.SessionID("55555555-5555-4555-8555-555555555555"),
		Authenticated: true,
	})
	for _, test := range []struct {
		name    string
		require func(context.Context, CollaborationPermissionChecker) error
		want    func() (policyv1.Can, error)
	}{
		{name: "create", require: requireWorkCreate, want: policyv1.Work.Create},
		{name: "list", require: requireWorkList, want: policyv1.Work.List},
	} {
		t.Run(test.name, func(t *testing.T) {
			checker := &workAuthorizationRecordingChecker{}
			want, err := test.want()
			if err != nil {
				t.Fatal(err)
			}
			if err := test.require(ctx, checker); err != nil {
				t.Fatal(err)
			}
			if checker.calls != 1 {
				t.Fatalf("permission checks = %d, want exactly 1", checker.calls)
			}
			if checker.decision.Resource() != want.Resource() {
				t.Fatalf("resource = %s:%s, want %s:%s", checker.decision.Resource().Type(), checker.decision.Resource().ID(), want.Resource().Type(), want.Resource().ID())
			}
			if checker.decision.Action().Name() != want.Action().Name() {
				t.Fatalf("action = %q, want %q", checker.decision.Action().Name(), want.Action().Name())
			}
			if checker.decision.Action().Permission() != want.Action().Permission() {
				t.Fatalf("permission = %q, want %q", checker.decision.Action().Permission(), want.Action().Permission())
			}
		})
	}
}
