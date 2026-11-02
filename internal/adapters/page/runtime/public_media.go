package runtime

import (
	"context"
	"strings"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"

	publicpage "github.com/echovisionlab/geul-api/internal/page/public"
)

type PublicFiles interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*fileref.BlockMediaItem) ([]*fileref.BlockMediaItem, error)
	ResolveDisplayMedia(context.Context, []string) (map[string]*fileref.Delivery, error)
	ResolveOGAsset(context.Context, *string, *string) (*mediaasset.Reference, error)
}

// PublicMedia adapts shared public File projection to Page-owned media ports.
type PublicMedia struct{ files PublicFiles }

func NewPublicMedia(files PublicFiles) *PublicMedia {
	if files == nil {
		panic("Page public media dependency is required")
	}
	return &PublicMedia{files: files}
}

func (a *PublicMedia) HydrateAuthorizedContentBlockMedia(
	ctx context.Context,
	items []*fileref.BlockMediaItem,
) ([]*fileref.BlockMediaItem, error) {
	return a.files.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func (a *PublicMedia) ResolvePageFeaturedImageDelivery(
	ctx context.Context,
	fileID *string,
) (*fileref.Delivery, error) {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil, nil
	}
	fileIDValue := strings.TrimSpace(*fileID)
	deliveries, err := a.files.ResolveDisplayMedia(ctx, []string{fileIDValue})
	if err != nil {
		return nil, err
	}
	delivery := deliveries[fileIDValue]
	if delivery == nil {
		return nil, nil
	}
	result := delivery.Clone()
	result.Inline = nil
	result.Download = nil
	return result, nil
}

func (a *PublicMedia) ResolvePageOGAsset(
	ctx context.Context,
	sourceAssetID *string,
	localizedAssetID *string,
) (*mediaasset.Reference, error) {
	return a.files.ResolveOGAsset(ctx, sourceAssetID, localizedAssetID)
}

var _ publicpage.MediaResolver = (*PublicMedia)(nil)
