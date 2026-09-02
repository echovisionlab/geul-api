// Package mapcluster owns generic viewport clustering for public map projections.
package mapcluster

import (
	"math"
)

const (
	mapClusterDefaultRadiusPx = 56.0
	// MapClusterDefaultMinPoints is the default number of place groups required for a cluster.
	MapClusterDefaultMinPoints = 2
	// MapClusterSameCoordinateBreakoutZoom is the zoom at which coincident groups render separately.
	MapClusterSameCoordinateBreakoutZoom = 9.0
	mapClusterDefaultViewportWidthPx     = 1280.0
	mapClusterMobileWidthPx              = 480.0
	mapClusterTabletWidthPx              = 768.0
)

// MapClusterParameters controls public Map Place clustering.
type MapClusterParameters struct {
	Zoom             float64
	RadiusPx         float64
	MinClusterPoints int
}

// MapClusterComponent summarizes connected Map Place groups.
type MapClusterComponent[T interface{}] struct {
	Groups          []T
	Lat             float64
	Lng             float64
	Count           int32
	West            float64
	South           float64
	East            float64
	North           float64
	MinBreakoutZoom *float64
	SameCoordinates bool
}

// ShouldRenderItems reports whether a component should be projected as individual items.
func (c MapClusterComponent[T]) ShouldRenderItems(parameters MapClusterParameters) bool {
	return len(c.Groups) < parameters.MinClusterPoints ||
		(parameters.Zoom >= MapClusterSameCoordinateBreakoutZoom && c.SameCoordinates)
}

type projectedMapPoint[T interface{}] struct {
	group T
	x     float64
	y     float64
}

type disjointSet struct {
	parent []int
}

func newDisjointSet(size int) *disjointSet {
	parent := make([]int, size)
	for index := range parent {
		parent[index] = index
	}
	return &disjointSet{parent: parent}
}

func (s *disjointSet) find(index int) int {
	if s.parent[index] != index {
		s.parent[index] = s.find(s.parent[index])
	}
	return s.parent[index]
}

func (s *disjointSet) union(left, right int) {
	leftRoot := s.find(left)
	rightRoot := s.find(right)
	if leftRoot != rightRoot {
		s.parent[rightRoot] = leftRoot
	}
}

// ClusterMapPlaceGroups groups nearby public Map Place projections.
func ClusterMapPlaceGroups[T interface{}](
	groups []T,
	parameters MapClusterParameters,
	position func(T) (lat, lng float64),
	count func(T) int32,
) []MapClusterComponent[T] {
	if len(groups) == 0 {
		return nil
	}

	points := projectMapPoints(groups, parameters.Zoom, position)
	sets := connectNearbyMapPoints(points, parameters.RadiusPx)
	components := collectMapComponents(points, sets)

	result := make([]MapClusterComponent[T], 0, len(components))
	for _, componentGroups := range components {
		result = append(result, summarizeMapComponent(componentGroups, parameters, position, count))
	}
	return result
}

func projectMapPoints[T interface{}](
	groups []T,
	zoom float64,
	position func(T) (lat, lng float64),
) []projectedMapPoint[T] {
	points := make([]projectedMapPoint[T], 0, len(groups))
	for _, group := range groups {
		lat, lng := position(group)
		x, y := lngLatToWorldPixel(lng, lat, zoom)
		points = append(points, projectedMapPoint[T]{group: group, x: x, y: y})
	}
	return points
}

func connectNearbyMapPoints[T interface{}](points []projectedMapPoint[T], radiusPx float64) *disjointSet {
	sets := newDisjointSet(len(points))
	cellSize := math.Max(radiusPx, 1)
	grid := make(map[[2]int][]int, len(points))

	for index, point := range points {
		cell := [2]int{
			int(math.Floor(point.x / cellSize)),
			int(math.Floor(point.y / cellSize)),
		}
		for _, neighborIndex := range nearbyMapPointIndexes(grid, cell) {
			neighbor := points[neighborIndex]
			if math.Hypot(point.x-neighbor.x, point.y-neighbor.y) <= radiusPx {
				sets.union(index, neighborIndex)
			}
		}
		grid[cell] = append(grid[cell], index)
	}
	return sets
}

