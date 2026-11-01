package series_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/series"
	"github.com/echovisionlab/geul-api/internal/series/seriestest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var staleTime = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

// seedPosts stores Posts outside any Series, last updated long ago.
func (f *fixture) seedPosts(count int) []string {
	ids := make([]string, count)
	for index := range ids {
		ids[index] = f.store.SeedPost(seriestest.Post{UpdatedAt: staleTime}).ID
	}
	return ids
}

func (f *fixture) assign(t *testing.T, seriesID string, postIDs ...string) {
	t.Helper()
	for _, postID := range postIDs {
		require.NoError(t, f.service.AssignPostToSeries(f.ctx(t), series.AssignPostToSeriesCommand{PostID: postID, SeriesID: seriesID}))
	}
}

func (f *fixture) backdate(postIDs ...string) {
	for _, postID := range postIDs {
		post := f.store.StoredPost(postID)
		post.UpdatedAt = staleTime
		f.store.SeedPost(post)
	}
}

func (f *fixture) requireTouched(t *testing.T, touched bool, postIDs ...string) {
	t.Helper()
	for _, postID := range postIDs {
		require.Equal(t, touched, f.store.StoredPost(postID).UpdatedAt.After(staleTime), "post %s", postID)
	}
}

func TestAssignPostToSeriesAppendsAndMovesBetweenSeries(t *testing.T) {
	f := newFixture(t)
	one, two := f.seedSeries("One"), f.seedSeries("Two")
	posts := f.seedPosts(3)
	oneRevision, twoRevision := f.store.Revision(one.ID), f.store.Revision(two.ID)

	f.assign(t, one.ID, posts...)
	require.Equal(t, posts, f.store.Order(one.ID))
	f.requireTouched(t, true, posts...)
	require.NotEqual(t, oneRevision, f.store.Revision(one.ID))

	oneRevision = f.store.Revision(one.ID)
	f.backdate(posts...)
	f.assign(t, two.ID, posts[0])

	require.Equal(t, []string{posts[1], posts[2]}, f.store.Order(one.ID), "the Series it left closes its gap")
	require.Equal(t, []string{posts[0]}, f.store.Order(two.ID))
	require.Equal(t, 0, *f.store.StoredPost(posts[1]).SeriesOrder)
	f.requireTouched(t, true, posts[0], posts[1], posts[2])
	require.NotEqual(t, oneRevision, f.store.Revision(one.ID), "both Series documents advance")
	require.NotEqual(t, twoRevision, f.store.Revision(two.ID))
	records := f.audit.Records()
	moved := records[len(records)-1]
	require.Equal(t, posts[0], moved.SubjectPostID)
	require.Equal(t, one.ID, moved.PreviousSeriesID)
	require.Equal(t, two.ID, moved.NewSeriesID)

	before := len(f.audit.Records())
	f.assign(t, two.ID, posts[0])
	require.Len(t, f.audit.Records(), before, "assigning a Post to its own Series changes nothing")
}

func TestAssignPostToSeriesLocksBothRootsBeforeTheManageCheck(t *testing.T) {
	f := newFixture(t)
	one, two := f.seedSeries("One"), f.seedSeries("Two")
	post := f.seedPosts(1)[0]
	f.assign(t, one.ID, post)

	f.assign(t, two.ID, post)

	require.Equal(t, []authztest.Check{
		authztest.Of(f.identityID, must(policyv1.PostSeries.Manage(one.ID)), nil),
		authztest.Of(f.identityID, must(policyv1.PostSeries.Manage(two.ID)), nil),
	}, f.checker.Checks(), "each assignment makes the one manage decision on its target")
}

