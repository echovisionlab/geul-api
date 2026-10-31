package public

import (
	"context"
	"errors"
	"testing"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func newDryRunPublicServiceDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=localhost user=geul dbname=geul sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	require.NoError(t, err)
	return db
}

var errUnusedTestDependency = errors.New("test did not configure this public Program Event dependency")

type unusedAssets struct{}

func (unusedAssets) ResolveReadyAssetForSourceFile(context.Context, string, ...string) *commonv1.AssetRef {
	return nil
}

func (unusedAssets) ResolveReadyAssetsForSourceFiles(context.Context, []string, ...string) (map[string]*commonv1.AssetRef, error) {
	return nil, errUnusedTestDependency
}
