package public

import (
	"context"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// Assets resolves ready public assets after Program Event visibility policy
// has selected the exact source File IDs.
type Assets interface {
	ResolveReadyAssetForSourceFile(context.Context, string, ...string) *commonv1.AssetRef
	ResolveReadyAssetsForSourceFiles(context.Context, []string, ...string) (map[string]*commonv1.AssetRef, error)
}
