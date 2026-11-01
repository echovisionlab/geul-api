package memberadapter

import (
	"context"

	memberdomain "github.com/echovisionlab/geul-api/internal/member"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// AvatarAssetPromoter is the File capability that promotes an uploaded
// avatar source to a public asset.
type AvatarAssetPromoter interface {
	PromoteUserAvatarAsset(ctx context.Context, fileID string) (*commonv1.AssetRef, error)
}

// AvatarFiles answers the Member avatar port with the promoted asset in the
// Member's own terms.
type AvatarFiles struct {
	Files AvatarAssetPromoter
}

var _ memberdomain.UserAvatarAssetPromoter = AvatarFiles{}

func (a AvatarFiles) PromoteUserAvatarAsset(ctx context.Context, fileID string) (*memberdomain.Asset, error) {
	asset, err := a.Files.PromoteUserAvatarAsset(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return memberdomain.AssetFromMessage(asset), nil
}
