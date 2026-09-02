package public

import (
	"testing"

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
