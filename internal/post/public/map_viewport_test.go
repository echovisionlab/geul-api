package public

import (
	"math"
	"testing"

	"connectrpc.com/connect"

	"github.com/echovisionlab/geul-api/internal/mapcluster"

	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestNormalizePostMapViewportTreatsWorldWrappingWidthAsFullLongitude(t *testing.T) {
	t.Parallel()

	viewport, err := normalizePostMapViewport(&openv1.PostMapViewport{
		Bounds: &openv1.MapBounds{
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
		t.Fatalf("normalizePostMapViewport returned error: %v", err)
	}

	if !viewport.FullLongitude {
		t.Fatalf("expected world-wrapping viewport to mark FullLongitude")
	}
	if viewport.West != -180 || viewport.East != 180 {
		t.Fatalf("expected full-world longitude bounds, got west=%v east=%v", viewport.West, viewport.East)
	}
}

func TestClusterPostMapPlaceGroupsKeepsSameCoordinateGroupsAsItemsAtHighZoom(t *testing.T) {
	t.Parallel()

	groups := []*postMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-1", PrimaryPostTitle: "Post 1"},
		{PlaceID: "place-2", Name: "B", Address: "B", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-2", PrimaryPostTitle: "Post 2"},
		{PlaceID: "place-3", Name: "C", Address: "C", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-3", PrimaryPostTitle: "Post 3"},
	}

	clusters, items := clusterPostMapPlaceGroups(groups, normalizedPostMapViewport{
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

func TestClusterPostMapPlaceGroupsStillClustersSameCoordinateGroupsAtLowZoom(t *testing.T) {
	t.Parallel()

	groups := []*postMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-1", PrimaryPostTitle: "Post 1"},
		{PlaceID: "place-2", Name: "B", Address: "B", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-2", PrimaryPostTitle: "Post 2"},
		{PlaceID: "place-3", Name: "C", Address: "C", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-3", PrimaryPostTitle: "Post 3"},
	}

	clusters, items := clusterPostMapPlaceGroups(groups, normalizedPostMapViewport{
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

func TestClusterPostMapPlaceGroupsProvideBreakoutZoomForDistinctCoordinates(t *testing.T) {
	t.Parallel()

	groups := []*postMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5665, Lng: 126.9780, PostCount: 1, PrimaryPostID: "post-1", PrimaryPostTitle: "Post 1"},
		{PlaceID: "place-2", Name: "B", Address: "B", Lat: 37.5676, Lng: 126.9792, PostCount: 1, PrimaryPostID: "post-2", PrimaryPostTitle: "Post 2"},
		{PlaceID: "place-3", Name: "C", Address: "C", Lat: 37.5654, Lng: 126.9768, PostCount: 1, PrimaryPostID: "post-3", PrimaryPostTitle: "Post 3"},
	}

	clusters, items := clusterPostMapPlaceGroups(groups, normalizedPostMapViewport{
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

func TestClusterPostMapPlaceGroupsReturnsItemsWhenBelowMinimumClusterPoints(t *testing.T) {
	t.Parallel()

	groups := []*postMapPlaceGroup{
		{PlaceID: "place-1", Name: "A", Address: "A", Lat: 37.5, Lng: 127.0, PostCount: 1, PrimaryPostID: "post-1", PrimaryPostTitle: "Post 1"},
	}

	clusters, items := clusterPostMapPlaceGroups(groups, normalizedPostMapViewport{
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
	if items[0].PlaceId != "place-1" || items[0].PrimaryPostId != "post-1" {
		t.Fatalf("unexpected standalone item: %+v", items[0])
	}
}

func TestNormalizePostMapViewportUsesSharedClusterFallbacks(t *testing.T) {
	t.Parallel()

	viewport, err := normalizePostMapViewport(&openv1.PostMapViewport{
		Bounds: &openv1.MapBounds{
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
		t.Fatalf("normalizePostMapViewport returned error: %v", err)
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

func TestNormalizePostMapViewportNormalizesBoundsAndDefaults(t *testing.T) {
	t.Parallel()

	viewport, err := normalizePostMapViewport(&openv1.PostMapViewport{
		Bounds: &openv1.MapBounds{
			West:  -540,
			South: 90,
			East:  540,
			North: -90,
		},
		Zoom:             0,
		WidthPx:          0,
		HeightPx:         0,
		ClusterRadiusPx:  -1,
		MinClusterPoints: -1,
	})
	if err != nil {
		t.Fatalf("normalizePostMapViewport returned error: %v", err)
	}

	if viewport.South != -85 || viewport.North != 85 {
		t.Fatalf("expected latitude clamp and swap to [-85,85], got south=%v north=%v", viewport.South, viewport.North)
	}
	if viewport.West != -180 || viewport.East != 180 {
		t.Fatalf("expected longitude normalization to [-180,180], got west=%v east=%v", viewport.West, viewport.East)
	}
	if viewport.Zoom != 1.5 {
		t.Fatalf("expected default zoom 1.5, got %v", viewport.Zoom)
	}
	if viewport.WidthPx != 1280 || viewport.HeightPx != 720 {
		t.Fatalf("expected default viewport size 1280x720, got %vx%v", viewport.WidthPx, viewport.HeightPx)
	}
	if viewport.ClusterRadiusPx <= 0 {
		t.Fatalf("expected positive default cluster radius, got %v", viewport.ClusterRadiusPx)
	}
	if viewport.MinClusterPoints != mapcluster.MapClusterDefaultMinPoints {
		t.Fatalf("expected default min cluster points %d, got %d", mapcluster.MapClusterDefaultMinPoints, viewport.MinClusterPoints)
	}
}

func TestNormalizePostMapViewportRejectsMissingBounds(t *testing.T) {
	t.Parallel()

	if _, err := normalizePostMapViewport(nil); err == nil {
		t.Fatal("expected missing viewport to return an error")
	}
	if _, err := normalizePostMapViewport(&openv1.PostMapViewport{}); err == nil {
		t.Fatal("expected missing bounds to return an error")
	}
}

func TestNormalizePostMapViewportUsesMapLibreWorldSize(t *testing.T) {
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
			viewport, err := normalizePostMapViewport(&openv1.PostMapViewport{
				Bounds: &openv1.MapBounds{West: -100, East: 100, South: -60, North: 60},
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

func TestNormalizePostMapViewportPreservesLegacyZoomDefaults(t *testing.T) {
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
			viewport, err := normalizePostMapViewport(&openv1.PostMapViewport{
				Bounds: &openv1.MapBounds{West: -20, East: 20, South: -10, North: 10},
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

func TestNormalizePostMapViewportRejectsNonFiniteZoom(t *testing.T) {
	t.Parallel()
	for _, zoom := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		if _, err := normalizePostMapViewport(&openv1.PostMapViewport{
			Bounds: &openv1.MapBounds{West: -20, East: 20, South: -10, North: 10},
			Zoom:   zoom, WidthPx: 320, HeightPx: 320,
		}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("non-finite zoom %v error=%v; want InvalidArgument", zoom, err)
		}
	}
}

func TestNormalizePostMapViewportPreservesSignedMapLibreZoom(t *testing.T) {
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
			viewport, err := normalizePostMapViewport(&openv1.PostMapViewport{
				Bounds: &openv1.MapBounds{West: -100, East: 100, South: -60, North: 60},
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
