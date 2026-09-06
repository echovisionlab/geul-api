package release_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestReleaseOgPolicyRetainsLegacyIdentityWithoutNewGeneration(t *testing.T) {
	policy, ok := og.PolicyForEntityType(managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE)
	require.True(t, ok)
	assert.Equal(t, "release", policy.Name)
	assert.True(t, policy.LegacyOnly)
	assert.False(t, og.SupportsNewGeneration(policy))
	assert.Equal(t, managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE, og.EntityTypeForName("release"))
}

func TestManualReleaseOgGenerationIsDisabledBeforeDatabaseLookup(t *testing.T) {
	_, err := releaseOGResolverForTest().Resolve(t.Context(), nil, &managev1.RegenerateOgImageRequest{
		EntityType: managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE,
		EntityId:   new("release-1"),
		Selection: &managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
			Primary: &managev1.OgPrimaryTarget{},
		}},
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestAutomaticReleaseOgGenerationIsDisabledBeforeDatabaseLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	refresher := og.NewRefresher(
		og.NewPlanner(db, "", releaseOGRenderConfig{}, postadapter.NewProjection()),
		releaseOGResolverForTest(),
	)
	_, err = refresher.RequestCurrentWithDB(
		t.Context(), db,
		managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE,
		"release-1", "", false, "release_updated",
	)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

type releaseOGRenderConfig struct{}

func (releaseOGRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":""}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func releaseOGResolverForTest() *og.Resolver {
	return og.NewResolver(postadapter.NewRequests())
}
