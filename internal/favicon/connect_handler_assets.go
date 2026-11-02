package favicon

import (
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func SetProto(set *Set) *commonv1.FaviconAssetSet {
	if set == nil {
		return nil
	}
	return &commonv1.FaviconAssetSet{
		IconIco:            mediaasset.ReferenceProto(set.IconICO),
		IconPng_16:         mediaasset.ReferenceProto(set.IconPNG16),
		IconPng_32:         mediaasset.ReferenceProto(set.IconPNG32),
		IconPng_48:         mediaasset.ReferenceProto(set.IconPNG48),
		AppleTouchIcon_180: mediaasset.ReferenceProto(set.AppleTouch180),
		ManifestIcon_192:   mediaasset.ReferenceProto(set.Manifest192),
		ManifestIcon_512:   mediaasset.ReferenceProto(set.Manifest512),
		IconSvg:            mediaasset.ReferenceProto(set.IconSVG),
	}
}
