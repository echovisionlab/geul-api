package sharelink_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/sharelink"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestConnectHandlerRendersTheStoredLinks(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	artistID := f.target(sharelink.EntityArtist, sharelink.Target{})
	f.checker.Allow(policyv1.Artist.ManageShareLinks(artistID))
	created, expires := time.Date(2026, 11, 1, 9, 30, 0, 123000, time.UTC), time.Date(2026, 11, 8, 9, 30, 0, 0, time.UTC)
	label, hash := "press", "hash"
	link := f.store.SeedLink(sharelink.Link{
		Token: "token-1", EntityType: sharelink.EntityArtist, EntityID: artistID, Label: &label, PasswordHash: &hash, CreatedAt: created, ExpiresAt: expires,
	})
	handler := sharelink.NewConnectHandler(f.links)

	listed, err := handler.ListShareLinks(callerContext(t), connect.NewRequest(&managev1.ListShareLinksRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST, EntityId: artistID,
	}))
	require.NoError(t, err)
	want := &managev1.ListShareLinksResponse{ShareLinks: []*managev1.ShareLinkItem{{
		Id: link.ID, EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_ARTIST, EntityId: artistID,
		Token: "token-1", Url: "/s/token-1", Label: &label, HasPassword: true,
		CreatedAt: timestamppb.New(created), ExpiresAt: timestamppb.New(expires),
	}}}
	require.True(t, proto.Equal(want, listed.Msg), "got %v", listed.Msg)

	deleted, err := handler.DeleteShareLink(callerContext(t), connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: link.ID}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
}
