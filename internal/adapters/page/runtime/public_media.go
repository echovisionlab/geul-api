package runtime

import (
	"context"
	"strings"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"

	publicpage "github.com/echovisionlab/geul-api/internal/page/public"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"

	contentv1 "github.com/echovisionlab/geul-event-contracts/gen/api/content/v1"
	"google.golang.org/protobuf/proto"
)

type PublicFiles interface {
	HydrateAuthorizedContentBlockMedia(context.Context, []*contentv1.ContentBlockMediaItem) ([]*contentv1.ContentBlockMediaItem, error)
	ResolvePublicDisplayMedia(context.Context, []string) (map[string]*commonv1.MediaDelivery, error)
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
	items []*contentv1.ContentBlockMediaItem,
) ([]*contentv1.ContentBlockMediaItem, error) {
	return a.files.HydrateAuthorizedContentBlockMedia(ctx, items)
}

func (a *PublicMedia) ResolvePageFeaturedImageDelivery(
	ctx context.Context,
	fileID *string,
) (*commonv1.MediaDelivery, error) {
	if fileID == nil || strings.TrimSpace(*fileID) == "" {
		return nil, nil
	}
	fileIDValue := strings.TrimSpace(*fileID)
	deliveries, err := a.files.ResolvePublicDisplayMedia(ctx, []string{fileIDValue})
	if err != nil {
		return nil, err
	}
	delivery := deliveries[fileIDValue]
	if delivery == nil {
		return nil, nil
	}
	result := proto.Clone(delivery).(*commonv1.MediaDelivery)
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
