package public

import (
	"context"

	fileref "github.com/echovisionlab/geul-api/internal/fileref"

	"github.com/echovisionlab/geul-api/internal/mediaasset"
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
func ExportBuildPublicHLSRef(domain string, row ReadyGeneration) (*fileref.HLSReference, error) {
	ref, err := buildPublicHLSRef(domain, row)
	return ref, err
}

type TestMediaAccess struct{ DirectDraft bool }

func ExportProjectScopedMediaURL(cdn, domain, secret string, access TestMediaAccess, file MediaFile, derivatives map[string]Derivative, assets map[string]ReadyAsset, generations map[string]ReadyGeneration, asset ReadyAsset) (*fileref.Delivery, error) {
	result, err := projectScopedMediaURL(cdn, domain, secret, resolvedMediaAccess{directDraft: access.DirectDraft}, file, derivatives, assets, generations, asset)
	return result, err
}
