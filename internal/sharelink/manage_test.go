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

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type recordingAuthority struct {
	db           *gorm.DB
	listed       bool
	created      bool
	deleted      bool
	authorizeErr error
}

func (a *recordingAuthority) AuthorizeList(context.Context, managev1.ShareLinkEntityType, string) error {
	a.listed = true
	return a.authorizeErr
}

func (a *recordingAuthority) Create(ctx context.Context, _ managev1.ShareLinkEntityType, _ string, link *model.ShareLink, create CreateRecord) error {
	a.created = true
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return create(ctx, tx, link) })
}

func (a *recordingAuthority) Delete(ctx context.Context, _ managev1.ShareLinkEntityType, link model.ShareLink, deleteRecord DeleteRecord) error {
	a.deleted = true
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return deleteRecord(ctx, tx, link) })
}

func TestServicePersistsThroughTargetAuthority(t *testing.T) {
	t.Parallel()
	db := newManageDB(t)
	authority := &recordingAuthority{db: db}
	service := NewService(db, authority)
	entityID := uuid.NewString()
	created, err := service.CreateShareLink(t.Context(), connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
		EntityId:   entityID,
	}))
	require.NoError(t, err)
	require.True(t, authority.created)
	require.NotEmpty(t, created.Msg.ShareLink.Id)
	require.NotEmpty(t, created.Msg.ShareLink.Token)
	require.False(t, created.Msg.ShareLink.HasPassword)
	require.WithinDuration(t, time.Now().Add(defaultExpiration), created.Msg.ShareLink.ExpiresAt.AsTime(), 2*time.Second)

	listed, err := service.ListShareLinks(t.Context(), connect.NewRequest(&managev1.ListShareLinksRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PAGE,
		EntityId:   entityID,
	}))
	require.NoError(t, err)
	require.True(t, authority.listed)
	require.Len(t, listed.Msg.ShareLinks, 1)

	deleted, err := service.DeleteShareLink(t.Context(), connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: created.Msg.ShareLink.Id}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	require.True(t, authority.deleted)
	var count int64
	require.NoError(t, db.Model(&model.ShareLink{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestServiceRejectsInvalidExpirationBeforeAuthority(t *testing.T) {
	t.Parallel()
	db := newManageDB(t)
	authority := &recordingAuthority{db: db}
	service := NewService(db, authority)
	expired := timestamppb.New(time.Now().Add(-time.Minute))
	_, err := service.CreateShareLink(t.Context(), connect.NewRequest(&managev1.CreateShareLinkRequest{
		EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_POST,
		EntityId:   uuid.NewString(),
		ExpiresAt:  expired,
	}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.False(t, authority.created)
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
