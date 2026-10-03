package public

import (
	"math"
	"testing"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/mapcluster"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeWorkMapViewportRequiresBounds(t *testing.T) {
	t.Parallel()

	_, err := normalizeWorkMapViewport(nil)
	require.Error(t, err)

	_, err = normalizeWorkMapViewport(&openv1.WorkMapViewport{})
	require.Error(t, err)
}

func TestNormalizeWorkMapViewportTreatsWorldWrappingWidthAsFullLongitude(t *testing.T) {
	t.Parallel()

	viewport, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
		Bounds: &openv1.WorkMapBounds{
			West:  -54.67106,
			South: -61.14290,
			East:  54.67106,
			North: 61.64470,
		},
		Zoom:             1.5,
		WidthPx:          1888,
		HeightPx:         630,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	})
	if err != nil {
		t.Fatalf("normalizeWorkMapViewport returned error: %v", err)
	}

	if !viewport.FullLongitude {
		t.Fatalf("expected world-wrapping viewport to mark FullLongitude")
	}
	if viewport.West != -180 || viewport.East != 180 {
		t.Fatalf("expected full-world longitude bounds, got west=%v east=%v", viewport.West, viewport.East)
	}
}

func TestClusterWorkMapPlaceGroupsKeepsSameCoordinateGroupsAsItemsAtHighZoom(t *testing.T) {
	t.Parallel()

	groups := []*workMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-1", PrimaryWorkTitle: "Work 1"},
		{PlaceID: "place-2", Name: "B", Address: "B", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-2", PrimaryWorkTitle: "Work 2"},
		{PlaceID: "place-3", Name: "C", Address: "C", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-3", PrimaryWorkTitle: "Work 3"},
	}

	clusters, items := clusterWorkMapPlaceGroups(groups, normalizedWorkMapViewport{
		Zoom:             9,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	})

	if len(clusters) != 0 {
		t.Fatalf("expected no clusters at high zoom for identical coordinates, got %d", len(clusters))
	}
	if len(items) != len(groups) {
		t.Fatalf("expected %d items at high zoom for identical coordinates, got %d", len(groups), len(items))
	}
}

func TestClusterWorkMapPlaceGroupsStillClustersSameCoordinateGroupsAtLowZoom(t *testing.T) {
	t.Parallel()

	groups := []*workMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-1", PrimaryWorkTitle: "Work 1"},
		{PlaceID: "place-2", Name: "B", Address: "B", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-2", PrimaryWorkTitle: "Work 2"},
		{PlaceID: "place-3", Name: "C", Address: "C", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-3", PrimaryWorkTitle: "Work 3"},
	}

	clusters, items := clusterWorkMapPlaceGroups(groups, normalizedWorkMapViewport{
		Zoom:             4,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	})

	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster at low zoom for identical coordinates, got %d", len(clusters))
	}
	if len(items) != 0 {
		t.Fatalf("expected no items at low zoom for identical coordinates, got %d", len(items))
	}
	if clusters[0].GetMinBreakoutZoom() != mapcluster.MapClusterSameCoordinateBreakoutZoom {
		t.Fatalf(
			"expected min_breakout_zoom=%v, got %v",
			mapcluster.MapClusterSameCoordinateBreakoutZoom,
			clusters[0].GetMinBreakoutZoom(),
		)
	}
}

func TestClusterWorkMapPlaceGroupsProvideBreakoutZoomForDistinctCoordinates(t *testing.T) {
	t.Parallel()

	groups := []*workMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5665, Lng: 126.9780, WorkCount: 1, PrimaryWorkID: "work-1", PrimaryWorkTitle: "Work 1"},
		{PlaceID: "place-2", Name: "B", Address: "B", Lat: 37.5676, Lng: 126.9792, WorkCount: 1, PrimaryWorkID: "work-2", PrimaryWorkTitle: "Work 2"},
		{PlaceID: "place-3", Name: "C", Address: "C", Lat: 37.5654, Lng: 126.9768, WorkCount: 1, PrimaryWorkID: "work-3", PrimaryWorkTitle: "Work 3"},
	}

	clusters, items := clusterWorkMapPlaceGroups(groups, normalizedWorkMapViewport{
		Zoom:             4,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	})

	if len(clusters) != 1 {
		t.Fatalf("expected 1 cluster for nearby coordinates, got %d", len(clusters))
	}
	if len(items) != 0 {
		t.Fatalf("expected no standalone items for nearby coordinates, got %d", len(items))
	}

	breakoutZoom := clusters[0].GetMinBreakoutZoom()
	if breakoutZoom <= 4 {
		t.Fatalf("expected breakout zoom above current zoom, got %v", breakoutZoom)
	}
	if clusters[0].Bounds.GetSouth() >= clusters[0].Bounds.GetNorth() {
		t.Fatalf("expected cluster bounds to track min/max latitude, got %+v", clusters[0].Bounds)
	}
	if clusters[0].Bounds.GetWest() >= clusters[0].Bounds.GetEast() {
		t.Fatalf("expected cluster bounds to track min/max longitude, got %+v", clusters[0].Bounds)
	}
}