func TestAssignPostToSeriesRefusesWithoutAuthority(t *testing.T) {
	for name, test := range map[string]struct {
		prepare func(f *fixture, seriesID, postID string)
		postID  func(postID string) string
		want    connect.Code
	}{
		"the Series manage action": {prepare: func(f *fixture, seriesID, _ string) {
			f.checker.Deny(policyv1.PostSeries.Manage(seriesID))
		}, want: connect.CodeNotFound},
		"the Post edit decision": {prepare: func(f *fixture, _, postID string) {
			post := f.store.StoredPost(postID)
			post.EditErr = domainerr.ResourceNotFound("post", postID)
			f.store.SeedPost(post)
		}, want: connect.CodeNotFound},
		"a missing Post": {postID: func(string) string { return uuid.NewString() }, want: connect.CodeNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			root := f.seedSeries("Guarded")
			postID := f.seedPosts(1)[0]
			if test.prepare != nil {
				test.prepare(f, root.ID, postID)
			}
			if test.postID != nil {
				postID = test.postID(postID)
			}
			err := f.service.AssignPostToSeries(f.ctx(t), series.AssignPostToSeriesCommand{PostID: postID, SeriesID: root.ID})
			requireCode(t, test.want, err)
			require.Empty(t, f.store.Order(root.ID))
			require.Empty(t, f.audit.Records())
		})
	}
}

func TestReorderSeriesPostsTouchesOnlyMovedPosts(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Ordered")
	posts := f.seedPosts(3)
	f.assign(t, root.ID, posts...)
	f.backdate(posts...)
	revision := f.store.Revision(root.ID)

	next := []string{posts[1], posts[0], posts[2]}
	require.NoError(t, f.service.ReorderSeriesPosts(f.ctx(t), series.ReorderSeriesPostsCommand{SeriesID: root.ID, PostIDs: next}))

	require.Equal(t, next, f.store.Order(root.ID))
	f.requireTouched(t, true, posts[0], posts[1])
	f.requireTouched(t, false, posts[2])
	require.NotEqual(t, revision, f.store.Revision(root.ID))
	records := f.audit.Records()
	require.Equal(t, &next, records[len(records)-1].PostIDs)

	before := len(f.audit.Records())
	require.NoError(t, f.service.ReorderSeriesPosts(f.ctx(t), series.ReorderSeriesPostsCommand{SeriesID: root.ID, PostIDs: next}))
	require.Len(t, f.audit.Records(), before, "the same order is a no-op")
}

func TestReorderSeriesPostsNeedsTheExactCurrentSet(t *testing.T) {
	f := newFixture(t)
	root, other := f.seedSeries("Ordered"), f.seedSeries("Other")
	posts := f.seedPosts(3)
	f.assign(t, root.ID, posts[0], posts[1])
	f.assign(t, other.ID, posts[2])
	for name, order := range map[string][]string{
		"duplicate":     {posts[1], posts[1]},
		"missing":       {posts[1]},
		"foreign":       {posts[1], posts[0], posts[2]},
		"unknown":       {posts[1], uuid.NewString()},
		"not canonical": {posts[1], "post-1"},
	} {
		t.Run(name, func(t *testing.T) {
			err := f.service.ReorderSeriesPosts(f.ctx(t), series.ReorderSeriesPostsCommand{SeriesID: root.ID, PostIDs: order})
			requireCode(t, connect.CodeInvalidArgument, err)
			require.Equal(t, []string{posts[0], posts[1]}, f.store.Order(root.ID))
		})
	}
}

func TestUnassignPostFromSeriesCompactsTheOrder(t *testing.T) {
	f := newFixture(t)
	root, other := f.seedSeries("Ordered"), f.seedSeries("Other")
	posts := f.seedPosts(4)
	f.assign(t, root.ID, posts[0], posts[1], posts[2])
	f.assign(t, other.ID, posts[3])

	err := f.service.UnassignPostFromSeries(f.ctx(t), series.UnassignPostFromSeriesCommand{SeriesID: root.ID, PostID: posts[3]})
	requireCode(t, connect.CodeInvalidArgument, err)

	require.NoError(t, f.service.UnassignPostFromSeries(f.ctx(t), series.UnassignPostFromSeriesCommand{SeriesID: root.ID, PostID: posts[0]}))
	require.Nil(t, f.store.StoredPost(posts[0]).SeriesID)
	require.Equal(t, []string{posts[1], posts[2]}, f.store.Order(root.ID))
	require.Equal(t, 0, *f.store.StoredPost(posts[1]).SeriesOrder)
	require.Equal(t, 1, *f.store.StoredPost(posts[2]).SeriesOrder)
	records := f.audit.Records()
	require.Equal(t, root.ID, records[len(records)-1].PreviousSeriesID)
	require.Empty(t, records[len(records)-1].NewSeriesID)
}

