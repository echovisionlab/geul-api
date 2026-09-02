package translation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildBundlesSplitsProviderLimits(t *testing.T) {
	t.Parallel()

	units := []Unit{
		{UnitID: "entity:title", ContainerType: ContainerTypeEntity, SourceText: "Title"},
		{UnitID: "entity:summary", ContainerType: ContainerTypeEntity, SourceText: strings.Repeat("a", 7000)},
		{UnitID: "entity:content_text", ContainerType: ContainerTypeEntity, SourceText: strings.Repeat("b", 7000)},
	}
	bundles := BuildBundles("series", "series-1", "en", "ko", units, nil)
	require.Len(t, bundles, 2)
	assert.Equal(t, "entity:meta", bundles[0].BundleID)
	assert.Equal(t, "entity:meta:chunk:2", bundles[1].BundleID)
	assert.Len(t, bundles[0].Units, 2)
	assert.Len(t, bundles[1].Units, 1)
	assert.Equal(t, 0, bundles[0].SequenceIndex)
	assert.Equal(t, 2, bundles[0].SequenceTotal)
	assert.Equal(t, 1, bundles[1].SequenceIndex)
	assert.Equal(t, 2, bundles[1].SequenceTotal)
}
