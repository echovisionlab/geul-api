//go:build integration

package geoipadapter

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	geoipdomain "github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newGeoIPZipFile(t *testing.T, name string, content string) *zip.File {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, zw.Close())

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	require.NoError(t, err)
	require.Len(t, zr.File, 1)
	return zr.File[0]
}

func TestImportGormWritesPublicGeoIPStagingShapes(t *testing.T) {
	db := newAdapterGeoIPIntegrationDB(t)
	createGeoIPNetworkStagingTable(t, db)

	f := newGeoIPZipFile(t, "GeoLite2-City-Blocks-IPv4.csv", `network,geoname_id,registered_country_geoname_id,represented_country_geoname_id,is_anonymous_proxy,is_satellite_provider,postal_code,latitude,longitude,accuracy_radius
8.8.8.0/24,5375480,6252001,,0,0,94035,37.386,-122.084,1000
`)

	require.NoError(t, importNetworksGorm(db, f))

	var row struct {
		GeonameID                    *int    `gorm:"column:geoname_id"`
		RegisteredCountryGeonameID   *int    `gorm:"column:registered_country_geoname_id"`
		RepresentedCountryGeonameID  *int    `gorm:"column:represented_country_geoname_id"`
		IsAnonymousProxy             bool    `gorm:"column:is_anonymous_proxy"`
		IsSatelliteProvider          bool    `gorm:"column:is_satellite_provider"`
		PostalCode                   *string `gorm:"column:postal_code"`
		LocationWKT                  *string `gorm:"column:location_wkt"`
		AccuracyRadius               *int    `gorm:"column:accuracy_radius"`
		ContainsImportedNetworkProbe bool    `gorm:"column:contains_imported_network_probe"`
	}
	require.NoError(t, db.Raw(`
		SELECT
			geoname_id,
			registered_country_geoname_id,
			represented_country_geoname_id,
			is_anonymous_proxy,
			is_satellite_provider,
			postal_code,
			ST_AsText(location::geometry) AS location_wkt,
			accuracy_radius,
			network >>= '8.8.8.8'::ipaddress AS contains_imported_network_probe
		FROM geoip_network_new
	`).Scan(&row).Error)

	require.NotNil(t, row.GeonameID)
	require.Equal(t, 5375480, *row.GeonameID)
	require.NotNil(t, row.RegisteredCountryGeonameID)
	require.Equal(t, 6252001, *row.RegisteredCountryGeonameID)
	require.Nil(t, row.RepresentedCountryGeonameID)
	require.False(t, row.IsAnonymousProxy)
	require.False(t, row.IsSatelliteProvider)
	require.NotNil(t, row.PostalCode)
	require.Equal(t, "94035", *row.PostalCode)
	require.NotNil(t, row.LocationWKT)
	require.Equal(t, "POINT(-122.084 37.386)", *row.LocationWKT)
	require.NotNil(t, row.AccuracyRadius)
	require.Equal(t, 1000, *row.AccuracyRadius)
	require.True(t, row.ContainsImportedNetworkProbe)

	assertImportLocationsGormWritesPublicGeoIPLocationShape(t)
}

