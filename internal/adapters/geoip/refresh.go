package geoipadapter

import (
	"archive/zip"
	"bufio"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	geoipdomain "github.com/echovisionlab/geul-api/internal/geoip"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

const (
	geoIPDatabaseType           = "GeoLite2-City-CSV"
	geoIPDownloadTimeout        = 5 * time.Minute
	geoIPMaxArchiveBytes int64  = 512 << 20
	geoIPMaxCSVBytes     uint64 = 8 << 30
	geoIPRedirectHost           = "mm-prod-geoip-databases.a2649acb697e2c09b632799562c076f2.r2.cloudflarestorage.com"
	geoIPMaxRedirects           = 3
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// RefreshExecutor owns MaxMind transport, archive validation, and the atomic
// PostgreSQL dataset replacement. Refresh policy remains in the GeoIP domain.
type RefreshExecutor struct {
	db         *gorm.DB
	accountID  string
	licenseKey string
	httpClient httpDoer
}

var _ geoipdomain.RefreshExecutor = (*RefreshExecutor)(nil)

func NewRefreshExecutor(db *gorm.DB, accountID string, licenseKey string) *RefreshExecutor {
	return newRefreshExecutor(db, accountID, licenseKey, newGeoIPHTTPClient())
}

func newRefreshExecutor(db *gorm.DB, accountID string, licenseKey string, client httpDoer) *RefreshExecutor {
	if client == nil {
		client = newGeoIPHTTPClient()
	}
	return &RefreshExecutor{
		db: db, accountID: strings.TrimSpace(accountID), licenseKey: strings.TrimSpace(licenseKey), httpClient: client,
	}
}

func (e *RefreshExecutor) ProviderConfigured() bool {
	return e != nil && e.accountID != "" && e.licenseKey != ""
}

func (e *RefreshExecutor) CurrentImportedAt(ctx context.Context) (*time.Time, error) {
	if e == nil || e.db == nil {
		return nil, fmt.Errorf("GeoIP refresh database is required")
	}
	return currentImportedAt(ctx, e.db)
}

// ImportLatestIfOlderThan downloads outside the transaction, then serializes
// and rechecks the current dataset before replacing it. This tolerates duplicate
// scheduled deliveries without retaining a second import history.
func (e *RefreshExecutor) ImportLatestIfOlderThan(
	ctx context.Context,
	now time.Time,
	cutoff time.Time,
) (bool, error) {
	if e == nil || e.db == nil {
		return false, fmt.Errorf("GeoIP refresh database is required")
	}
	if !e.ProviderConfigured() {
		return false, fmt.Errorf("GeoIP provider is not configured")
	}
	archive, err := downloadGeoIPArchive(ctx, e.httpClient, geoIPDownloadURL(e.accountID, e.licenseKey))
	if err != nil {
		return false, err
	}
	defer func() {
		if err := archive.Close(); err != nil {
			slog.Warn("Failed to remove temporary GeoIP archive", "error", err)
		}
	}()

	zipReader, err := zip.NewReader(archive.file, archive.size)
	if err != nil {
		return false, fmt.Errorf("failed to open zip file: %w", err)
	}

	files, err := findGeoIPArchiveFiles(zipReader.File)
	if err != nil {
		return false, err
	}
	return e.importGeoIPArchiveIfOlderThan(ctx, files, now.UTC(), cutoff.UTC())
}

type geoIPArchiveFiles struct {
	locations *zip.File
	ipv4      *zip.File
	ipv6      *zip.File
}

func findGeoIPArchiveFiles(entries []*zip.File) (geoIPArchiveFiles, error) {
	var files geoIPArchiveFiles
	for _, entry := range entries {
		name := filepath.Base(entry.Name)
		if entry.UncompressedSize64 > geoIPMaxCSVBytes {
			return files, fmt.Errorf("GeoIP archive entry %q exceeds the uncompressed size limit", name)
		}
		switch {
		case strings.HasSuffix(name, "Locations-en.csv"):
			files.locations = entry
		case strings.HasSuffix(name, "Blocks-IPv4.csv"):
			files.ipv4 = entry
		case strings.HasSuffix(name, "Blocks-IPv6.csv"):
			files.ipv6 = entry
		}
	}
	if files.locations == nil || files.ipv4 == nil {
		return files, fmt.Errorf("required CSV files not found in zip")
	}
	return files, nil
}

func (e *RefreshExecutor) importGeoIPArchiveIfOlderThan(
	ctx context.Context,
	files geoIPArchiveFiles,
	now time.Time,
	cutoff time.Time,
) (bool, error) {
	imported := false
	err := e.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A duplicate delivery can finish downloading while another import wins.
		// Serialize the swap, then re-check the authoritative current-dataset row.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext('geoip_dataset_import'))").Error; err != nil {
			return fmt.Errorf("lock GeoIP import: %w", err)
		}
		importedAt, err := currentImportedAt(ctx, tx)
		if err != nil {
			return fmt.Errorf("recheck current GeoIP dataset: %w", err)
		}
		if importedAt != nil && importedAt.After(cutoff) {
			return nil
		}

		if err := createGeoIPStagingTables(tx); err != nil {
			return err
		}
		if err := importGeoIPStagingRows(tx, files); err != nil {
			return err
		}
		locationCount, networkCount, err := countGeoIPStagingRows(tx)
		if err != nil {
			return err
		}
		if err := swapGeoIPStagingTables(tx); err != nil {
			return err
		}
		if err := replaceGeoIPMetadata(tx, now, locationCount, networkCount); err != nil {
			return err
		}
		imported = true
		return nil
	})
	return imported, err
}