func TestClusterWorkMapPlaceGroupsReturnsItemsWhenBelowMinimumClusterPoints(t *testing.T) {
	t.Parallel()

	groups := []*workMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5, Lng: 127.0, WorkCount: 1, PrimaryWorkID: "work-1", PrimaryWorkTitle: "Work 1"},
	}

	clusters, items := clusterWorkMapPlaceGroups(groups, normalizedWorkMapViewport{
		Zoom:             4,
		ClusterRadiusPx:  56,
		MinClusterPoints: 2,
	})

	if len(clusters) != 0 {
		t.Fatalf("expected no clusters below min cluster points, got %d", len(clusters))
	}
	if len(items) != 1 {
		t.Fatalf("expected one standalone item below min cluster points, got %d", len(items))
	}
	if items[0].PlaceId != "place-1" || items[0].PrimaryWorkId != "work-1" {
		t.Fatalf("unexpected standalone item: %+v", items[0])
	}
}

func TestNormalizeWorkMapViewportUsesSharedClusterFallbacks(t *testing.T) {
	t.Parallel()

	viewport, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
		Bounds: &openv1.WorkMapBounds{
			West:  -54.67106,
			South: -61.14290,
			East:  54.67106,
			North: 61.64470,
		},
		Zoom:             1.5,
		WidthPx:          390,
		HeightPx:         219,
		ClusterRadiusPx:  0,
		MinClusterPoints: 0,
	})
	if err != nil {
		t.Fatalf("normalizeWorkMapViewport returned error: %v", err)
	}

	if viewport.ClusterRadiusPx != 36 {
		t.Fatalf("expected shared mobile fallback radius 36, got %v", viewport.ClusterRadiusPx)
	}
	if viewport.MinClusterPoints != mapcluster.MapClusterDefaultMinPoints {
		t.Fatalf(
			"expected shared default min cluster points %d, got %d",
			mapcluster.MapClusterDefaultMinPoints,
			viewport.MinClusterPoints,
		)
	}
}

func TestNormalizeWorkMapViewportClampsLatitudeAndWrapsLongitude(t *testing.T) {
	t.Parallel()

	viewport, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
		Bounds: &openv1.WorkMapBounds{
			West:  -540,
			South: 120,
			East:  540,
			North: -120,
		},
		WidthPx:  800,
		HeightPx: 600,
	})
	require.NoError(t, err)

	assert.Equal(t, float64(-85), viewport.South)
	assert.Equal(t, float64(85), viewport.North)
	assert.Equal(t, float64(-180), viewport.West)
	assert.Equal(t, float64(180), viewport.East)
	// With explicit positive dimensions, an omitted proto scalar zoom denotes
	// the valid MapLibre zoom zero, rather than the legacy missing-size default.
	assert.Equal(t, float64(0), viewport.Zoom)
	assert.True(t, viewport.FullLongitude)
}

func TestNormalizeLongitudeAndClampHelpers(t *testing.T) {
	t.Parallel()

	assert.Equal(t, float64(170), mapcluster.NormalizeLongitude(530))
	assert.Equal(t, float64(-170), mapcluster.NormalizeLongitude(-530))
	assert.Equal(t, float64(42), mapcluster.NormalizeLongitude(42))

	assert.Equal(t, float64(-10), mapcluster.Clamp(-20, -10, 10))
	assert.Equal(t, float64(10), mapcluster.Clamp(20, -10, 10))
	assert.Equal(t, float64(5), mapcluster.Clamp(5, -10, 10))
}

func TestNormalizeWorkMapViewportUsesMapLibreWorldSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		zoom  float64
		width int32
		full  bool
	}{
		{name: "zoom zero narrow", zoom: 0, width: 320, full: false},
		{name: "zoom zero full world", zoom: 0, width: 512, full: true},
		{name: "zoom one narrow", zoom: 1, width: 800, full: false},
		{name: "zoom one full world", zoom: 1, width: 1024, full: true},
		{name: "fractional zoom narrow", zoom: 1.5, width: 1280, full: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			viewport, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
				Bounds: &openv1.WorkMapBounds{West: -100, East: 100, South: -60, North: 60},
				Zoom:   test.zoom, WidthPx: test.width, HeightPx: 256,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("requested zoom=%v width=%d: normalized zoom=%v fullLongitude=%v", test.zoom, test.width, viewport.Zoom, viewport.FullLongitude)
			if viewport.Zoom != test.zoom || viewport.FullLongitude != test.full {
				t.Fatalf("zoom=%v FullLongitude=%v; want zoom=%v FullLongitude=%v", viewport.Zoom, viewport.FullLongitude, test.zoom, test.full)
			}
			if !test.full && (viewport.West != -100 || viewport.East != 100) {
				t.Fatalf("narrow viewport lost geographic bounds: %+v", viewport)
			}
			if viewport.FullLatitude {
				t.Fatal("256px height does not cover the zoom-zero 512px world")
			}
		})
	}
}

