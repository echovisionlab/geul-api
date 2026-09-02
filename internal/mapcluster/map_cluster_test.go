package mapcluster

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapClusterRadiusUsesZoomAndViewportBreakpoints(t *testing.T) {
	t.Parallel()

	assert.Equal(t, float64(56), getBaseClusterRadiusPxForZoom(2.4))
	assert.Equal(t, float64(40), getBaseClusterRadiusPxForZoom(2.5))
	assert.Equal(t, float64(32), getBaseClusterRadiusPxForZoom(3.5))
	assert.Equal(t, float64(24), getBaseClusterRadiusPxForZoom(5))
	assert.Equal(t, float64(18), getBaseClusterRadiusPxForZoom(7))
	assert.Equal(t, float64(14), getBaseClusterRadiusPxForZoom(9))

	assert.Equal(t, float64(18), DefaultMapClusterRadiusPxForZoom(9, 390))
	assert.Equal(t, float64(20), DefaultMapClusterRadiusPxForZoom(9, 700))
	assert.Equal(t, float64(14), DefaultMapClusterRadiusPxForZoom(9, 1280))
	assert.Equal(t, float64(14), DefaultMapClusterRadiusPxForZoom(9, 0))
}

func TestRoundFloatRoundsPositiveAndNegativeValues(t *testing.T) {
	t.Parallel()

	assert.Equal(t, float64(2), roundFloat(1.5))
	assert.Equal(t, float64(1), roundFloat(1.49))
	assert.Equal(t, float64(-2), roundFloat(-1.5))
	assert.Equal(t, float64(-1), roundFloat(-1.49))
}

func TestEstimateClusterMinBreakoutZoomHandlesInvalidAndSmallExtents(t *testing.T) {
	t.Parallel()

	assert.Nil(t, estimateClusterMinBreakoutZoom(4, 0, 126.9, 37.5, 127.0, 37.6))
	assert.Nil(t, estimateClusterMinBreakoutZoom(4, 24, 127.0, 37.5, 127.0, 37.5))
	assert.Nil(t, estimateClusterMinBreakoutZoom(math.Inf(1), 24, 126.9, 37.5, 127.0, 37.6))

	zoom := estimateClusterMinBreakoutZoom(4, 24, 126.9, 37.5, 127.0, 37.6)
	require.NotNil(t, zoom)
	assert.GreaterOrEqual(t, *zoom, 4+minClusterBreakoutZoomDelta+clusterBreakoutZoomPadding)

	minimumBoundedZoom := estimateClusterMinBreakoutZoom(10, 1, 126.0, 37.0, 128.0, 39.0)
	require.NotNil(t, minimumBoundedZoom)
	assert.InDelta(t, 10+minClusterBreakoutZoomDelta+clusterBreakoutZoomPadding, *minimumBoundedZoom, 0.000001)
}