func createGeoIPStagingTables(tx *gorm.DB) error {
	if err := tx.Exec(`
			DROP TABLE IF EXISTS geoip_location_new;
			DROP TABLE IF EXISTS geoip_network_new;

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
	`).Error; err != nil {
		return fmt.Errorf("failed to create GeoIP staging tables: %w", err)
	}
	return nil
}

func importGeoIPStagingRows(tx *gorm.DB, files geoIPArchiveFiles) error {
	slog.Info("Importing GeoIP locations")
	if err := importLocationsGorm(tx, files.locations); err != nil {
		return fmt.Errorf("failed to import locations: %w", err)
	}
	slog.Info("Importing GeoIP IPv4 blocks")
	if err := importNetworksGorm(tx, files.ipv4); err != nil {
		return fmt.Errorf("failed to import IPv4 blocks: %w", err)
	}
	if files.ipv6 == nil {
		return nil
	}
	slog.Info("Importing GeoIP IPv6 blocks")
	if err := importNetworksGorm(tx, files.ipv6); err != nil {
		return fmt.Errorf("failed to import IPv6 blocks: %w", err)
	}
	return nil
}

func countGeoIPStagingRows(tx *gorm.DB) (int, int, error) {
	var locationCount, networkCount int
	if err := tx.Raw("SELECT COUNT(*) FROM geoip_location_new").Scan(&locationCount).Error; err != nil {
		return 0, 0, fmt.Errorf("count imported GeoIP locations: %w", err)
	}
	if err := tx.Raw("SELECT COUNT(*) FROM geoip_network_new").Scan(&networkCount).Error; err != nil {
		return 0, 0, fmt.Errorf("count imported GeoIP networks: %w", err)
	}
	if locationCount == 0 || networkCount == 0 {
		return 0, 0, fmt.Errorf("GeoIP import is incomplete: locations=%d networks=%d", locationCount, networkCount)
	}
	return locationCount, networkCount, nil
}

