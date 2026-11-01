package pageaccess_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/pageaccess"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// audience is the reader's state as the Audience reads it.
type audience struct {
	inactive   bool
	tags       []string
	subscribed bool
	locked     int
}

func (a *audience) LockActivePrincipal(context.Context, *auth.UserInfo) (bool, error) {
	a.locked++
	return !a.inactive, nil
}

func (a *audience) HasUserTag(_ context.Context, _ string, tagIDs []string) (bool, error) {
	for _, tag := range a.tags {
		if slices.Contains(tagIDs, tag) {
			return true, nil
		}
	}
	return false, nil
}

func (a *audience) SubscribesToNewsletter(context.Context, string) (bool, error) {
	return a.subscribed, nil
}

type checker struct {
	allowed map[string]bool
	calls   []string
}

func (c *checker) Can(_ context.Context, decision policyv1.AuthorizationDecision) (bool, error) {
	c.calls = append(c.calls, decision.EngineKey())
	return c.allowed[decision.EngineKey()], nil
}

func reader(t *testing.T) context.Context {
	t.Helper()
	return auth.WithUser(t.Context(), &auth.UserInfo{
		IdentityID: auth.IdentityID(uuid.NewString()), MemberID: auth.MemberID(uuid.NewString()),
		SessionID: auth.SessionID(uuid.NewString()), Authenticated: true, Onboarded: true,
	})
}

func encoded(t *testing.T, policy *commonv1.PageAccessPolicy) json.RawMessage {
	t.Helper()
	raw, err := pageaccess.Encode(pageaccess.FromMessage(policy))
	require.NoError(t, err)
	return raw
}

func engineKey(can policyv1.Can, err error) string {
	if err != nil {
		panic(err)
	}
	return can.EngineKey()
}

func TestEvaluateAdmitsByModeAndCurrentConditions(t *testing.T) {
	pageID := uuid.NewString()
	author := engineKey(policyv1.Platform.IsAuthor())
	admin := engineKey(policyv1.Platform.IsAdmin())
	manage := engineKey(policyv1.Page.Manage(pageID))
	tagA, tagB := uuid.NewString(), uuid.NewString()
	all := &commonv1.PageAccessPolicy{
		Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ALL,
		AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR}, UserTagIds: []string{tagA, tagB}, NewsletterSubscriber: true,
	}
	anyGroup := &commonv1.PageAccessPolicy{
		Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, Match: commonv1.PageAccessMatch_PAGE_ACCESS_MATCH_ANY,
		AllowedRoles: []policyv1.AuthorizationRole{policyv1.AuthorizationRole_AUTHOR, policyv1.AuthorizationRole_ADMIN},
		UserTagIds:   []string{tagA, tagB}, NewsletterSubscriber: true,
	}
	for _, test := range []struct {
		name      string
		anonymous bool
		raw       json.RawMessage
		audience  audience
		allowed   []string
		want      pageaccess.Reason
	}{
		{name: "an empty legacy policy is public", anonymous: true, want: pageaccess.ReasonAllowed},
		{name: "public admits anyone", anonymous: true, raw: encoded(t, nil), want: pageaccess.ReasonAllowed},
		{name: "authenticated refuses an anonymous reader", anonymous: true, raw: encoded(t, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED}), want: pageaccess.ReasonAuthenticationRequired},
		{name: "authenticated admits an active reader", raw: encoded(t, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED}), want: pageaccess.ReasonAllowed},
		{name: "an inactive reader is unauthenticated", raw: encoded(t, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED}), audience: audience{inactive: true}, want: pageaccess.ReasonAuthenticationRequired},
		{name: "no group met", raw: encoded(t, anyGroup), want: pageaccess.ReasonConditionsNotMet},
		{name: "any role qualifies", raw: encoded(t, anyGroup), allowed: []string{admin}, want: pageaccess.ReasonAllowed},
		{name: "any one tag qualifies", raw: encoded(t, anyGroup), audience: audience{tags: []string{tagB}}, want: pageaccess.ReasonAllowed},
		{name: "a subscription qualifies", raw: encoded(t, anyGroup), audience: audience{subscribed: true}, want: pageaccess.ReasonAllowed},
		{name: "all needs the subscription too", raw: encoded(t, all), audience: audience{tags: []string{tagA}}, allowed: []string{author}, want: pageaccess.ReasonConditionsNotMet},
		{name: "all groups qualify", raw: encoded(t, all), audience: audience{tags: []string{tagA}, subscribed: true}, allowed: []string{author}, want: pageaccess.ReasonAllowed},
		{name: "a manager bypasses missing conditions", raw: encoded(t, all), allowed: []string{manage}, want: pageaccess.ReasonAllowed},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := reader(t)
			if test.anonymous {
				ctx = t.Context()
			}
			checks := &checker{allowed: map[string]bool{}}
			for _, key := range test.allowed {
				checks.allowed[key] = true
			}
			reason, err := pageaccess.Evaluate(ctx, &test.audience, checks, pageID, test.raw)
			require.NoError(t, err)
			require.Equal(t, test.want, reason)
		})
	}
}

func TestEvaluateLocksTheReaderOnlyForARestrictedPage(t *testing.T) {
	ctx := reader(t)
	public := &audience{}
	_, err := pageaccess.Evaluate(ctx, public, &checker{}, uuid.NewString(), encoded(t, nil))
	require.NoError(t, err)
	require.Zero(t, public.locked)
	restricted := &audience{}
	_, err = pageaccess.Evaluate(ctx, restricted, &checker{}, uuid.NewString(), encoded(t, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED}))
	require.NoError(t, err)
	require.Equal(t, 1, restricted.locked)
}

func TestRequireAnswersAMalformedPolicyAsAServerFault(t *testing.T) {
	raw, err := protojson.Marshal(&commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode(999)})
	require.NoError(t, err)
	ctx := reader(t)
	reason, err := pageaccess.Evaluate(ctx, &audience{}, &checker{}, uuid.NewString(), raw)
	require.Error(t, err)
	require.NotEqual(t, pageaccess.ReasonAllowed, reason)
	require.Equal(t, connect.CodeInternal, errs.Wrap(pageaccess.Require(ctx, &audience{}, &checker{}, uuid.NewString(), raw)).Code())
}

func TestRequireAnswersEachReason(t *testing.T) {
	restricted := encoded(t, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_AUTHENTICATED})
	require.NoError(t, pageaccess.Require(reader(t), &audience{}, &checker{}, uuid.NewString(), restricted))
	require.Equal(t, connect.CodeUnauthenticated, errs.Wrap(pageaccess.Require(t.Context(), &audience{}, &checker{}, uuid.NewString(), restricted)).Code())
	conditions := encoded(t, &commonv1.PageAccessPolicy{Mode: commonv1.PageAccessMode_PAGE_ACCESS_MODE_CONDITIONS, NewsletterSubscriber: true})
	denied := pageaccess.Require(reader(t), &audience{}, &checker{}, uuid.NewString(), conditions)
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(denied).Code())
	require.False(t, errors.Is(denied, context.Canceled))
}
