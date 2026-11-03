package criteria_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	membercriteria "github.com/echovisionlab/geul-api/internal/member/criteria"
	"github.com/echovisionlab/geul-api/internal/member/criteria/criteriatest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestGetCriteriaReturnsTheFilterReferencesAndEstimate(t *testing.T) {
	h := newHarness(t)
	tagID := h.store.SeedMemberTag()
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Tagged", Type: membercriteria.KindMemberTags, Config: membercriteria.Config{MemberTagIDs: []string{tagID}}})
	h.store.SeedCampaign(criteria.ID, criteriatest.CampaignDraft)
	h.store.SeedDownloadPolicy(membercriteria.DownloadPolicy{Owner: "post", ItemID: uuid.NewString(), CriteriaIDs: []string{criteria.ID}})
	h.counter.count = 12

	view, err := h.service.GetCriteria(h.admin, membercriteria.GetCriteriaQuery{ID: criteria.ID})
	require.NoError(t, err)
	require.Equal(t, []string{tagID}, view.Criteria.Config.MemberTagIDs)
	require.Equal(t, membercriteria.References{Campaigns: 1, DownloadPolicies: 1}, view.References)
	require.Equal(t, int32(12), *view.EstimatedCount)
	require.Equal(t, []membercriteria.Criteria{view.Criteria}, h.counter.criteriaSet)
	viewCan, err := policyv1.MemberCriteria.View(criteria.ID)
	require.Equal(t, []authztest.Check{authztest.Of(h.identityID, viewCan, err)}, h.checker.Checks())

	h.counter.err = errors.New("count failed")
	view, err = h.service.GetCriteria(h.admin, membercriteria.GetCriteriaQuery{ID: criteria.ID})
	require.NoError(t, err, "a failed estimate leaves the count out")
	require.Nil(t, view.EstimatedCount)

	_, err = h.service.GetCriteria(h.admin, membercriteria.GetCriteriaQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestGetCriteriaRequiresAnAdministrator(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	criteria := h.store.SeedCriteria(membercriteria.Criteria{Name: "Hidden", Type: membercriteria.KindAllMembers})
	_, err := h.service.GetCriteria(h.admin, membercriteria.GetCriteriaQuery{ID: criteria.ID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	require.Empty(t, h.counter.criteriaSet)
}

func TestListCriteriaSetPagesActiveCriteriaSetWithReferences(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC()
	active := h.store.SeedCriteria(membercriteria.Criteria{Name: "Active", Type: membercriteria.KindAllMembers})
	h.store.SeedCriteria(membercriteria.Criteria{Name: "Archived", Type: membercriteria.KindAllMembers, ArchivedAt: &archivedAt})
	h.store.SeedDeliveryRun(active.ID, criteriatest.RunSending)

	page, err := h.service.ListCriteriaSet(h.admin, membercriteria.ListCriteriaSetQuery{})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, int32(20), page.Limit, "the limit defaults to 20")
	require.Len(t, page.CriteriaSet, 1)
	require.Equal(t, active.ID, page.CriteriaSet[0].Criteria.ID)
	require.Equal(t, membercriteria.References{DeliveryRuns: 1}, page.CriteriaSet[0].References)

	page, err = h.service.ListCriteriaSet(h.admin, membercriteria.ListCriteriaSetQuery{IncludeArchived: true, Limit: 500, Offset: -3})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Equal(t, int32(100), page.Limit, "the limit is capped at 100")
	require.Zero(t, page.Offset)
	require.Equal(t, "Archived", page.CriteriaSet[1].Criteria.Name)

	denied := newHarness(t, &authztest.Checker{})
	_, err = denied.service.ListCriteriaSet(denied.admin, membercriteria.ListCriteriaSetQuery{})
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
}

func TestListAuthenticatedAccessCriteriaSetServesAuthorsOnly(t *testing.T) {
	listCan, err := policyv1.MemberCriteria.ListAuthenticatedAccess()
	require.NoError(t, err)
	h := newHarness(t, (&authztest.Checker{}).Allow(listCan, nil))
	h.store.SeedCriteria(membercriteria.Criteria{Name: "All", Type: membercriteria.KindAllMembers})
	filtered := h.store.SeedCriteria(membercriteria.Criteria{Name: "Filtered", Type: membercriteria.KindFilter})

	page, err := h.service.ListAuthenticatedAccessCriteriaSet(h.admin, membercriteria.ListAuthenticatedAccessCriteriaSetQuery{Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, []string{filtered.ID}, []string{page.CriteriaSet[0].ID})

	identityID, memberID := uuid.NewString(), uuid.NewString()
	banned := principalContext(t, identityID, memberID)
	user := *auth.GetUser(banned)
	user.Banned = true
	for name, test := range map[string]struct {
		ctx     context.Context
		checker *authztest.Checker
		code    connect.Code
		message string
	}{
		"anonymous":     {context.Background(), authztest.AllowAll(), connect.CodeUnauthenticated, "authentication required"},
		"banned":        {auth.WithUser(banned, &user), authztest.AllowAll(), connect.CodePermissionDenied, "account is banned"},
		"no permission": {h.admin, &authztest.Checker{}, connect.CodePermissionDenied, "author access required"},
		"SpiceDB down":  {h.admin, authztest.AllowAll().Fail(errors.New("down")), connect.CodeUnavailable, "SpiceDB unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			denied := newHarness(t, test.checker)
			_, err := denied.service.ListAuthenticatedAccessCriteriaSet(test.ctx, membercriteria.ListAuthenticatedAccessCriteriaSetQuery{})
			require.Equal(t, test.code, errs.Wrap(err).Code())
			require.Equal(t, test.message, errs.Wrap(err).Message())
		})
	}
}

func TestRequireAuthenticatedAccessCriteriaSetAcceptsOnlyActiveAccessTypes(t *testing.T) {
	archivedAt := time.Now().UTC()
	filtered := membercriteria.Criteria{ID: "a", Type: membercriteria.KindFilter}
	tagged := membercriteria.Criteria{ID: "b", Type: membercriteria.KindMemberTags}
	require.NoError(t, membercriteria.RequireAuthenticatedAccessCriteriaSet([]string{"a", "b"}, []membercriteria.Criteria{filtered, tagged}))
	for name, locked := range map[string][]membercriteria.Criteria{
		"missing":     {filtered},
		"all members": {filtered, {ID: "b", Type: membercriteria.KindAllMembers}},
		"archived":    {filtered, {ID: "b", Type: membercriteria.KindMemberTags, ArchivedAt: &archivedAt}},
	} {
		err := membercriteria.RequireAuthenticatedAccessCriteriaSet([]string{"a", "b"}, locked)
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code(), name)
		require.Equal(t, "member_criteria_ids: one or more Member criteria do not exist or cannot grant authenticated access", errs.Wrap(err).Message(), name)
	}
}
