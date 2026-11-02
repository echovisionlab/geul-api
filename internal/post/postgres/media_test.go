package postpostgres

import (
	"context"
	"errors"
	"testing"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"
	optional "github.com/echovisionlab/geul-api/internal/optional"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postMediaLoaderFunc func(context.Context, *gorm.DB, uuid.UUID) ([]*fileref.BlockMediaItem, error)

func (f postMediaLoaderFunc) LoadContentBlockMediaReferences(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) ([]*fileref.BlockMediaItem, error) {
	return f(ctx, tx, documentID)
}

type postMediaHydratorFunc func(context.Context, *gorm.DB, string, uuid.UUID, *auth.UserInfo, []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error)

func (f postMediaHydratorFunc) HydrateAuthorizedPostBlockMediaWithDB(ctx context.Context, tx *gorm.DB, postID string, documentID uuid.UUID, principal *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
	return f(ctx, tx, postID, documentID, principal, items)
}

func TestPostMediaProjectionReturnsDeliveryForAuthorizedOwner(t *testing.T) {
	postID, documentID, fileID := uuid.NewString(), uuid.New(), uuid.NewString()
	tx := new(gorm.DB)
	principal := &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(uuid.NewString())}
	references := []*fileref.BlockMediaItem{{Attachment: &fileref.Attachment{Kind: fileref.AttachmentActive, FileID: fileID}}}
	delivery := &fileref.Delivery{}
	loader := postMediaLoaderFunc(func(_ context.Context, db *gorm.DB, id uuid.UUID) ([]*fileref.BlockMediaItem, error) {
		require.Same(t, tx, db)
		require.Equal(t, documentID, id)
		return references, nil
	})
	hydrator := postMediaHydratorFunc(func(ctx context.Context, db *gorm.DB, ownerID string, id uuid.UUID, user *auth.UserInfo, items []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
		require.Same(t, tx, db)
		require.Equal(t, postID, ownerID)
		require.Equal(t, documentID, id)
		require.Same(t, principal, user)
		require.Same(t, principal, auth.GetUser(ctx))
		require.Equal(t, references, items)
		return []*fileref.BlockMediaItem{{Attachment: items[0].Attachment, Delivery: delivery}}, nil
	})
	items, err := NewStore(tx, Ports{MediaLoader: loader, Media: hydrator}).BlockMedia(t.Context(), postID, documentID, principal)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, fileID, optional.Value(items[0]).Attachment.ActiveFileID())
	require.Same(t, delivery, items[0].Delivery)
	require.Nil(t, references[0].Delivery, "private delivery must not mutate durable references")
}

func TestPostMediaProjectionDoesNotReturnReferencesAfterDeliveryFailure(t *testing.T) {
	failure := errors.New("delivery unavailable")
	for _, failLoading := range []bool{false, true} {
		loader := postMediaLoaderFunc(func(context.Context, *gorm.DB, uuid.UUID) ([]*fileref.BlockMediaItem, error) {
			if failLoading {
				return nil, failure
			}
			return []*fileref.BlockMediaItem{{}}, nil
		})
		hydrator := postMediaHydratorFunc(func(context.Context, *gorm.DB, string, uuid.UUID, *auth.UserInfo, []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error) {
			require.False(t, failLoading, "failed reference reads must stop before signing")
			return nil, failure
		})
		items, err := NewStore(new(gorm.DB), Ports{MediaLoader: loader, Media: hydrator}).BlockMedia(t.Context(), uuid.NewString(), uuid.New(), new(auth.UserInfo))
		require.ErrorIs(t, err, failure)
		require.Nil(t, items)
	}
}
