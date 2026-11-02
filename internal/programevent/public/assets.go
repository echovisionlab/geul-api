package public

import (
	"context"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
)

// Assets resolves ready public assets after Program Event visibility policy
// has selected the exact source File IDs.
type Assets interface {
	ResolveReadyAssetForSourceFile(context.Context, string, ...string) *mediaasset.Reference
	ResolveReadyAssetsForSourceFiles(context.Context, []string, ...string) (map[string]*mediaasset.Reference, error)
}
