package memberadapter

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	memberdomain "github.com/echovisionlab/geul-api/internal/member"
)

// AvatarAssetPromoter is the File capability that promotes an uploaded
// avatar source to a public asset.
type AvatarAssetPromoter interface {
	PromoteUserAvatarAsset(ctx context.Context, fileID string) (*mediaasset.Reference, error)
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
	if asset == nil {
		return nil, nil
	}
	return &memberdomain.Asset{ID: asset.AssetID, URL: asset.URL, Extension: asset.Extension, MIMEType: asset.MimeType, FileSize: asset.FileSize, SHA256: append([]byte(nil), asset.SHA256...), Disposition: memberdomain.AssetDisposition(asset.Disposition)}, nil
}