func swapGeoIPStagingTables(tx *gorm.DB) error {
	if err := tx.Exec(`
			DROP TABLE IF EXISTS geoip_location_old;
			DROP TABLE IF EXISTS geoip_network_old;
			ALTER TABLE IF EXISTS geoip_location RENAME TO geoip_location_old;
			ALTER TABLE IF EXISTS geoip_network RENAME TO geoip_network_old;
			ALTER TABLE geoip_location_new RENAME TO geoip_location;
			ALTER TABLE geoip_network_new RENAME TO geoip_network;
			DROP TABLE IF EXISTS geoip_location_old;
			DROP TABLE IF EXISTS geoip_network_old;
			ALTER INDEX IF EXISTS geoip_location_new_pkey RENAME TO geoip_location_pkey;
			ALTER INDEX IF EXISTS geoip_network_new_pkey RENAME TO geoip_network_pkey;
	`).Error; err != nil {
		return fmt.Errorf("failed to swap tables: %w", err)
	}
	if err := tx.Exec(`
			CREATE INDEX IF NOT EXISTS idx_geoip_location_country ON geoip_location (country_iso_code);
			CREATE INDEX IF NOT EXISTS idx_geoip_network_geoname ON geoip_network (geoname_id);
			CREATE INDEX IF NOT EXISTS idx_geoip_network_network ON geoip_network USING GIST (network);
			CREATE INDEX IF NOT EXISTS idx_geoip_network_location ON geoip_network USING GIST (location);
	`).Error; err != nil {
		return fmt.Errorf("failed to create GeoIP indexes: %w", err)
	}
	return nil
}

func replaceGeoIPMetadata(tx *gorm.DB, now time.Time, locationCount, networkCount int) error {
	// geoip_metadata describes the current swapped dataset, not import history.
	// build_epoch remains NULL because the CSV archive does not expose the
	// MaxMind build epoch as an authoritative value.
	if err := tx.Exec("DELETE FROM geoip_metadata WHERE database_type = ?", geoIPDatabaseType).Error; err != nil {
		return fmt.Errorf("replace GeoIP metadata: %w", err)
	}
	if err := tx.Exec(`
			INSERT INTO geoip_metadata (
				database_type, build_epoch, imported_at,
				record_count_location, record_count_network
			) VALUES (?, NULL, ?, ?, ?)
	`, geoIPDatabaseType, now, locationCount, networkCount).Error; err != nil {
		return fmt.Errorf("record current GeoIP dataset: %w", err)
	}
	return nil
}

func newGeoIPHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= geoIPMaxRedirects {
				return fmt.Errorf("GeoIP download exceeded %d redirects", geoIPMaxRedirects)
			}
			if !strings.EqualFold(req.URL.Scheme, "https") ||
				req.URL.User != nil ||
				req.URL.Port() != "" ||
				!strings.EqualFold(req.URL.Hostname(), geoIPRedirectHost) {
				return fmt.Errorf("GeoIP download redirect target is not allowed")
			}
			// The initial permalink currently carries account credentials in its
			// query. Never expose that URL through redirected request headers.
			req.Header.Del("Referer")
			req.Header.Del("Authorization")
			return nil
		},
	}
}

func geoIPDownloadURL(accountID, licenseKey string) string {
	query := url.Values{
		"account_id":  []string{accountID},
		"license_key": []string{licenseKey},
		"suffix":      []string{"zip"},
	}
	return "https://download.maxmind.com/geoip/databases/GeoLite2-City-CSV/download?" + query.Encode()
}

type geoIPArchive struct {
	file *os.File
	path string
	size int64
}

