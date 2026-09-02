package mapcluster

import "math"

const (
	minClusterBreakoutZoomDelta = 0.35
	clusterBreakoutZoomPadding  = 0.05
)

func estimateClusterMinBreakoutZoom(
	currentZoom float64,
	clusterRadiusPx float64,
	boundsWest float64,
	boundsSouth float64,
	boundsEast float64,
	boundsNorth float64,
) *float64 {
	if clusterRadiusPx <= 0 {
		return nil
	}

	xWest := longitudeToWorldPixel(boundsWest, currentZoom)
	xEast := longitudeToWorldPixel(boundsEast, currentZoom)
	yNorth := latitudeToWorldPixel(boundsNorth, currentZoom)
	ySouth := latitudeToWorldPixel(boundsSouth, currentZoom)

	extentPx := math.Hypot(math.Abs(xEast-xWest), math.Abs(ySouth-yNorth))
	if extentPx <= 0.000001 {
		return nil
	}

	breakoutZoom := currentZoom + math.Log2(clusterRadiusPx/extentPx)
	if math.IsNaN(breakoutZoom) || math.IsInf(breakoutZoom, 0) {
		return nil
	}

	minimumBreakoutZoom := currentZoom + minClusterBreakoutZoomDelta
	if breakoutZoom < minimumBreakoutZoom {
		breakoutZoom = minimumBreakoutZoom
	}

	breakoutZoom += clusterBreakoutZoomPadding
	return &breakoutZoom
}

func longitudeToWorldPixel(lng float64, zoom float64) float64 {
	return (NormalizeLongitude(lng) + 180.0) / 360.0 * (256.0 * math.Pow(2, zoom))
}

func latitudeToWorldPixel(lat float64, zoom float64) float64 {
	safeLat := Clamp(lat, -85.05112878, 85.05112878)
	sinLat := math.Sin(safeLat * math.Pi / 180.0)
	return (0.5 - math.Log((1+sinLat)/(1-sinLat))/(4*math.Pi)) * (256.0 * math.Pow(2, zoom))
}
