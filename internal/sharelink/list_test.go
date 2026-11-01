package sharelink_test

import (
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestListShareLinksReturnsTheTargetsLinksNewestFirst(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	pageID := f.target(sharelink.EntityPage, sharelink.Target{})
	f.checker.Allow(policyv1.Page.ManageShareLinks(pageID))
	now := time.Now()
	older := f.store.SeedLink(sharelink.Link{Token: "older", EntityType: sharelink.EntityPage, EntityID: pageID, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour)})
	newer := f.store.SeedLink(sharelink.Link{Token: "newer", EntityType: sharelink.EntityPage, EntityID: pageID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	f.store.SeedLink(sharelink.Link{Token: "other", EntityType: sharelink.EntityPage, EntityID: uuid.NewString(), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})

	links, err := f.links.ListShareLinks(callerContext(t), sharelink.ListShareLinksQuery{EntityType: sharelink.EntityPage, EntityID: pageID})
	require.NoError(t, err)
	require.Equal(t, []sharelink.Link{newer, older}, links)
	require.Equal(t, []authztest.Check{check(policyv1.Page.ManageShareLinks, pageID)}, f.checker.Checks())
	require.Empty(t, f.fence.Locked(), "a read checks the action without the identity fence")
	require.Empty(t, f.units.Steps())
}

func TestListShareLinksRejectsAnInvalidTargetBeforeReadingIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	handler := sharelink.NewConnectHandler(f.links)

	_, err := handler.ListShareLinks(callerContext(t), connect.NewRequest(&managev1.ListShareLinksRequest{EntityId: uuid.NewString()}))
	requireMessage(t, connect.CodeInvalidArgument, "unsupported entity type: SHARE_LINK_ENTITY_TYPE_UNSPECIFIED", err)
	_, err = f.links.ListShareLinks(callerContext(t), sharelink.ListShareLinksQuery{EntityType: sharelink.EntityPost, EntityID: "not-a-uuid"})
	requireCode(t, connect.CodeInvalidArgument, err)
	require.Empty(t, f.checker.Checks())
}

func TestListShareLinksMasksMissingAndDeniedTargets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	missing := uuid.NewString()
	_, err := f.links.ListShareLinks(callerContext(t), sharelink.ListShareLinksQuery{EntityType: sharelink.EntityPage, EntityID: missing})
	requireMessage(t, connect.CodeNotFound, "page not found: "+missing, err)
	require.Empty(t, f.checker.Checks())

	denied := f.target(sharelink.EntityPage, sharelink.Target{})
	_, err = f.links.ListShareLinks(callerContext(t), sharelink.ListShareLinksQuery{EntityType: sharelink.EntityPage, EntityID: denied})
	requireMessage(t, connect.CodeNotFound, "page not found: "+denied, err)
	require.Equal(t, []authztest.Check{check(policyv1.Page.ManageShareLinks, denied)}, f.checker.Checks())
}

func TestListShareLinksOfAnArchivedTargetRequiresViewArchived(t *testing.T) {
	t.Parallel()
	for entityType, action := range map[sharelink.EntityType]auth.ResourceAction{
		sharelink.EntityPost: policyv1.Post.ViewArchived,
		sharelink.EntityWork: policyv1.Work.ViewArchived,
	} {
		t.Run(string(entityType), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			id := f.target(entityType, sharelink.Target{Archived: true})
			f.checker.Allow(action(id))

			_, err := f.links.ListShareLinks(callerContext(t), sharelink.ListShareLinksQuery{EntityType: entityType, EntityID: id})
			require.NoError(t, err)
			require.Equal(t, []authztest.Check{check(action, id)}, f.checker.Checks())
		})
	}
}

// A legal version takes links only while a future version is scheduled; the
// answer follows the authorization check so it reveals nothing to a stranger.
func TestListShareLinksOfALegalVersionRequiresAFutureSchedule(t *testing.T) {
	t.Parallel()
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Hour)
	for name, tc := range map[string]struct {
		target  sharelink.Target
		action  auth.ResourceAction
		allowed bool
	}{
		"scheduled in the future": {target: sharelink.Target{Scheduled: true, EffectiveFrom: &future}, action: policyv1.TermsHistory.ManageShareLinks, allowed: true},
		"scheduled in the past":   {target: sharelink.Target{Scheduled: true, EffectiveFrom: &past}, action: policyv1.TermsHistory.ManageShareLinks},
		"active":                  {target: sharelink.Target{}, action: policyv1.TermsHistory.ManageShareLinks},
		"archived":                {target: sharelink.Target{Archived: true}, action: policyv1.TermsHistory.ViewArchived},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			id := f.target(sharelink.EntityTerms, tc.target)
			f.checker.Allow(tc.action(id))

			_, err := f.links.ListShareLinks(callerContext(t), sharelink.ListShareLinksQuery{EntityType: sharelink.EntityTerms, EntityID: id})
			require.Equal(t, []authztest.Check{check(tc.action, id)}, f.checker.Checks())
			if tc.allowed {
				require.NoError(t, err)
				return
			}
			requireMessage(t, connect.CodeFailedPrecondition, "terms preview links require a future scheduled version", err)
		})
	}
}

func TestListShareLinksAnswersTheCallerAndSpiceDBFailures(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	labelID := f.target(sharelink.EntityLabel, sharelink.Target{})
	query := sharelink.ListShareLinksQuery{EntityType: sharelink.EntityLabel, EntityID: labelID}

	_, err := f.links.ListShareLinks(t.Context(), query)
	requireCode(t, connect.CodeUnauthenticated, err)
	_, err = f.links.ListShareLinks(principalContext(t, &auth.UserInfo{
		IdentityID: callerIdentityID, MemberID: callerMemberID, Authenticated: true, Onboarded: true, Banned: true,
	}), query)
	requireCode(t, connect.CodePermissionDenied, err)
	require.Empty(t, f.checker.Checks())

	unavailable := errors.New("spicedb unavailable")
	f.checker.Fail(unavailable)
	_, err = f.links.ListShareLinks(callerContext(t), query)
	requireMessage(t, connect.CodeUnavailable, "SpiceDB unavailable", err)
	require.ErrorIs(t, err, unavailable)
}
