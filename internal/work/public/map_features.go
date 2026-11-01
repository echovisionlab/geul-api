package public

import (
	"fmt"
	"math"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/mapcluster"
)

// Bounds is a longitude and latitude box.
type Bounds struct {
	West, South, East, North float64
}

// Viewport is the map a client shows: its bounds, zoom and size in pixels,
// and the clustering it asks for. A zero value takes the defaults.
type Viewport struct {
	Bounds           *Bounds
	Zoom             float64
	WidthPx          int32
	HeightPx         int32
	ClusterRadiusPx  int32
	MinClusterPoints int32
}

// MapCluster is a cluster of mapped places.
type MapCluster struct {
	ID              string
	Lat             float64
	Lng             float64
	PlaceCount      int32
	WorkCount       int32
	MinBreakoutZoom *float64
	Bounds          Bounds
}

// MapItem is one mapped place with its primary Work.
type MapItem struct {
	PlaceID          string
	Name             string
	Address          string
	Lat              float64
	Lng              float64
	WorkCount        int32
	PrimaryWorkID    string
	PrimaryWorkSlug  *string
	PrimaryWorkTitle string
}

type normalizedWorkMapViewport struct {
	West             float64
	South            float64
	East             float64
	North            float64
	Zoom             float64
	WidthPx          float64
	HeightPx         float64
	ClusterRadiusPx  float64
	MinClusterPoints int
	FullLongitude    bool
	FullLatitude     bool
}

type workMapPlaceGroup struct {
	PlaceID          string
	Name             string
	Address          string
	Lat              float64
	Lng              float64
	WorkCount        int32
	PrimaryWorkID    string
	PrimaryWorkSlug  *string
	PrimaryWorkTitle string
	Order            int
}

func normalizeWorkMapViewport(viewport *Viewport) (normalizedWorkMapViewport, error) {
	if viewport == nil || viewport.Bounds == nil {
		return normalizedWorkMapViewport{}, domainerr.InvalidField("viewport", "viewport bounds are required")
	}

	south := mapcluster.Clamp(viewport.Bounds.South, -85, 85)
	north := mapcluster.Clamp(viewport.Bounds.North, -85, 85)
	if north < south {
		south, north = north, south
	}

	west := mapcluster.NormalizeLongitude(viewport.Bounds.West)
	east := mapcluster.NormalizeLongitude(viewport.Bounds.East)

	zoom := viewport.Zoom
	if math.IsNaN(zoom) || math.IsInf(zoom, 0) {
		return normalizedWorkMapViewport{}, domainerr.InvalidField("viewport.zoom", "zoom must be finite")
	}
	// Signed MapLibre zooms down to the renderer's minimum are valid with
	// measured dimensions. Preserve legacy defaults for missing-size inputs
	// at zoom zero or below, and for zooms below the supported minimum.
	if zoom < mapcluster.MinViewportZoom || (zoom <= 0 && (viewport.WidthPx <= 0 || viewport.HeightPx <= 0)) {
		zoom = 1.5
	}

	widthPx := float64(viewport.WidthPx)
	if widthPx <= 0 {
		widthPx = 1280
	}

	heightPx := float64(viewport.HeightPx)
	if heightPx <= 0 {
		heightPx = 720
	}

	clusterRadiusPx := float64(viewport.ClusterRadiusPx)
	if clusterRadiusPx <= 0 {
		clusterRadiusPx = mapcluster.DefaultMapClusterRadiusPxForZoom(zoom, widthPx)
	}

	worldScale := mapcluster.WorldTileSize * math.Pow(2, zoom)
	fullLongitude := widthPx >= worldScale-1
	fullLatitude := heightPx >= worldScale-1

	minClusterPoints := int(viewport.MinClusterPoints)
	if minClusterPoints <= 0 {
		minClusterPoints = mapcluster.MapClusterDefaultMinPoints
	}

	if fullLongitude {
		west = -180
		east = 180
	}
	if fullLatitude {
		south = -85
		north = 85
	}

	return normalizedWorkMapViewport{
		West:             west,
		South:            south,
		East:             east,
		North:            north,
		Zoom:             zoom,
		WidthPx:          widthPx,
		HeightPx:         heightPx,
		ClusterRadiusPx:  clusterRadiusPx,
		MinClusterPoints: minClusterPoints,
		FullLongitude:    fullLongitude,
		FullLatitude:     fullLatitude,
	}, nil
}

func buildWorkMapPlaceGroups(
	rows []MapFeatureRow,
) []*workMapPlaceGroup {
	placeByID := make(map[string]*workMapPlaceGroup, len(rows))
	ordered := make([]*workMapPlaceGroup, 0, len(rows))

	for idx, row := range rows {
		group, ok := placeByID[row.PlaceID]
		if !ok {
			group = &workMapPlaceGroup{
				PlaceID:          row.PlaceID,
				Name:             row.PlaceName,
				Address:          row.PlaceAddress,
				Lat:              row.PlaceLat,
				Lng:              row.PlaceLng,
				PrimaryWorkID:    row.WorkID,
				PrimaryWorkSlug:  row.WorkSlug,
				PrimaryWorkTitle: row.WorkTitle,
				Order:            idx,
			}
			placeByID[row.PlaceID] = group
			ordered = append(ordered, group)
		}
		group.WorkCount++
	}

	return ordered
}

func collectWorkMapIDs(rows []MapFeatureRow) []string {
	ids := make([]string, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.WorkID]; ok {
			continue
		}
		seen[row.WorkID] = struct{}{}
		ids = append(ids, row.WorkID)
	}
	return ids
}

func clusterWorkMapPlaceGroups(
	placeGroups []*workMapPlaceGroup,
	viewport normalizedWorkMapViewport,
) ([]MapCluster, []MapItem) {
	if len(placeGroups) == 0 {
		return nil, nil
	}

	parameters := mapcluster.MapClusterParameters{
		Zoom:             viewport.Zoom,
		RadiusPx:         viewport.ClusterRadiusPx,
		MinClusterPoints: viewport.MinClusterPoints,
	}
	components := mapcluster.ClusterMapPlaceGroups(
		placeGroups,
		parameters,
		func(group *workMapPlaceGroup) (float64, float64) { return group.Lat, group.Lng },
		func(group *workMapPlaceGroup) int32 { return group.WorkCount },
	)

	clusters := make([]MapCluster, 0, len(components))
	items := make([]MapItem, 0, len(placeGroups))
	for index, component := range components {
		if component.ShouldRenderItems(parameters) {
			for _, group := range component.Groups {
				items = append(items, workMapItem(group))
			}
			continue
		}
		clusters = append(clusters, MapCluster{
			ID:              fmt.Sprintf("cluster-%d", index+1),
			Lat:             component.Lat,
			Lng:             component.Lng,
			PlaceCount:      int32(len(component.Groups)),
			WorkCount:       component.Count,
			MinBreakoutZoom: component.MinBreakoutZoom,
			Bounds: Bounds{
				West: component.West, South: component.South,
				East: component.East, North: component.North,
			},
		})
	}
	return clusters, items
}

func workMapItem(group *workMapPlaceGroup) MapItem {
	return MapItem{
		PlaceID:          group.PlaceID,
		Name:             group.Name,
		Address:          group.Address,
		Lat:              group.Lat,
		Lng:              group.Lng,
		WorkCount:        group.WorkCount,
		PrimaryWorkID:    group.PrimaryWorkID,
		PrimaryWorkSlug:  group.PrimaryWorkSlug,
		PrimaryWorkTitle: group.PrimaryWorkTitle,
	}
}
