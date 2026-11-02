package labelpostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/label"
	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
)

// The logo bindings of a Label's public assets.
const (
	labelOwnerType   = "label"
	lightLogoBinding = "logo_light"
	darkLogoBinding  = "logo_dark"
	logoAssetKind    = "logo"
)

func logoBinding(variant label.LogoVariant) string {
	if variant == label.LogoVariantDark {
		return darkLogoBinding
	}
	return lightLogoBinding
}

// The media runtime answers in gorm's errors. A logo mutation answers every
// missing row as a missing Label, as it always did, so the not-found error
// of a runtime call carries label.ErrLabelNotFound.

func (s *store) LockAttachableFiles(ctx context.Context, fileIDs []string) error {
	return labelNotFound(s.ports.Runtime.LockAttachableFilesForUpdate(ctx, s.tx, fileIDs))
}

func (s *store) BindLogo(ctx context.Context, fileID, labelID string, variant label.LogoVariant) error {
	_, err := s.ports.Runtime.BindReadyAssetForSourceFile(ctx, s.tx, fileID, labelOwnerType, labelID, logoBinding(variant), logoAssetKind)
	return labelNotFound(err)
}

func (s *store) ReleaseLogoBinding(ctx context.Context, labelID string, variant label.LogoVariant) error {
	return labelNotFound(s.ports.Runtime.ReleasePublicAssetBindings(ctx, s.tx, labelOwnerType, labelID, logoBinding(variant)))
}

func (s *store) ReleaseLogoBindings(ctx context.Context, labelID string) error {
	return labelNotFound(s.ports.Runtime.ReleaseExactPublicAssetBindings(ctx, s.tx, labelOwnerType, labelID, []string{lightLogoBinding, darkLogoBinding}))
}

func (s *store) ReadyAssets(ctx context.Context, assetIDs []string) (map[string]*mediaasset.Reference, error) {
	return s.ports.Runtime.ResolveReadyAssetRefs(ctx, s.tx, assetIDs)
}

func (s *store) ReadyLogoAssets(ctx context.Context, fileIDs []string) (map[string]*mediaasset.Reference, error) {
	return s.ports.Runtime.ResolveReadySourceFileRefs(ctx, s.tx, fileIDs, logoAssetKind)
}

func (s *store) RequestOG(ctx context.Context, labelID, reason string) (string, error) {
	runID, err := s.ports.Runtime.RequestCurrentWithDB(ctx, s.tx, labelID, reason)
	return runID, labelNotFound(err)
}

func (s *store) CancelOG(ctx context.Context, labelID string) error {
	return labelNotFound(s.ports.Runtime.CancelAndReleaseOGWithDB(ctx, s.tx, labelID))
}
