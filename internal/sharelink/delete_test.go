package sharelink_test

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/sharelink/sharelinktest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func seedLinkOn(f *fixture, entityType sharelink.EntityType, entityID string) sharelink.Link {
	return f.store.SeedLink(sharelink.Link{
		Token: uuid.NewString(), EntityType: entityType, EntityID: entityID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	})
}

func TestDeleteShareLinkAnswersAMissingLinkBeforeTheTransaction(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	missing := uuid.NewString()
	err := f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: missing})
	requireMessage(t, connect.CodeNotFound, "share link not found: "+missing, err)
	require.Empty(t, f.units.Steps())
	require.Empty(t, f.checker.Checks())
}

func TestDeleteShareLinkRejectsALinkOfAnUnknownTargetKind(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	legacy := seedLinkOn(f, "page", uuid.NewString())
	err := f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: legacy.ID})
	requireMessage(t, connect.CodeInvalidArgument, "unsupported entity type: page", err)
	require.Empty(t, f.units.Steps())
	require.Equal(t, []sharelink.Link{legacy}, f.store.Links())
}

func TestDeleteShareLinkMasksADeniedTargetAndKeepsTheLink(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	workID := f.target(sharelink.EntityWork, sharelink.Target{Archived: true})
	f.checker.Allow(policyv1.Work.ManageShareLinks(workID))
	link := seedLinkOn(f, sharelink.EntityWork, workID)

	err := f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: link.ID})
	requireMessage(t, connect.CodeNotFound, "work not found: "+workID, err)
	require.Equal(t, []authztest.Check{check(policyv1.Work.EditArchived, workID)}, f.checker.Checks(), "an archived target needs EditArchived")
	require.Equal(t, []sharelink.Link{link}, f.store.Links())
	require.Empty(t, f.audit.Records())
}

// concurrentDelete removes the link before its target lock returns, as a
// second request that deleted it and committed first would.
type concurrentDelete struct {
	*sharelinktest.Store
	link sharelink.Link
}

func (s concurrentDelete) LockTarget(ctx context.Context, entityType sharelink.EntityType, entityID string) (sharelink.Target, error) {
	if err := s.Store.DeleteLink(ctx, s.link); err != nil {
		return sharelink.Target{}, err
	}
	return s.Store.LockTarget(ctx, entityType, entityID)
}

func TestDeleteShareLinkAnswersALinkDeletedWhileItWaited(t *testing.T) {
	t.Parallel()
	store := sharelinktest.NewStore()
	labelID := uuid.NewString()
	store.SeedTarget(sharelink.EntityLabel, labelID, sharelink.Target{})
	link := store.SeedLink(sharelink.Link{Token: "raced", EntityType: sharelink.EntityLabel, EntityID: labelID, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)})
	f := newFixtureOver(t, store, concurrentDelete{Store: store, link: link})
	f.checker.Allow(policyv1.Label.ManageShareLinks(labelID))

	err := f.links.DeleteShareLink(callerContext(t), sharelink.DeleteShareLinkCommand{ID: link.ID})
	requireMessage(t, connect.CodeNotFound, "share link not found: "+link.ID, err)
	require.Empty(t, f.audit.Records())
}
