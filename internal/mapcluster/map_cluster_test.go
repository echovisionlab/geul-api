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

func TestMapProjectionMatchesMapLibreCSSPixels(t *testing.T) {
	t.Parallel()

	x, y := lngLatToWorldPixel(0, 0, 0)
	t.Logf("zoom 0 equator/origin world pixels: x=%v y=%v", x, y)
	assert.InDelta(t, 256, x, 0.000001)
	assert.InDelta(t, 256, y, 0.000001)

	// At zoom zero, 45 degrees is one eighth of MapLibre's 512px world.
	xEast, _ := lngLatToWorldPixel(45, 0, 0)
	assert.InDelta(t, 64, xEast-x, 0.000001)
	assert.InDelta(t, x, longitudeToWorldPixel(0, 0), 0.000001)
	assert.InDelta(t, y, latitudeToWorldPixel(0, 0), 0.000001)
}

func TestMapClusteringUsesGeographicCSSDistance(t *testing.T) {
	t.Parallel()

	type point struct{ lat, lng float64 }
	position := func(p point) (float64, float64) { return p.lat, p.lng }
	count := func(point) int32 { return 1 }
	parameters := MapClusterParameters{Zoom: 0, RadiusPx: 56, MinClusterPoints: 2}
	// Equatorial longitudes 45 degrees apart are 64 CSS pixels apart at zoom 0,
	// beyond the 56px clustering radius. The old 256px scale measured only 32px.
	separate := ClusterMapPlaceGroups([]point{{lng: 0}, {lng: 45}}, parameters, position, count)
	t.Logf("45-degree pair, zoom 0, radius 56px: components=%d", len(separate))
	assert.Len(t, separate, 2)

	nearby := ClusterMapPlaceGroups([]point{{lng: 0}, {lng: 22.5}}, parameters, position, count)
	require.Len(t, nearby, 1)
	assert.Equal(t, int32(2), nearby[0].Count)
}

func TestMapBreakoutZoomMatchesGeographicTargetRadius(t *testing.T) {
	t.Parallel()

	// 22.5 equatorial degrees span exactly 32 CSS pixels at zoom zero. Reaching
	// a 56px radius requires log2(56/32), then the existing 0.05 zoom padding.
	zoom := estimateClusterMinBreakoutZoom(0, 56, 0, 0, 22.5, 0)
	require.NotNil(t, zoom)
	t.Logf("22.5-degree pair, radius 56px: breakout zoom=%v", *zoom)
	assert.InDelta(t, 0.857354922057604, *zoom, 0.000001)
	assert.InDelta(t, 56*math.Pow(2, 0.05), 32*math.Pow(2, *zoom), 0.000001)
}
