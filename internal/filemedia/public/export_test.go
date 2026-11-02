package public

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

var (
	ExportJoinPublicCDNPath               = joinPublicCDNPath
	ExportPublicMediaSupportsInlineSource = publicMediaSupportsInlineSource
)

func ExportProjectReadyPublicAsset(cdn string, row ReadyAsset) (*mediaasset.Reference, error) {
	ref, err := projectReadyPublicAsset(cdn, row)
	return ref, err
}
func ExportResolvedOGAssetRef(ctx context.Context, store Store, cdn string, source, localized *string) (*mediaasset.Reference, error) {
	ref, err := resolvedOgAssetRef(ctx, store, cdn, source, localized)
	return ref, err
}
func ExportBuildPublicHLSRef(domain string, row ReadyGeneration) (*commonv1.HlsMediaRef, error) {
	ref, err := buildPublicHLSRef(domain, row)
	return hlsReferenceProto(ref), err
}

type TestMediaAccess struct{ DirectDraft bool }

func ExportProjectScopedMediaURL(cdn, domain, secret string, access TestMediaAccess, file MediaFile, derivatives map[string]Derivative, assets map[string]ReadyAsset, generations map[string]ReadyGeneration, asset ReadyAsset) (*commonv1.MediaDelivery, error) {
	result, err := projectScopedMediaURL(cdn, domain, secret, resolvedMediaAccess{directDraft: access.DirectDraft}, file, derivatives, assets, generations, asset)
	return DeliveryProto(result), err
}
