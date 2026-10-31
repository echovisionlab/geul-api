package email

import (
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
