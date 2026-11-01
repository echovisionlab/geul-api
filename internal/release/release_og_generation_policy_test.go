package release_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	errs "github.com/echovisionlab/geul-api/internal/errors"

	"connectrpc.com/connect"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	"github.com/echovisionlab/geul-api/internal/og"
	ogpostgres "github.com/echovisionlab/geul-api/internal/og/postgres"
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
	store := ogpostgres.Bind(nil, ogpostgres.Ports{Requests: releaseOGResolverForTest()})
	_, err := og.ResolveRequests(t.Context(), store, "release", "release-1", og.Selection{Kind: og.SelectionPrimary})
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
}

func TestAutomaticReleaseOgGenerationIsDisabledBeforeDatabaseLookup(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	refresher := ogpostgres.NewRefresher(
		ogpostgres.NewPlanner("", releaseOGRenderConfig{}, postadapter.NewProjection()),
		releaseOGResolverForTest(),
	)
	_, err = refresher.RequestCurrentWithDB(
		t.Context(), db,
		managev1.OgEntityType_OG_ENTITY_TYPE_RELEASE,
		"release-1", "", false, "release_updated",
	)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
}

type releaseOGRenderConfig struct{}

func (releaseOGRenderConfig) Snapshot(context.Context, *gorm.DB, string) ([]byte, string, error) {
	payload := []byte(`{"site_title":""}`)
	return payload, fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func releaseOGResolverForTest() *ogpostgres.Resolver {
	return ogpostgres.NewResolver(postadapter.NewRequests())
}
