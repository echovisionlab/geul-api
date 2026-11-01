package programeventpostgres

import (
	"context"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
)

// The public asset owners and bindings of event media and series posters.
const (
	eventOwnerType  = "program_event"
	seriesOwnerType = "program_event_series"
	mediaBinding    = "media"
	posterBinding   = "poster"
	posterKind      = "poster"
)

func mediaBindingKey(mediaID string) string {
	return mediaBinding + "_" + mediaID
}

func (s *store) LockAttachableFiles(ctx context.Context, fileIDs []string) error {
	return s.ports.Media.LockAttachableFilesForUpdate(ctx, s.tx, fileIDs)
}

func (s *store) BindPosterMedia(ctx context.Context, eventID, mediaID, fileID string) error {
	_, err := s.ports.Media.BindReadyAssetForSourceFile(ctx, s.tx, fileID, eventOwnerType, eventID, mediaBindingKey(mediaID), posterKind)
	return err
}

func (s *store) ReleaseMediaBinding(ctx context.Context, eventID, mediaID string) error {
	return s.ports.Media.ReleasePublicAssetBindings(ctx, s.tx, eventOwnerType, eventID, mediaBindingKey(mediaID))
}

func (s *store) ReleaseEventMedia(ctx context.Context, eventID string) error {
	return s.ports.Media.ReleasePublicAssetBindings(ctx, s.tx, eventOwnerType, eventID, mediaBinding)
}

func (s *store) BindSeriesPoster(ctx context.Context, seriesID, fileID string) error {
	_, err := s.ports.Media.BindReadyAssetForSourceFile(ctx, s.tx, fileID, seriesOwnerType, seriesID, posterBinding, posterKind)
	return err
}

// ReleaseSeriesPoster answers a missing row as a missing series, as a
// series deletion always did.
func (s *store) ReleaseSeriesPoster(ctx context.Context, seriesID string) error {
	return seriesNotFound(s.ports.Media.ReleasePublicAssetBindings(ctx, s.tx, seriesOwnerType, seriesID, posterBinding))
}

func (s *store) ReadyImageAsset(ctx context.Context, fileID string) (*commonv1.AssetRef, error) {
	return s.ports.Media.ReadyPublicAssetRefForSourceFile(ctx, s.tx, fileID, "image")
}

func (s *store) InlineImageAsset(ctx context.Context, fileID string) (*commonv1.AssetRef, error) {
	return s.ports.Media.ResolveSingleReadyInlineAssetForSourceFile(ctx, s.tx, fileID, "image")
}

func (s *store) HydrateBlockMedia(ctx context.Context, eventID string, documentID uuid.UUID, principal *auth.UserInfo, items []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error) {
	return s.ports.Files.HydrateAuthorizedProgramEventBlockMediaWithDB(ctx, s.tx, eventID, documentID, principal, items)
}
