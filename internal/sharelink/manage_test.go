package sharelink

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingAuthority struct {
	listed       bool
	locked       bool
	authorizeErr error
	lockErr      error
}

func (a *recordingAuthority) AuthorizeList(context.Context, EntityType, string) error {
	a.listed = true
	return a.authorizeErr
}

func (a *recordingAuthority) LockTarget(context.Context, *gorm.DB, EntityType, string) (LockedTarget, error) {
	a.locked = true
	return LockedTarget{}, a.lockErr
}

func newTestHandler(t *testing.T, db *gorm.DB, targets TargetAuthority) *ConnectHandler {
	t.Helper()
	service, err := NewService(Deps{DB: db, SpiceDB: &auth.SpiceDBClient{}, Targets: targets, Audit: &audittest.Recorder{}})
	require.NoError(t, err)
	return NewConnectHandler(service)
}

func TestServiceListsThroughTargetAuthority(t *testing.T) {
	t.Parallel()
	db := newManageDB(t)
	authority := &recordingAuthority{}
	handler := newTestHandler(t, db, authority)
	entityID := uuid.NewString()
	other := uuid.NewString()
	expiresAt := time.Now().Add(time.Hour)
	require.NoError(t, db.Create(&model.ShareLink{Token: "listed", EntityType: string(EntityPage), EntityID: entityID, CreatedAt: time.Now(), ExpiresAt: &expiresAt}).Error)
	require.NoError(t, db.Create(&model.ShareLink{Token: "other", EntityType: string(EntityPage), EntityID: other, CreatedAt: time.Now(), ExpiresAt: &expiresAt}).Error)

	listed, err := handler.ListShareLinks(t.Context(), connect.NewRequest(&managev1.ListShareLinksRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
		EntityId:   entityID,
	}))
	require.NoError(t, err)
	require.True(t, authority.listed)
	require.Len(t, listed.Msg.ShareLinks, 1)
	require.Equal(t, "listed", listed.Msg.ShareLinks[0].Token)
	require.Equal(t, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE, listed.Msg.ShareLinks[0].EntityType)
}

func TestServiceRejectsAnUnspecifiedTargetByName(t *testing.T) {
	t.Parallel()
	authority := &recordingAuthority{}
	_, err := newTestHandler(t, newManageDB(t), authority).ListShareLinks(t.Context(), connect.NewRequest(&managev1.ListShareLinksRequest{EntityId: uuid.NewString()}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.ErrorContains(t, err, "unsupported entity type: SHARE_LINK_ENTITY_TYPE_UNSPECIFIED")
	require.False(t, authority.listed)
}

func TestServiceRejectsInvalidExpirationBeforeAuthority(t *testing.T) {
	t.Parallel()
	authority := &recordingAuthority{}
	handler := newTestHandler(t, newManageDB(t), authority)
	expired := timestamppb.New(time.Now().Add(-time.Minute))
	_, err := handler.CreateShareLink(t.Context(), connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST,
		EntityId:   uuid.NewString(),
		ExpiresAt:  expired,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.False(t, authority.locked)
}

func newManageDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE share_link (
		id TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
		token TEXT NOT NULL UNIQUE,
		entity_type TEXT NOT NULL,
		entity_id TEXT NOT NULL,
		label TEXT,
		password_hash TEXT,
		expires_at DATETIME NOT NULL,
		created_at DATETIME NOT NULL
	)`).Error)
	return db
}
