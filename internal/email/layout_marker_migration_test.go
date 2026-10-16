package email

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func layoutMigrationDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	conn, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: conn}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db, mock
}

func expectLayoutRender(mock sqlmock.Sqlmock, source, target, locale string) {
	mock.ExpectQuery(`SELECT source_locale AS locale FROM "email_layout"`).WithArgs("legacy", 1).WillReturnRows(sqlmock.NewRows([]string{"locale"}).AddRow("en"))
	mock.ExpectQuery(`SELECT "html_content" FROM "email_layout_translation"`).WithArgs("legacy", "en", 1).WillReturnRows(sqlmock.NewRows([]string{"html_content"}).AddRow(source))
	if target != "" {
		mock.ExpectQuery(`SELECT locale, html_content FROM "email_layout_translation"`).WithArgs("legacy", locale).WillReturnRows(sqlmock.NewRows([]string{"locale", "html_content"}).AddRow(locale, target))
	}
}

func TestLegacyLayoutBackfillPreservesTranslationsAndRenders(t *testing.T) {
	source := `<html><body><img src="{{logo_email_url}}" alt="Brand"><h1>Welcome</h1>{{content}}<a href="mailto:support@example.test" title="Help">Contact support</a></body></html>`
	target := strings.NewReplacer(`alt="Brand"`, `alt="상표"`, "Welcome", "환영합니다", `title="Help"`, `title="도움말"`, "Contact support", "지원 문의").Replace(source)
	db, mock := layoutMigrationDB(t)
	expectLayoutRender(mock, source, "", "ko")
	_, failedLocale, err := WrapWithLayoutForLocaleStrict(context.Background(), db, "legacy", "ko", "<p>Body</p>", nil)
	require.Equal(t, "en", failedLocale)
	require.ErrorContains(t, err, "element <img> is missing its durable unit marker")
	upgraded, err := BackfillLayoutMarkers("en", map[string]string{"en": source, "ko": target})
	require.NoError(t, err)
	expectLayoutRender(mock, upgraded["en"], upgraded["ko"], "ko")
	rendered, locale, err := WrapWithLayoutForLocaleStrict(context.Background(), db, "legacy", "ko", "<p>Body</p>", map[string]string{"logo_email_url": "https://cdn.example.test/logo.png"})
	require.NoError(t, err)
	require.Equal(t, "ko", locale)
	for _, text := range []string{"상표", "환영합니다", "도움말", "지원 문의", "<p>Body</p>"} {
		require.Contains(t, rendered, text)
	}
	require.NotContains(t, rendered, "geul-unit")
	require.NotContains(t, rendered, "geul-value")
	repeat, err := BackfillLayoutMarkers("en", upgraded)
	require.NoError(t, err)
	require.Equal(t, upgraded, repeat)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLegacyLayoutBackfillRejectsAmbiguousStructure(t *testing.T) {
	for _, target := range []string{`<div>{{content}}<p>Translated</p></div>`, `<div><p>Translated</p>{{content}}<p>Extra</p></div>`, `<div><p title="Different">Translated</p>{{content}}</div>`} {
		_, err := BackfillLayoutMarkers("en", map[string]string{"en": `<div><p>Source</p>{{content}}</div>`, "ko": target})
		require.ErrorContains(t, err, "legacy layout structure differs")
	}
}

func TestDurableUnitsRetainPurePlaceholdersAfterLiteralMigration(t *testing.T) {
	source, err := CanonicalizeLayoutSourceMarkers(`<img alt="Brand"><p>Brand</p>{{content}}`)
	require.NoError(t, err)
	units, err := ExtractLayoutContentUnits(source)
	require.NoError(t, err)
	require.Len(t, units, 2)
	transformed := strings.ReplaceAll(source, "Brand", "{{site.title}}")
	canonical, err := CanonicalizeLayoutSourceMarkers(transformed)
	require.NoError(t, err)
	require.Equal(t, transformed, canonical)
	after, err := ExtractLayoutContentUnits(canonical)
	require.NoError(t, err)
	require.Len(t, after, 2)
	for i, unit := range after {
		require.Equal(t, units[i].Handle, unit.Handle)
		require.Equal(t, "{{site.title}}", unit.SourceValue)
	}
}

func TestLayoutMarkerOwnerRejectsConcurrentSourceSwitch(t *testing.T) {
	db, mock := layoutMigrationDB(t)
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT "source_locale" FROM "email_layout"`)).WithArgs("legacy", 1).WillReturnRows(sqlmock.NewRows([]string{"source_locale"}).AddRow("ko"))
	called := false
	err := MigrateLayoutMarkers(t.Context(), db, "legacy", "en", nil, func() error { called = true; return nil })
	require.ErrorContains(t, err, "source locale changed")
	require.False(t, called)
	require.NoError(t, mock.ExpectationsWereMet())
}
