package filemedia

import (
	"context"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

type UserAvatarAssetPromoter interface {
	PromoteUserAvatarAsset(ctx context.Context, sourceFileID string) (*commonv1.AssetRef, error)
}

func (s *FileService) PromoteUserAvatarAsset(ctx context.Context, sourceFileID string) (*commonv1.AssetRef, error) {
	return s.promoteSourceFileToPublicAsset(ctx, sourceFileID, "avatar")
}