func assertImportLocationsGormWritesPublicGeoIPLocationShape(t *testing.T) {
	t.Helper()

	db := newAdapterGeoIPIntegrationDB(t)
	createGeoIPLocationStagingTable(t, db)

	f := newGeoIPZipFile(t, "GeoLite2-City-Locations-en.csv", `geoname_id,locale_code,continent_code,continent_name,country_iso_code,country_name,subdivision_1_iso_code,subdivision_1_name,subdivision_2_iso_code,subdivision_2_name,city_name,metro_code,time_zone,is_in_european_union
5375480,en,NA,North America,US,United States,CA,California,,,Mountain View,807,America/Los_Angeles,0
`)

	require.NoError(t, importLocationsGorm(db, f))

	var rows []struct {
		GeonameID           int     `gorm:"column:geoname_id"`
		ContinentCode       *string `gorm:"column:continent_code"`
		ContinentName       *string `gorm:"column:continent_name"`
		CountryISOCode      *string `gorm:"column:country_iso_code"`
		CountryName         *string `gorm:"column:country_name"`
		Subdivision1ISOCode *string `gorm:"column:subdivision_1_iso_code"`
		Subdivision1Name    *string `gorm:"column:subdivision_1_name"`
		Subdivision2ISOCode *string `gorm:"column:subdivision_2_iso_code"`
		Subdivision2Name    *string `gorm:"column:subdivision_2_name"`
		CityName            *string `gorm:"column:city_name"`
		MetroCode           *int    `gorm:"column:metro_code"`
		TimeZone            *string `gorm:"column:time_zone"`
		IsInEuropeanUnion   bool    `gorm:"column:is_in_european_union"`
	}
	require.NoError(t, db.Raw(`
		SELECT
			geoname_id,
			continent_code,
			continent_name,
			country_iso_code,
			country_name,
			subdivision_1_iso_code,
			subdivision_1_name,
			subdivision_2_iso_code,
			subdivision_2_name,
			city_name,
			metro_code,
			time_zone,
			is_in_european_union
		FROM geoip_location_new
		ORDER BY geoname_id
	`).Scan(&rows).Error)

	require.Len(t, rows, 1)
	require.Equal(t, 5375480, rows[0].GeonameID)
	require.NotNil(t, rows[0].ContinentCode)
	require.Equal(t, "NA", *rows[0].ContinentCode)
	require.NotNil(t, rows[0].ContinentName)
	require.Equal(t, "North America", *rows[0].ContinentName)
	require.NotNil(t, rows[0].CountryISOCode)
	require.Equal(t, "US", *rows[0].CountryISOCode)
	require.NotNil(t, rows[0].CountryName)
	require.Equal(t, "United States", *rows[0].CountryName)
	require.NotNil(t, rows[0].Subdivision1ISOCode)
	require.Equal(t, "CA", *rows[0].Subdivision1ISOCode)
	require.NotNil(t, rows[0].Subdivision1Name)
	require.Equal(t, "California", *rows[0].Subdivision1Name)
	require.Nil(t, rows[0].Subdivision2ISOCode)
	require.Nil(t, rows[0].Subdivision2Name)
	require.NotNil(t, rows[0].CityName)
	require.Equal(t, "Mountain View", *rows[0].CityName)
	require.NotNil(t, rows[0].MetroCode)
	require.Equal(t, 807, *rows[0].MetroCode)
	require.NotNil(t, rows[0].TimeZone)
	require.Equal(t, "America/Los_Angeles", *rows[0].TimeZone)
	require.False(t, rows[0].IsInEuropeanUnion)
}

func TestImportLocationsGormRejectsInvalidRows(t *testing.T) {
	db := newAdapterGeoIPIntegrationDB(t)
	createGeoIPLocationStagingTable(t, db)

	f := newGeoIPZipFile(t, "GeoLite2-City-Locations-en.csv", `geoname_id,locale_code,continent_code,continent_name,country_iso_code,country_name,subdivision_1_iso_code,subdivision_1_name,subdivision_2_iso_code,subdivision_2_name,city_name,metro_code,time_zone,is_in_european_union
0,en,NA,North America,US,United States,,,,,Invalid City,,America/Los_Angeles,0
`)

	err := importLocationsGorm(db, f)
	require.ErrorContains(t, err, "row 2 geoname_id")
}

func TestImportNetworksGormRejectsInvalidRows(t *testing.T) {
	db := newAdapterGeoIPIntegrationDB(t)
	createGeoIPNetworkStagingTable(t, db)

	f := newGeoIPZipFile(t, "GeoLite2-City-Blocks-IPv4.csv", `network,geoname_id,registered_country_geoname_id,represented_country_geoname_id,is_anonymous_proxy,is_satellite_provider,postal_code,latitude,longitude,accuracy_radius
8.8.8.0/24,5375480,6252001,,sometimes,0,94035,37.386,-122.084,1000
`)

	err := importNetworksGorm(db, f)
	require.ErrorContains(t, err, "row 2 is_anonymous_proxy")
}