func TestListSeriesPostsListsThemInOrder(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Listed")
	posts := f.seedPosts(2)
	f.assign(t, root.ID, posts[1], posts[0])

	listed, err := f.service.ListSeriesPosts(f.ctx(t), series.ListSeriesPostsQuery{SeriesID: root.ID})

	require.NoError(t, err)
	require.Len(t, listed, 2)
	require.Equal(t, posts[1], listed[0].ID)
	require.Equal(t, 1, *listed[1].SeriesOrder)
}

func TestAddSeriesManagerWritesTheAttributionAndTheGrant(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Managed")
	managerMember, managerIdentity := uuid.NewString(), uuid.NewString()
	f.store.SeedMember(managerMember, seriestest.Member{IdentityID: managerIdentity})
	actor := must(policyv1.NewAccountIdentityActor(managerIdentity))
	touch := must(policyv1.PostSeries.TouchManager(root.ID, actor))
	remove := must(policyv1.PostSeries.DeleteManager(root.ID, actor))

	require.NoError(t, f.service.AddSeriesManager(f.ctx(t), series.AddSeriesManagerCommand{SeriesID: root.ID, MemberID: managerMember}))
	require.True(t, f.store.Managed(root.ID, managerMember))
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, f.checker.Applied())
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, managerMember, records[0].SubjectMemberID)
	require.Equal(t, sharedtelemetry.AuditRelationshipManager, records[0].NewRelationship)

	require.NoError(t, f.service.AddSeriesManager(f.ctx(t), series.AddSeriesManagerCommand{SeriesID: root.ID, MemberID: managerMember}))
	require.Len(t, f.audit.Records(), 1, "an existing manager is re-granted without a record")
	require.Len(t, f.checker.Applied(), 2)

	require.NoError(t, f.service.RemoveSeriesManager(f.ctx(t), series.RemoveSeriesManagerCommand{SeriesID: root.ID, MemberID: managerMember}))
	require.False(t, f.store.Managed(root.ID, managerMember))
	require.Equal(t, []policyv1.RelationshipMutation{remove}, f.checker.Applied()[2])
	err := f.service.RemoveSeriesManager(f.ctx(t), series.RemoveSeriesManagerCommand{SeriesID: root.ID, MemberID: managerMember})
	requireCode(t, connect.CodeNotFound, err)
}

func TestAddSeriesManagerRollsBackTheAttributionWhenTheGrantFails(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Managed")
	managerMember := uuid.NewString()
	f.store.SeedMember(managerMember, seriestest.Member{IdentityID: uuid.NewString()})
	f.checker.FailWrites(errDown)

	err := f.service.AddSeriesManager(f.ctx(t), series.AddSeriesManagerCommand{SeriesID: root.ID, MemberID: managerMember})

	require.ErrorIs(t, err, errDown)
	require.Equal(t, mutation.StepRollback, f.lastStep())
	require.False(t, f.store.Managed(root.ID, managerMember))

	f.checker.FailWrites(nil)
	err = f.service.AddSeriesManager(f.ctx(t), series.AddSeriesManagerCommand{SeriesID: root.ID, MemberID: uuid.NewString()})
	requireCode(t, connect.CodeNotFound, err)
}

func TestRemoveSeriesManagerOfARetiredMemberWritesNoGrant(t *testing.T) {
	f := newFixture(t)
	root := f.seedSeries("Managed")
	retired := uuid.NewString()
	f.store.SeedMember(retired, seriestest.Member{IdentityID: uuid.NewString(), Unlinked: true})
	f.store.SeedManager(root.ID, retired, contractTime)

	require.NoError(t, f.service.RemoveSeriesManager(f.ctx(t), series.RemoveSeriesManagerCommand{SeriesID: root.ID, MemberID: retired}))

	require.False(t, f.store.Managed(root.ID, retired))
	require.Empty(t, f.checker.Applied(), "account deletion already removed the grant")
	require.NotContains(t, f.units.Steps(), mutation.StepRelationships)
	records := f.audit.Records()
	require.Len(t, records, 1)
	require.Equal(t, sharedtelemetry.AuditRelationshipNone, records[0].NewRelationship)
}
