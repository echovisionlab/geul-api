package public

import (
	"context"
	"errors"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

var errUnusedTestDependency = errors.New("test did not configure this public Program Event dependency")

type unusedAssets struct{}

func (unusedAssets) ResolveReadyAssetForSourceFile(context.Context, string, ...string) *commonv1.AssetRef {
	return nil
}

func (unusedAssets) ResolveReadyAssetsForSourceFiles(context.Context, []string, ...string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}