func TestRefreshExecutorRollsBackPartialImport(t *testing.T) {
	db := newAdapterGeoIPIntegrationDB(t)
	oldImportedAt := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Exec(`DELETE FROM geoip_metadata`).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO geoip_metadata (
			database_type, build_epoch, imported_at,
			record_count_location, record_count_network
		) VALUES (?, NULL, ?, 11, 22)
	`, geoIPDatabaseType, oldImportedAt).Error)

	archiveBody := newGeoIPArchiveBody(t, map[string]string{
		"GeoLite2-City-Locations-en.csv": `geoname_id,locale_code,continent_code,continent_name,country_iso_code,country_name,subdivision_1_iso_code,subdivision_1_name,subdivision_2_iso_code,subdivision_2_name,city_name,metro_code,time_zone,is_in_european_union
5375480,en,NA,North America,US,United States,CA,California,,,Mountain View,807,America/Los_Angeles,0
`,
		"GeoLite2-City-Blocks-IPv4.csv": `network,geoname_id,registered_country_geoname_id,represented_country_geoname_id,is_anonymous_proxy,is_satellite_provider,postal_code,latitude,longitude,accuracy_radius
8.8.8.0/24,5375480,6252001,,0,0,94035,37.386,-122.084,1000
`,
		"GeoLite2-City-Blocks-IPv6.csv": `network,geoname_id,registered_country_geoname_id,represented_country_geoname_id,is_anonymous_proxy,is_satellite_provider,postal_code,latitude,longitude,accuracy_radius
2001:4860::/32,5375480,6252001,,invalid,0,94035,37.386,-122.084,1000
`,
	})

	executor := newRefreshExecutor(
		db,
		"account",
		"license",
		geoIPHTTPDoerFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(archiveBody)),
				Body:          io.NopCloser(bytes.NewReader(archiveBody)),
				Header:        make(http.Header),
			}, nil
		}),
	)
	_, err := geoipdomain.NewRefreshService(executor).Refresh(
		context.Background(),
		oldImportedAt.Add(geoipdomain.RefreshInterval),
	)
	require.ErrorContains(t, err, "failed to import IPv6 blocks")

	var metadata struct {
		ImportedAt          time.Time `gorm:"column:imported_at"`
		RecordCountLocation int       `gorm:"column:record_count_location"`
		RecordCountNetwork  int       `gorm:"column:record_count_network"`
	}
	require.NoError(t, db.Table("geoip_metadata").
		Where("database_type = ?", geoIPDatabaseType).
		Take(&metadata).Error)
	require.WithinDuration(t, oldImportedAt, metadata.ImportedAt, time.Second)
	require.Equal(t, 11, metadata.RecordCountLocation)
	require.Equal(t, 22, metadata.RecordCountNetwork)

	var stagingTables int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('geoip_location_new', 'geoip_network_new')
	`).Scan(&stagingTables).Error)
	require.Zero(t, stagingTables)
}

func newGeoIPArchiveBody(t *testing.T, entries map[string]string) []byte {
	t.Helper()

	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for name, content := range entries {
		entry, err := writer.Create(name)
		require.NoError(t, err)
		_, err = entry.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	return body.Bytes()
}

func createGeoIPNetworkStagingTable(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Intentional low-level DDL fixture for GeoIP import staging-table coverage.
	require.NoError(t, db.Exec(`
		DROP TABLE IF EXISTS geoip_network_new;
		CREATE TABLE geoip_network_new (
			network IPRANGE PRIMARY KEY,
			geoname_id INTEGER,
			registered_country_geoname_id INTEGER,
			represented_country_geoname_id INTEGER,
			is_anonymous_proxy BOOLEAN NOT NULL,
			is_satellite_provider BOOLEAN NOT NULL,
			postal_code VARCHAR(50),
			location GEOGRAPHY(Point, 4326),
			accuracy_radius SMALLINT
		);
	`).Error)
}

func createGeoIPLocationStagingTable(t *testing.T, db *gorm.DB) {
	t.Helper()

	// Intentional low-level DDL fixture for GeoIP import staging-table coverage.
	require.NoError(t, db.Exec(`
		DROP TABLE IF EXISTS geoip_location_new;
		CREATE TABLE geoip_location_new (
			geoname_id INTEGER PRIMARY KEY,
			continent_code CHAR(2),
			continent_name VARCHAR(50),
			country_iso_code CHAR(2),
			country_name VARCHAR(100),
			subdivision_1_iso_code VARCHAR(10),
			subdivision_1_name VARCHAR(100),
			subdivision_2_iso_code VARCHAR(10),
			subdivision_2_name VARCHAR(100),
			city_name VARCHAR(100),
			metro_code SMALLINT,
			time_zone VARCHAR(50),
			is_in_european_union BOOLEAN NOT NULL
		);
	`).Error)
}
