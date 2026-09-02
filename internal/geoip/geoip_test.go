//go:build integration

package geoip_test

import (
	"context"
	"testing"

	"github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestQueryDBUsesIP4RAddressCastAndHandlesMissingCoordinates(t *testing.T) {
	db := newGeoIPIntegrationDB(t)
	seedGeoIPLocation(t, db, 5375480, "US", "United States", "Mountain View", "America/Los_Angeles")
	seedGeoIPNetwork(t, db, "8.8.8.0/24", 5375480, "SRID=4326;POINT(-122.08400000 37.38600000)")
	seedGeoIPNetwork(t, db, "8.8.4.0/24", 5375480, "")

	withCoordinates := geoip.NewLookup(db).LookupIP(context.Background(), "8.8.8.8")
	require.NotNil(t, withCoordinates)
	require.Equal(t, "US", withCoordinates.CountryCode)
	require.Equal(t, "Mountain View", withCoordinates.City)
	require.InDelta(t, 37.386, withCoordinates.Latitude, 0.000001)
	require.InDelta(t, -122.084, withCoordinates.Longitude, 0.000001)

	withoutCoordinates := geoip.NewLookup(db).LookupIP(context.Background(), "8.8.4.4")
	require.NotNil(t, withoutCoordinates)
	require.Equal(t, "US", withoutCoordinates.CountryCode)
	require.Equal(t, "America/Los_Angeles", withoutCoordinates.TimeZone)
	require.Zero(t, withoutCoordinates.Latitude)
	require.Zero(t, withoutCoordinates.Longitude)
}

func newGeoIPIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newGeoIPIntegrationTransaction(t)
	// Intentional low-level table-shape fixture for GeoIP lookup SQL coverage.
	require.NoError(t, db.Exec(`
		DROP TABLE IF EXISTS geoip_network;
		DROP TABLE IF EXISTS geoip_location;
		CREATE TABLE geoip_location (
			geoname_id INTEGER PRIMARY KEY,
			country_iso_code CHAR(2),
			country_name VARCHAR(100),
			city_name VARCHAR(100),
			time_zone VARCHAR(50)
		);
		CREATE TABLE geoip_network (
			network IPRANGE PRIMARY KEY,
			geoname_id INTEGER,
			is_anonymous_proxy BOOLEAN NOT NULL,
			is_satellite_provider BOOLEAN NOT NULL,
			location GEOGRAPHY(Point, 4326)
		);
	`).Error)
	return db
}

func seedGeoIPLocation(t *testing.T, db *gorm.DB, geonameID int, countryCode string, countryName string, cityName string, timeZone string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO geoip_location (geoname_id, country_iso_code, country_name, city_name, time_zone)
		 VALUES (?, ?, ?, ?, ?)`,
		geonameID,
		countryCode,
		countryName,
		cityName,
		timeZone,
	).Error)
}

func seedGeoIPNetwork(t *testing.T, db *gorm.DB, network string, geonameID int, location string) {
	t.Helper()
	if location == "" {
		require.NoError(t, db.Exec(
			`INSERT INTO geoip_network (network, geoname_id, is_anonymous_proxy, is_satellite_provider, location)
			 VALUES (?::iprange, ?, false, false, NULL)`,
			network,
			geonameID,
		).Error)
		return
	}
	require.NoError(t, db.Exec(
		`INSERT INTO geoip_network (network, geoname_id, is_anonymous_proxy, is_satellite_provider, location)
		 VALUES (?::iprange, ?, false, false, ST_GeogFromText(?))`,
		network,
		geonameID,
		location,
	).Error)
}