func nearbyMapPointIndexes(grid map[[2]int][]int, cell [2]int) []int {
	var indexes []int
	for deltaX := -1; deltaX <= 1; deltaX++ {
		for deltaY := -1; deltaY <= 1; deltaY++ {
			neighbor := [2]int{cell[0] + deltaX, cell[1] + deltaY}
			indexes = append(indexes, grid[neighbor]...)
		}
	}
	return indexes
}

func collectMapComponents[T interface{}](
	points []projectedMapPoint[T],
	sets *disjointSet,
) map[int][]T {
	components := make(map[int][]T)
	for index, point := range points {
		root := sets.find(index)
		components[root] = append(components[root], point.group)
	}
	return components
}

func summarizeMapComponent[T interface{}](
	groups []T,
	parameters MapClusterParameters,
	position func(T) (lat, lng float64),
	count func(T) int32,
) MapClusterComponent[T] {
	firstLat, firstLng := position(groups[0])
	summary := MapClusterComponent[T]{
		Groups:          groups,
		West:            firstLng,
		South:           firstLat,
		East:            firstLng,
		North:           firstLat,
		SameCoordinates: true,
	}

	for _, group := range groups {
		lat, lng := position(group)
		summary.Lat += lat
		summary.Lng += lng
		summary.Count += count(group)
		summary.West = math.Min(summary.West, lng)
		summary.South = math.Min(summary.South, lat)
		summary.East = math.Max(summary.East, lng)
		summary.North = math.Max(summary.North, lat)
		if math.Abs(lat-firstLat) > 0.0000001 || math.Abs(lng-firstLng) > 0.0000001 {
			summary.SameCoordinates = false
		}
	}

	summary.Lat /= float64(len(groups))
	summary.Lng /= float64(len(groups))
	summary.MinBreakoutZoom = mapComponentBreakoutZoom(summary, parameters)
	return summary
}

func mapComponentBreakoutZoom[T interface{}](
	summary MapClusterComponent[T],
	parameters MapClusterParameters,
) *float64 {
	if summary.SameCoordinates {
		value := MapClusterSameCoordinateBreakoutZoom
		return &value
	}
	return estimateClusterMinBreakoutZoom(
		parameters.Zoom,
		parameters.RadiusPx,
		summary.West,
		summary.South,
		summary.East,
		summary.North,
	)
}

func lngLatToWorldPixel(lng, lat, zoom float64) (float64, float64) {
	lat = Clamp(lat, -85.05112878, 85.05112878)
	scale := 256.0 * math.Pow(2, zoom)
	x := (lng + 180.0) / 360.0 * scale
	sinLat := math.Sin(lat * math.Pi / 180.0)
	y := (0.5 - math.Log((1+sinLat)/(1-sinLat))/(4*math.Pi)) * scale
	return x, y
}

// NormalizeLongitude wraps a longitude to the inclusive -180 to 180 range.
func NormalizeLongitude(lng float64) float64 {
	for lng < -180 {
		lng += 360
	}
	for lng > 180 {
		lng -= 360
	}
	return lng
}

// Clamp constrains value to the supplied range.
func Clamp(value, minValue, maxValue float64) float64 {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func getBaseClusterRadiusPxForZoom(zoom float64) float64 {
	if zoom >= 9 {
		return 14
	}
	if zoom >= 7 {
		return 18
	}
	if zoom >= 5 {
		return 24
	}
	if zoom >= 3.5 {
		return 32
	}
	if zoom >= 2.5 {
		return 40
	}

	return mapClusterDefaultRadiusPx
}

// DefaultMapClusterRadiusPxForZoom returns the responsive default cluster radius.
func DefaultMapClusterRadiusPxForZoom(zoom float64, widthPx float64) float64 {
	baseRadius := getBaseClusterRadiusPxForZoom(zoom)
	if widthPx <= 0 {
		widthPx = mapClusterDefaultViewportWidthPx
	}

	if widthPx <= mapClusterMobileWidthPx {
		return maxFloat(18, roundFloat(baseRadius*0.64))
	}

	if widthPx <= mapClusterTabletWidthPx {
		return maxFloat(20, roundFloat(baseRadius*0.82))
	}

	return baseRadius
}

func roundFloat(value float64) float64 {
	if value < 0 {
		return float64(int(value - 0.5))
	}
	return float64(int(value + 0.5))
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
