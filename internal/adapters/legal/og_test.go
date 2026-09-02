package legal

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/og"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLegalOgRouteHelpersResolveStableIDs(t *testing.T) {
	require.Equal(t, og.PrivacyRouteEntityID, RouteID("privacy"))
	require.Equal(t, og.TermsRouteEntityID, RouteID("terms"))
}

func TestCurrentRequestsUsesCanonicalRouteAndLocaleTitles(t *testing.T) {
	db := legalOGTestDB(t)
	now := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Exec(`
		INSERT INTO privacy_history (id, version, status, effective_from, title)
		VALUES ('privacy-current', 1, 'PRIVACY_STATUS_ACTIVE', ?, 'Privacy')
	`, now).Error)
	seedLegalOGLocales(t, db, localization.LocaleEnglish, localization.LocaleKorean)
	require.NoError(t, db.Exec(`
		INSERT INTO privacy_translation (entity_id, locale, title)
		VALUES ('privacy-current', 'ko', '개인정보 처리방침')
	`).Error)

	requests, err := CurrentRequests(t.Context(), db, "privacy", stringRef(" background-file "))
	require.NoError(t, err)
	require.Len(t, requests, 2)
	require.Equal(t, RouteID("privacy"), requests[0].EntityID)
	require.Equal(t, "locale", requests[0].Kind)
	require.Equal(t, "en", *requests[0].Locale)
	require.Equal(t, "Privacy", requests[0].Title)
	require.Equal(t, "background-file", *requests[0].FeaturedImageFileID)
	require.Equal(t, "ko", *requests[1].Locale)
	require.Equal(t, "개인정보 처리방침", requests[1].Title)
}

func TestCurrentForRouteFallsBackToEarliestScheduledPolicy(t *testing.T) {
	db := legalOGTestDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO terms_history (id, version, status, effective_from, title)
		VALUES
			('later', 2, 'TERMS_STATUS_SCHEDULED', '2030-02-01T00:00:00Z', 'Later'),
			('first', 1, 'TERMS_STATUS_SCHEDULED', '2030-01-01T00:00:00Z', 'First')
	`).Error)

	current, err := CurrentForRoute(t.Context(), db, "terms")
	require.NoError(t, err)
	require.NotNil(t, current)
	require.Equal(t, "first", current.ID)
	require.Equal(t, "First", current.Title)
}

func TestRouteIDRejectsUnknownLegalKind(t *testing.T) {
	require.Empty(t, RouteID("unknown"))
}

func TestRequestsResolveMissingCurrentPolicyAsNotFound(t *testing.T) {
	_, err := NewRequests().Resolve(
		t.Context(), legalOGTestDB(t), "privacy", "ignored",
		&managev1.OgTargetSelection{Target: &managev1.OgTargetSelection_Primary{
			Primary: &managev1.OgPrimaryTarget{},
		}},
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}

func TestResolvePrimaryUsesEnabledDefaultLocaleAndCanonicalRoute(t *testing.T) {
	db := legalOGTestDB(t)
	now := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, db.Exec(`
		INSERT INTO privacy_history (id, version, status, effective_from, title)
		VALUES ('privacy-current', 1, 'PRIVACY_STATUS_ACTIVE', ?, 'Source policy')
	`, now).Error)
	seedLegalOGLocales(t, db, localization.LocaleKorean)
	require.NoError(t, db.Exec(`
		INSERT INTO privacy_translation (entity_id, locale, title)
		VALUES ('privacy-current', 'ko', '데이터 정책')
	`).Error)

	ignoredVersionID := "ignored-version"
	requests, err := NewRequests().Resolve(t.Context(), db, "privacy", ignoredVersionID, &managev1.OgTargetSelection{
		Target: &managev1.OgTargetSelection_Primary{Primary: &managev1.OgPrimaryTarget{}},
	})
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Equal(t, RouteID("privacy"), requests[0].EntityID)
	require.Equal(t, "ko", *requests[0].Locale)
	require.Equal(t, "데이터 정책", requests[0].Title)
}

func TestTitleUsesExistingTargetValue(t *testing.T) {
	db := legalOGTestDB(t)
	require.NoError(t, db.Exec(`
		INSERT INTO privacy_translation (entity_id, locale, title)
		VALUES ('privacy-current', 'ko', '저장된 번역')
	`).Error)

	title, err := Title(t.Context(), db, "privacy", "privacy-current", "ko", "Privacy")
	require.NoError(t, err)
	require.Equal(t, "저장된 번역", title)
}

func legalOGTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE privacy_history (
			id TEXT, version INTEGER, status TEXT, effective_from DATETIME, title TEXT
		);
		CREATE TABLE terms_history (
			id TEXT, version INTEGER, status TEXT, effective_from DATETIME, title TEXT
		);
		CREATE TABLE privacy_translation (entity_id TEXT, locale TEXT, title TEXT);
		CREATE TABLE terms_translation (entity_id TEXT, locale TEXT, title TEXT);
		CREATE TABLE translation_locale (code TEXT, enabled BOOLEAN, sort_order INTEGER);
		CREATE TABLE translation_settings (id INTEGER PRIMARY KEY, default_locale TEXT);
		CREATE TABLE site_settings (
			id INTEGER PRIMARY KEY, privacy_og_background_file_id TEXT,
			terms_og_background_file_id TEXT
		);
		INSERT INTO translation_settings (id, default_locale) VALUES (1, 'ko');
		INSERT INTO site_settings (id) VALUES (1);
	`).Error)
	return db
}

func seedLegalOGLocales(t *testing.T, db *gorm.DB, enabled ...string) {
	t.Helper()
	enabledSet := make(map[string]struct{}, len(enabled))
	for _, code := range enabled {
		enabledSet[code] = struct{}{}
	}
	for index, code := range localization.CanonicalLocaleCodes() {
		_, isEnabled := enabledSet[code]
		require.NoError(t, db.Exec(
			"INSERT INTO translation_locale (code, enabled, sort_order) VALUES (?, ?, ?)",
			code,
			isEnabled,
			index+1,
		).Error)
	}
}

func stringRef(value string) *string { return &value }
