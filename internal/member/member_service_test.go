//go:build integration

package member

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"gorm.io/gorm"
)

func TestSelfExtendedProfileMutationRequiresAuthorRole(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	bio := "biography"
	website := "https://profile.example.test"
	links := map[string]string{"homepage": website}

	requireDenied := func(t *testing.T, err error) {
		t.Helper()
		if code := errs.Wrap(err).Code(); code != connect.CodePermissionDenied {
			t.Fatalf("code = %s, error = %v; want permission_denied", code, err)
		}
	}
	service := newTestService(t, Deps{DB: stack.DB, SpiceDB: stack.SpiceDBClient})
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	author := stack.CreateUser(t, policyv1.Role.Author().ID())
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	userCtx := auth.WithUser(context.Background(), user.AuthUserInfo())
	requireDenied(t, service.validateSelfMemberProfileMutation(userCtx, &bio, nil, nil))
	requireDenied(t, service.validateSelfMemberProfileMutation(userCtx, nil, &website, nil))
	requireDenied(t, service.validateSelfMemberProfileMutation(userCtx, nil, nil, links))
	if err := service.validateSelfMemberProfileMutation(userCtx, nil, nil, nil); err != nil {
		t.Fatalf("nickname-only mutation denied: %v", err)
	}
	for _, principal := range []*testutil.OryUser{author, admin} {
		ctx := auth.WithUser(context.Background(), principal.AuthUserInfo())
		if err := service.validateSelfMemberProfileMutation(ctx, &bio, &website, links); err != nil {
			t.Fatalf("%s extended mutation denied: %v", principal.Role, err)
		}
	}
}

func TestGetCurrentSessionProjectsAuthenticatedIdentityIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.Author().ID())
	service := NewConnectHandler(newTestService(t, Deps{
		DB:        stack.DB,
		CDNDomain: "https://cdn.example.test",
		SpiceDB:   stack.SpiceDBClient,
		Accounts:  integrationAccountSummaryReader{},
	}))

	response, err := service.GetCurrentSession(
		auth.WithUser(t.Context(), user.AuthUserInfo()),
		connect.NewRequest(&managev1.GetCurrentSessionRequest{}),
	)
	if err != nil {
		t.Fatalf("get current session: %v", err)
	}
	if response.Msg.AccountIdentityId != user.IdentityID {
		t.Fatalf("account identity = %q, want %q", response.Msg.AccountIdentityId, user.IdentityID)
	}
	if response.Msg.Member.GetSummary().GetId() != user.MemberID {
		t.Fatalf("member id = %q, want %q", response.Msg.Member.GetSummary().GetId(), user.MemberID)
	}
	if response.Msg.Member.GetRole() != policyv1.AuthorizationRole_AUTHOR {
		t.Fatalf("role = %s, want AUTHOR", response.Msg.Member.GetRole())
	}
}

type currentSessionAccountSummaryReader struct {
	summary *managev1.AccountSummary
}

func (reader currentSessionAccountSummaryReader) SessionSummaryForMember(
	context.Context, *gorm.DB, *auth.SpiceDBClient, string,
) (*managev1.AccountSummary, error) {
	return reader.summary, nil
}

func (reader currentSessionAccountSummaryReader) SummaryForMember(
	context.Context, *gorm.DB, *auth.SpiceDBClient, string,
) (*managev1.AccountSummary, error) {
	return reader.summary, nil
}

func (currentSessionAccountSummaryReader) SummariesForMembers(
	context.Context, *gorm.DB, *auth.SpiceDBClient, []string,
) (map[string]*managev1.AccountSummary, error) {
	panic("GetCurrentSession must not load a member collection")
}

func TestGetCurrentSessionUsesLeanProjectionWithoutProfileOrSectionAuthorizationIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.Author().ID())
	email := "author@example.test"
	service := NewConnectHandler(newTestService(t, Deps{
		DB:        stack.DB,
		CDNDomain: "https://cdn.example.test",
		// Unconnected: the lean session boundary must not perform author
		// checks or My-section resource lookups itself.
		SpiceDB: &auth.SpiceDBClient{},
		Accounts: currentSessionAccountSummaryReader{summary: &managev1.AccountSummary{
			MemberId:       user.MemberID,
			CanonicalEmail: &managev1.CanonicalEmailSummary{Email: email},
			Role:           policyv1.AuthorizationRole_AUTHOR,
			Status:         managev1.AccountStatus_ACCOUNT_STATUS_ACTIVE,
		}},
	}))

	response, err := service.GetCurrentSession(
		auth.WithUser(t.Context(), user.AuthUserInfo()),
		connect.NewRequest(&managev1.GetCurrentSessionRequest{}),
	)
	if err != nil {
		t.Fatalf("get current session: %v", err)
	}
	if response.Msg.AccountIdentityId != user.IdentityID {
		t.Fatalf("account identity = %q, want %q", response.Msg.AccountIdentityId, user.IdentityID)
	}
	if response.Msg.Member.GetSummary().GetId() != user.MemberID {
		t.Fatalf("member id = %q, want %q", response.Msg.Member.GetSummary().GetId(), user.MemberID)
	}
	if response.Msg.Member.GetEmail() != email {
		t.Fatalf("email = %q, want %q", response.Msg.Member.GetEmail(), email)
	}
	if response.Msg.Member.GetRole() != policyv1.AuthorizationRole_AUTHOR {
		t.Fatalf("role = %s, want AUTHOR", response.Msg.Member.GetRole())
	}
}