func (a *geoIPArchive) Close() error {
	if a == nil {
		return nil
	}
	var closeErr error
	if a.file != nil {
		closeErr = a.file.Close()
		a.file = nil
	}
	removeErr := os.Remove(a.path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func downloadGeoIPArchive(ctx context.Context, client httpDoer, downloadURL string) (*geoIPArchive, error) {
	downloadCtx, cancel := context.WithTimeout(ctx, geoIPDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create GeoIP database request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		// The request URL contains the MaxMind license key. Do not wrap transport
		// errors because common HTTP clients include the full request URL.
		return nil, fmt.Errorf("download GeoIP database: provider request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download GeoIP database: status %d", resp.StatusCode)
	}
	if resp.ContentLength > geoIPMaxArchiveBytes {
		return nil, fmt.Errorf("GeoIP database archive exceeds the download size limit")
	}
	tmp, err := os.CreateTemp("", "geul-geoip-*.zip")
	if err != nil {
		return nil, fmt.Errorf("create temporary GeoIP archive: %w", err)
	}
	archive := &geoIPArchive{file: tmp, path: tmp.Name()}
	written, err := io.Copy(tmp, io.LimitReader(resp.Body, geoIPMaxArchiveBytes+1))
	if err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("write temporary GeoIP archive: %w", err)
	}
	if written > geoIPMaxArchiveBytes {
		_ = archive.Close()
		return nil, fmt.Errorf("GeoIP database archive exceeds the download size limit")
	}
	archive.size = written
	return archive, nil
}

func currentImportedAt(ctx context.Context, db *gorm.DB) (*time.Time, error) {
	var importedAt sql.NullTime
	if err := db.WithContext(ctx).
		Table("geoip_metadata").
		Select("imported_at").
		Where("database_type = ?", geoIPDatabaseType).
		Order("imported_at DESC, id DESC").
		Limit(1).
		Scan(&importedAt).Error; err != nil {
		return nil, err
	}
	if !importedAt.Valid {
		return nil, nil
	}
	value := importedAt.Time.UTC()
	return &value, nil
}

func importLocationsGorm(tx *gorm.DB, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	reader := csv.NewReader(bufio.NewReader(rc))
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read locations header: %w", err)
	}
	if err := requireGeoIPCSVColumns(header, []string{
		"geoname_id", "locale_code", "continent_code", "continent_name",
		"country_iso_code", "country_name", "subdivision_1_iso_code", "subdivision_1_name",
		"subdivision_2_iso_code", "subdivision_2_name", "city_name", "metro_code",
		"time_zone", "is_in_european_union",
	}); err != nil {
		return fmt.Errorf("invalid locations header: %w", err)
	}
	reader.FieldsPerRecord = len(header)

	row := 1
	for {
		row++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read locations row %d: %w", row, err)
		}

		geonameID, err := requiredPositiveInt(csvField(record, 0))
		if err != nil {
			return fmt.Errorf("parse locations row %d geoname_id: %w", row, err)
		}
		metroCode, err := optionalCSVInt(csvField(record, 11))
		if err != nil {
			return fmt.Errorf("parse locations row %d metro_code: %w", row, err)
		}
		isEU, err := requiredCSVBool(csvField(record, 13))
		if err != nil {
			return fmt.Errorf("parse locations row %d is_in_european_union: %w", row, err)
		}

		err = tx.Exec(`
			INSERT INTO geoip_location_new (
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
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			geonameID,
			nullString(csvField(record, 2)),
			nullString(csvField(record, 3)),
			nullString(csvField(record, 4)),
			nullString(csvField(record, 5)),
			nullString(csvField(record, 6)),
			nullString(csvField(record, 7)),
			nullString(csvField(record, 8)),
			nullString(csvField(record, 9)),
			nullString(csvField(record, 10)),
			metroCode,
			nullString(csvField(record, 12)),
			isEU,
		).Error
		if err != nil {
			return fmt.Errorf("insert locations row %d: %w", row, err)
		}
	}

	return nil
}

func importNetworksGorm(tx *gorm.DB, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	reader := csv.NewReader(bufio.NewReader(rc))
	header, err := reader.Read()
	if err != nil {
		return fmt.Errorf("read networks header: %w", err)
	}
	if err := requireGeoIPCSVColumns(header, []string{
		"network", "geoname_id", "registered_country_geoname_id",
		"represented_country_geoname_id", "is_anonymous_proxy", "is_satellite_provider",
		"postal_code", "latitude", "longitude", "accuracy_radius",
	}); err != nil {
		return fmt.Errorf("invalid networks header: %w", err)
	}
	reader.FieldsPerRecord = len(header)

	row := 1
	for {
		row++
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read networks row %d: %w", row, err)
		}

		network := strings.TrimSpace(csvField(record, 0))
		if network == "" {
			return fmt.Errorf("parse networks row %d: network is required", row)
		}
		geonameID, err := optionalCSVInt(csvField(record, 1))
		if err != nil {
			return fmt.Errorf("parse networks row %d geoname_id: %w", row, err)
		}
		registeredCountryID, err := optionalCSVInt(csvField(record, 2))
		if err != nil {
			return fmt.Errorf("parse networks row %d registered_country_geoname_id: %w", row, err)
		}
		representedCountryID, err := optionalCSVInt(csvField(record, 3))
		if err != nil {
			return fmt.Errorf("parse networks row %d represented_country_geoname_id: %w", row, err)
		}
		isAnonymousProxy, err := requiredCSVBool(csvField(record, 4))
		if err != nil {
			return fmt.Errorf("parse networks row %d is_anonymous_proxy: %w", row, err)
		}
		isSatelliteProvider, err := requiredCSVBool(csvField(record, 5))
		if err != nil {
			return fmt.Errorf("parse networks row %d is_satellite_provider: %w", row, err)
		}
		location, err := optionalLocationWKT(csvField(record, 7), csvField(record, 8))
		if err != nil {
			return fmt.Errorf("parse networks row %d location: %w", row, err)
		}
		accuracyRadius, err := optionalCSVInt(csvField(record, 9))
		if err != nil {
			return fmt.Errorf("parse networks row %d accuracy_radius: %w", row, err)
		}

		err = tx.Exec(`
			INSERT INTO geoip_network_new (
				network,
				geoname_id,
				registered_country_geoname_id,
				represented_country_geoname_id,
				is_anonymous_proxy,
				is_satellite_provider,
				postal_code,
				location,
				accuracy_radius
			)
			VALUES (?::iprange, ?, ?, ?, ?, ?, ?, ST_GeogFromText(?), ?)
		`,
			network,
			geonameID,
			registeredCountryID,
			representedCountryID,
			isAnonymousProxy,
			isSatelliteProvider,
			nullString(csvField(record, 6)),
			location,
			accuracyRadius,
		).Error
		if err != nil {
			return fmt.Errorf("insert networks row %d: %w", row, err)
		}
	}

	return nil
}

func csvField(record []string, idx int) string {
	if idx < 0 || idx >= len(record) {
		return ""
	}
	return record[idx]
}

func nullString(value string) structured.Value {
	if value == "" {
		return nil
	}
	return value
}

func requireGeoIPCSVColumns(actual, expected []string) error {
	if len(actual) < len(expected) {
		return fmt.Errorf("got %d columns, need at least %d", len(actual), len(expected))
	}
	for index, name := range expected {
		if strings.TrimSpace(actual[index]) != name {
			return fmt.Errorf("column %d is %q, want %q", index+1, actual[index], name)
		}
	}
	return nil
}

func requiredPositiveInt(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("value is required")
	}
	i, err := strconv.Atoi(value)
	if err != nil || i <= 0 {
		return 0, fmt.Errorf("value must be a positive integer")
	}
	return i, nil
}

func optionalCSVInt(value string) (structured.Value, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	i, err := strconv.Atoi(value)
	if err != nil {
		return nil, fmt.Errorf("value must be an integer")
	}

	return i, nil
}

func requiredCSVBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	default:
		return false, fmt.Errorf("value must be 0, 1, false, or true")
	}
}

func optionalLocationWKT(latStr, lonStr string) (structured.Value, error) {
	latStr = strings.TrimSpace(latStr)
	lonStr = strings.TrimSpace(lonStr)
	if latStr == "" && lonStr == "" {
		return nil, nil
	}
	if latStr == "" || lonStr == "" {
		return nil, fmt.Errorf("latitude and longitude must both be present")
	}
	lat, latErr := strconv.ParseFloat(latStr, 64)
	lon, lonErr := strconv.ParseFloat(lonStr, 64)
	if latErr != nil || lonErr != nil {
		return nil, fmt.Errorf("latitude and longitude must be numbers")
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, fmt.Errorf("latitude or longitude is out of range")
	}

	return fmt.Sprintf("SRID=4326;POINT(%.8f %.8f)", lon, lat), nil
}
