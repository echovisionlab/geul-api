package public

import (
	"context"
	"errors"

	mediaasset "github.com/echovisionlab/geul-api/internal/mediaasset"
)

var errUnusedTestDependency = errors.New("test did not configure this public Program Event dependency")

type unusedAssets struct{}

func (unusedAssets) ResolveReadyAssetForSourceFile(context.Context, string, ...string) *mediaasset.Reference {
	return nil
}

func (unusedAssets) ResolveReadyAssetsForSourceFiles(context.Context, []string, ...string) (map[string]*mediaasset.Reference, error) {
	return nil, errUnusedTestDependency
}
