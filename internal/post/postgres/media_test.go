package postpostgres

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type postMediaLoaderFunc func(context.Context, *gorm.DB, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error)

func (f postMediaLoaderFunc) LoadContentBlockMediaReferences(ctx context.Context, tx *gorm.DB, documentID uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
	return f(ctx, tx, documentID)
}

type postMediaHydratorFunc func(context.Context, *gorm.DB, string, uuid.UUID, *auth.UserInfo, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)

func (f postMediaHydratorFunc) HydrateAuthorizedPostBlockMediaWithDB(ctx context.Context, tx *gorm.DB, postID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return f(ctx, tx, postID, documentID, principal, items)
}

func TestPostMediaProjectionReturnsDeliveryForAuthorizedOwner(t *testing.T) {
	postID, documentID, fileID := uuid.NewString(), uuid.New(), uuid.NewString()
	tx := new(gorm.DB)
	principal := &auth.UserInfo{Authenticated: true, MemberID: auth.MemberID(uuid.NewString())}
	references := []*contentv1.ContentBlockMediaItem{{Attachment: &contentv1.FileAttachment{State: &contentv1.FileAttachment_ActiveFileId{ActiveFileId: fileID}}}}
	delivery := &commonv1.MediaDelivery{}
	loader := postMediaLoaderFunc(func(_ context.Context, db *gorm.DB, id uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
		require.Same(t, tx, db)
		require.Equal(t, documentID, id)
		return references, nil
	})
	hydrator := postMediaHydratorFunc(func(ctx context.Context, db *gorm.DB, ownerID string, id uuid.UUID, user *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
		require.Same(t, tx, db)
		require.Equal(t, postID, ownerID)
		require.Equal(t, documentID, id)
		require.Same(t, principal, user)
		require.Same(t, principal, auth.GetUser(ctx))
		require.Equal(t, references, items)
		return []*contentv1.ContentBlockMediaItem{{Attachment: items[0].Attachment, Delivery: delivery}}, nil
	})
	items, err := Bind(tx, Deps{MediaLoader: loader, Media: hydrator}).BlockMedia(t.Context(), postID, documentID, principal)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, fileID, items[0].GetAttachment().GetActiveFileId())
	require.Same(t, delivery, items[0].Delivery)
	require.Nil(t, references[0].Delivery, "private delivery must not mutate durable references")
}

func TestPostMediaProjectionDoesNotReturnReferencesAfterDeliveryFailure(t *testing.T) {
	failure := errors.New("delivery unavailable")
	for _, failLoading := range []bool{false, true} {
		loader := postMediaLoaderFunc(func(context.Context, *gorm.DB, uuid.UUID) ([]*contentv1.ContentBlockMediaItem, error) {
			if failLoading {
				return nil, failure
			}
			return []*contentv1.ContentBlockMediaItem{{}}, nil
		})
		hydrator := postMediaHydratorFunc(func(context.Context, *gorm.DB, string, uuid.UUID, *auth.UserInfo, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
			require.False(t, failLoading, "failed reference reads must stop before signing")
			return nil, failure
		})
		items, err := Bind(new(gorm.DB), Deps{MediaLoader: loader, Media: hydrator}).BlockMedia(t.Context(), uuid.NewString(), uuid.New(), new(auth.UserInfo))
		require.ErrorIs(t, err, failure)
		require.Nil(t, items)
	}
}
