package audience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/audience/audiencetest"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestGetSegmentReturnsTheFilterReferencesAndEstimate(t *testing.T) {
	h := newHarness(t)
	tagID := h.store.SeedMemberTag()
	segment := h.store.SeedSegment(audience.Segment{Name: "Tagged", Type: audience.SegmentMemberTags, Config: audience.Config{MemberTagIDs: []string{tagID}}})
	h.store.SeedCampaign(segment.ID, audiencetest.CampaignDraft)
	h.store.SeedDownloadPolicy(audience.DownloadPolicy{Owner: "post", ItemID: uuid.NewString(), SegmentIDs: []string{segment.ID}})
	h.counter.count = 12

	view, err := h.service.GetSegment(h.admin, audience.GetSegmentQuery{ID: segment.ID})
	require.NoError(t, err)
	require.Equal(t, []string{tagID}, view.Segment.Config.MemberTagIDs)
	require.Equal(t, audience.References{Campaigns: 1, DownloadPolicies: 1}, view.References)
	require.Equal(t, int32(12), *view.EstimatedCount)
	require.Equal(t, []audience.Segment{view.Segment}, h.counter.segments)
	viewCan, err := policyv1.AudienceSegment.View(segment.ID)
	require.Equal(t, []authztest.Check{authztest.Of(h.identityID, viewCan, err)}, h.checker.Checks())

	h.counter.err = errors.New("count failed")
	view, err = h.service.GetSegment(h.admin, audience.GetSegmentQuery{ID: segment.ID})
	require.NoError(t, err, "a failed estimate leaves the count out")
	require.Nil(t, view.EstimatedCount)

	_, err = h.service.GetSegment(h.admin, audience.GetSegmentQuery{ID: uuid.NewString()})
	require.Equal(t, connect.CodeNotFound, errs.Wrap(err).Code())
}

func TestGetSegmentRequiresAnAdministrator(t *testing.T) {
	h := newHarness(t, &authztest.Checker{})
	segment := h.store.SeedSegment(audience.Segment{Name: "Hidden", Type: audience.SegmentAllMembers})
	_, err := h.service.GetSegment(h.admin, audience.GetSegmentQuery{ID: segment.ID})
	require.Equal(t, connect.CodePermissionDenied, errs.Wrap(err).Code())
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
	require.Empty(t, h.counter.segments)
}

func TestListSegmentsPagesActiveSegmentsWithReferences(t *testing.T) {
	h := newHarness(t)
	archivedAt := time.Now().UTC()
	active := h.store.SeedSegment(audience.Segment{Name: "Active", Type: audience.SegmentAllMembers})
	h.store.SeedSegment(audience.Segment{Name: "Archived", Type: audience.SegmentAllMembers, ArchivedAt: &archivedAt})
	h.store.SeedDeliveryRun(active.ID, audiencetest.RunSending)

	page, err := h.service.ListSegments(h.admin, audience.ListSegmentsQuery{})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, int32(20), page.Limit, "the limit defaults to 20")
	require.Len(t, page.Segments, 1)
	require.Equal(t, active.ID, page.Segments[0].Segment.ID)
	require.Equal(t, audience.References{DeliveryRuns: 1}, page.Segments[0].References)

	page, err = h.service.ListSegments(h.admin, audience.ListSegmentsQuery{IncludeArchived: true, Limit: 500, Offset: -3})
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Equal(t, int32(100), page.Limit, "the limit is capped at 100")
	require.Zero(t, page.Offset)
	require.Equal(t, "Archived", page.Segments[1].Segment.Name)

	denied := newHarness(t, &authztest.Checker{})
	_, err = denied.service.ListSegments(denied.admin, audience.ListSegmentsQuery{})
	require.Equal(t, "admin access required", errs.Wrap(err).Message())
}

func TestListAuthenticatedAccessSegmentsServesAuthorsOnly(t *testing.T) {
	listCan, err := policyv1.AudienceSegment.ListAuthenticatedAccess()
	require.NoError(t, err)
	h := newHarness(t, (&authztest.Checker{}).Allow(listCan, nil))
	h.store.SeedSegment(audience.Segment{Name: "All", Type: audience.SegmentAllMembers})
	filtered := h.store.SeedSegment(audience.Segment{Name: "Filtered", Type: audience.SegmentMembersByFilter})

	page, err := h.service.ListAuthenticatedAccessSegments(h.admin, audience.ListAuthenticatedAccessSegmentsQuery{Limit: 1})
	require.NoError(t, err)
	require.EqualValues(t, 1, page.Total)
	require.Equal(t, []string{filtered.ID}, []string{page.Segments[0].ID})

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
			_, err := denied.service.ListAuthenticatedAccessSegments(test.ctx, audience.ListAuthenticatedAccessSegmentsQuery{})
			require.Equal(t, test.code, errs.Wrap(err).Code())
			require.Equal(t, test.message, errs.Wrap(err).Message())
		})
	}
}

func TestRequireAuthenticatedAccessSegmentsAcceptsOnlyActiveAccessTypes(t *testing.T) {
	archivedAt := time.Now().UTC()
	filtered := audience.Segment{ID: "a", Type: audience.SegmentMembersByFilter}
	tagged := audience.Segment{ID: "b", Type: audience.SegmentMemberTags}
	require.NoError(t, audience.RequireAuthenticatedAccessSegments([]string{"a", "b"}, []audience.Segment{filtered, tagged}))
	for name, locked := range map[string][]audience.Segment{
		"missing":     {filtered},
		"all members": {filtered, {ID: "b", Type: audience.SegmentAllMembers}},
		"archived":    {filtered, {ID: "b", Type: audience.SegmentMemberTags, ArchivedAt: &archivedAt}},
	} {
		err := audience.RequireAuthenticatedAccessSegments([]string{"a", "b"}, locked)
		require.Equal(t, connect.CodeInvalidArgument, errs.Wrap(err).Code(), name)
		require.Equal(t, "audience_segment_ids: one or more audience segments do not exist or cannot grant authenticated access", errs.Wrap(err).Message(), name)
	}
}