func TestNormalizeWorkMapViewportPreservesLegacyZoomDefaults(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name          string
		zoom          float64
		width, height int32
	}{
		{name: "omitted dimensions"},
		{name: "missing width", height: 320},
		{name: "missing height", width: 320},
		{name: "below minimum zoom", zoom: -2.01, width: 320, height: 320},
		{name: "negative zoom missing width", zoom: -1, height: 320},
		{name: "negative zoom missing height", zoom: -0.5, width: 320},
	} {
		t.Run(test.name, func(t *testing.T) {
			viewport, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
				Bounds: &openv1.WorkMapBounds{West: -20, East: 20, South: -10, North: 10},
				Zoom:   test.zoom, WidthPx: test.width, HeightPx: test.height,
			})
			if err != nil {
				t.Fatal(err)
			}
			if viewport.Zoom != 1.5 {
				t.Fatalf("legacy default zoom=%v; want 1.5", viewport.Zoom)
			}
			if test.width == 0 && viewport.WidthPx != 1280 {
				t.Fatalf("default width=%v; want 1280", viewport.WidthPx)
			}
			if test.height == 0 && viewport.HeightPx != 720 {
				t.Fatalf("default height=%v; want 720", viewport.HeightPx)
			}
		})
	}
}

func TestNormalizeWorkMapViewportRejectsNonFiniteZoom(t *testing.T) {
	t.Parallel()
	for _, zoom := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
			Bounds: &openv1.WorkMapBounds{West: -20, East: 20, South: -10, North: 10},
			Zoom:   zoom, WidthPx: 320, HeightPx: 320,
		}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("non-finite zoom %v error=%v; want InvalidArgument", zoom, err)
		}
	}
}

func TestNormalizeWorkMapViewportPreservesSignedMapLibreZoom(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                        string
		zoom                        float64
		width, height               int32
		fullLongitude, fullLatitude bool
	}{
		{name: "minimum zoom", zoom: -2, width: 390, height: 219, fullLongitude: true, fullLatitude: true},
		{name: "negative fractional zoom", zoom: -1.5, width: 390, height: 219, fullLongitude: true, fullLatitude: true},
		{name: "negative zoom longitude wraps only", zoom: -0.5, width: 390, height: 219, fullLongitude: true, fullLatitude: false},
		{name: "negative zoom narrow", zoom: -0.5, width: 320, height: 219, fullLongitude: false, fullLatitude: false},
		{name: "zoom zero", zoom: 0, width: 390, height: 219, fullLongitude: false, fullLatitude: false},
		// A 512px MapLibre world fitted into a 219px-high mobile map is zoom
		// log2(219/512), approximately -1.225. Its 390px width wraps longitude.
		{name: "mobile world fit", zoom: math.Log2(219.0 / 512), width: 390, height: 219, fullLongitude: true, fullLatitude: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			viewport, err := normalizeWorkMapViewport(&openv1.WorkMapViewport{
				Bounds: &openv1.WorkMapBounds{West: -100, East: 100, South: -60, North: 60},
				Zoom:   test.zoom, WidthPx: test.width, HeightPx: test.height,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("requested zoom=%v viewport=%dx%d: normalized zoom=%v fullLongitude=%v fullLatitude=%v", test.zoom, test.width, test.height, viewport.Zoom, viewport.FullLongitude, viewport.FullLatitude)
			if viewport.Zoom != test.zoom || viewport.FullLongitude != test.fullLongitude || viewport.FullLatitude != test.fullLatitude {
				t.Fatalf("zoom=%v FullLongitude=%v FullLatitude=%v; want zoom=%v FullLongitude=%v FullLatitude=%v", viewport.Zoom, viewport.FullLongitude, viewport.FullLatitude, test.zoom, test.fullLongitude, test.fullLatitude)
			}
			if !test.fullLongitude && (viewport.West != -100 || viewport.East != 100) {
				t.Fatalf("narrow viewport lost longitude bounds: %+v", viewport)
			}
		})
	}
}
