package authz

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const checkerIdentityID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

type fakeAuthorizationDecisionChecker struct {
	allowed bool
	err     error
	calls   []policyv1.AuthorizationDecision
}

func (f *fakeAuthorizationDecisionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	f.calls = append(f.calls, decision)
	return f.allowed, f.err
}

func authenticatedCheckerContext() context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(checkerIdentityID),
		MemberID:      auth.MemberID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
		SessionID:     auth.SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
		Authenticated: true,
		Onboarded:     true,
	})
}

func newCheckerTestDB(t *testing.T, postID string) (*gorm.DB, *atomic.Int64) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE posts (id TEXT PRIMARY KEY)").Error)
	if postID != "" {
		require.NoError(t, db.Exec("INSERT INTO posts (id) VALUES (?)", postID).Error)
	}
	queryCount := &atomic.Int64{}
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(
		"authz:test_query_counter",
		func(*gorm.DB) { queryCount.Add(1) },
	))
	return db, queryCount
}

func TestSpiceDBResourceCheckerRejectsUnauthenticatedWithoutAuthorizationCall(t *testing.T) {
	fake := &fakeAuthorizationDecisionChecker{allowed: true}
	checker := newSpiceDBResourceChecker(fake, nil, "posts")
	can, err := policyv1.Post.Edit("cccccccc-cccc-4ccc-8ccc-cccccccccccc")
	require.NoError(t, err)
	err = checker.Check(context.Background(), can)

	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	require.Empty(t, fake.calls)
}

func TestSpiceDBResourceCheckerRejectsInvalidCanAndActorBeforeAuthorizationCall(t *testing.T) {
	const postID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	t.Run("invalid Can", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{allowed: true}
		checker := newSpiceDBResourceChecker(fake, db, "posts")

		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(checker.Check(authenticatedCheckerContext(), policyv1.Can{})))
		require.Empty(t, fake.calls)
		require.Zero(t, queryCount.Load())
	})

	t.Run("invalid account identity", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{allowed: true}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		ctx := auth.WithUser(context.Background(), &auth.UserInfo{
			IdentityID:    auth.IdentityID("not-a-uuid"),
			MemberID:      auth.MemberID("bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"),
			SessionID:     auth.SessionID("dddddddd-dddd-4ddd-8ddd-dddddddddddd"),
			Authenticated: true,
		})
		can, err := policyv1.Post.Edit(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(checker.Check(ctx, can)))
		require.Empty(t, fake.calls)
		require.Zero(t, queryCount.Load())
	})
}

func TestSpiceDBResourceCheckerUsesCompleteDecisionAndAuthenticatedAccountIdentity(t *testing.T) {
	const postID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	ctx := authenticatedCheckerContext()
	fake := &fakeAuthorizationDecisionChecker{allowed: true}
	checker := newSpiceDBResourceChecker(fake, nil, "posts")
	can, err := policyv1.Post.Edit(postID)
	require.NoError(t, err)

	require.NoError(t, checker.Check(ctx, can))
	require.Len(t, fake.calls, 1)
	decision := fake.calls[0]
	require.Equal(t, "post", decision.Resource().Type())
	require.Equal(t, postID, decision.Resource().ID())
	require.Equal(t, "edit", decision.Action().Name())
	require.Equal(t, "edit", decision.Action().Permission())
	require.Equal(t, checkerIdentityID, decision.Actor().AccountIdentityID())
	require.Equal(t, policyv1.DelegationDirectSession, decision.Delegation().Kind())
	require.NotEqual(t, auth.GetUser(ctx).MemberID.String(), decision.Actor().AccountIdentityID())
}

func TestSpiceDBResourceCheckerSupportsGeneratedPostActionsThroughExactPath(t *testing.T) {
	const postID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	actions := []func(string) (policyv1.Can, error){
		policyv1.Post.ViewArchived,
		policyv1.Post.EditArchived,
		policyv1.Post.ManageShareLinks,
	}

	for index, action := range actions {
		t.Run(fmt.Sprintf("action_%d", index), func(t *testing.T) {
			fake := &fakeAuthorizationDecisionChecker{allowed: true}
			checker := newSpiceDBResourceChecker(fake, nil, "posts")
			can, err := action(postID)
			require.NoError(t, err)

			require.NoError(t, checker.Check(authenticatedCheckerContext(), can))
			require.Len(t, fake.calls, 1)
			require.Equal(t, can.EngineKey(), fake.calls[0].EngineKey())
		})
	}
}

func TestSpiceDBResourceCheckerMapsDeniedAndDependencyFailures(t *testing.T) {
	ctx := authenticatedCheckerContext()
	const postID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	t.Run("denied", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(checker.Check(ctx, can)))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})

	t.Run("dependency failure", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{err: errors.New("unavailable")}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(checker.Check(ctx, can)))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})

	t.Run("allowed", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{allowed: true}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.NoError(t, checker.Check(ctx, can))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})
}

func TestSpiceDBResourceCheckerCheckOrNotFoundQueriesOnlyToMaskDenial(t *testing.T) {
	ctx := authenticatedCheckerContext()
	const postID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	t.Run("missing resource becomes not found", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, "")
		fake := &fakeAuthorizationDecisionChecker{}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeNotFound, connect.CodeOf(checker.CheckOrNotFound(ctx, can)))
		require.Len(t, fake.calls, 1)
		require.EqualValues(t, 1, queryCount.Load())
	})

	t.Run("existing resource preserves denied", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(checker.CheckOrNotFound(ctx, can)))
		require.Len(t, fake.calls, 1)
		require.EqualValues(t, 1, queryCount.Load())
	})

	t.Run("allowed skips existence query", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{allowed: true}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.NoError(t, checker.CheckOrNotFound(ctx, can))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})

	t.Run("dependency failure skips existence query", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{err: errors.New("unavailable")}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.EditArchived(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(checker.CheckOrNotFound(ctx, can)))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})
}

func TestSpiceDBResourceCheckerViewPreservesExistenceMaskingWithoutDatabase(t *testing.T) {
	const postID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

	t.Run("allowed", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{allowed: true}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.View(postID)
		require.NoError(t, err)

		require.NoError(t, checker.CheckView(authenticatedCheckerContext(), can))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})

	t.Run("unauthenticated", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{allowed: true}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.View(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeNotFound, connect.CodeOf(checker.CheckView(context.Background(), can)))
		require.Empty(t, fake.calls)
		require.Zero(t, queryCount.Load())
	})

	t.Run("denied", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.View(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeNotFound, connect.CodeOf(checker.CheckView(authenticatedCheckerContext(), can)))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})

	t.Run("dependency failure", func(t *testing.T) {
		db, queryCount := newCheckerTestDB(t, postID)
		fake := &fakeAuthorizationDecisionChecker{err: errors.New("unavailable")}
		checker := newSpiceDBResourceChecker(fake, db, "posts")
		can, err := policyv1.Post.View(postID)
		require.NoError(t, err)

		require.Equal(t, connect.CodeUnavailable, connect.CodeOf(checker.CheckView(authenticatedCheckerContext(), can)))
		require.Len(t, fake.calls, 1)
		require.Zero(t, queryCount.Load())
	})
}
