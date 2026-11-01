package translationpostgres

import (
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/translation/application"
)

// entrySelectRegistry projects every entry table the way the Post adapter
// does; the tests reach no other registry method.
type entrySelectRegistry struct{ Registry }

func (entrySelectRegistry) TranslationEntrySelectSQL(string, string) (string, error) {
	return "SELECT locale,title,summary,content_html,content_text,content_json,updated_at,og_asset_id FROM post_translation", nil
}

func newEntryQueryTestStore(t *testing.T) (*store, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return &store{tx: db, ports: Ports{Domains: entrySelectRegistry{}}}, mock
}

func entryQueryTestRows(locales ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"locale", "title", "summary", "content_html", "content_text", "content_json", "updated_at", "og_asset_id"})
	for _, locale := range locales {
		rows.AddRow(locale, nil, nil, nil, nil, nil, time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC), nil)
	}
	return rows
}

func TestEntriesDistinguishRowErrorsFromEmptyResults(t *testing.T) {
	cause := errors.New("translation row stream interrupted")
	for _, test := range []struct {
		name      string
		locales   []string
		errorRow  int
		wantError bool
	}{
		{name: "first row error", locales: []string{"en"}, errorRow: 0, wantError: true},
		{name: "middle row error", locales: []string{"en", "ja"}, errorRow: 1, wantError: true},
		{name: "empty result"},
		{name: "complete result", locales: []string{"en", "ja"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newEntryQueryTestStore(t)
			rows := entryQueryTestRows(test.locales...)
			if test.wantError {
				rows.RowError(test.errorRow, cause)
			}
			mock.ExpectQuery("SELECT locale").WithArgs("post-a").WillReturnRows(rows)
			entries, err := store.Entries(t.Context(), "post", "post-a")
			if test.wantError {
				if !errors.Is(err, cause) || entries != nil {
					t.Fatalf("row error returned entries=%v, error=%v", entries, err)
				}
			} else if err != nil || entries == nil || len(entries) != len(test.locales) {
				t.Fatalf("successful query returned entries=%v, error=%v", entries, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestEntryDistinguishesRowErrorsFromNotFound(t *testing.T) {
	cause := errors.New("translation row stream interrupted")
	for _, test := range []struct {
		name      string
		locales   []string
		rowError  bool
		wantError error
	}{
		{name: "first row error", locales: []string{"en"}, rowError: true, wantError: cause},
		{name: "empty result", wantError: application.ErrEntryNotFound},
		{name: "existing entry", locales: []string{"en"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, mock := newEntryQueryTestStore(t)
			rows := entryQueryTestRows(test.locales...)
			if test.rowError {
				rows.RowError(0, cause)
			}
			mock.ExpectQuery("SELECT locale").WithArgs("post-a", "en").WillReturnRows(rows)
			entry, err := store.Entry(t.Context(), "post", "post-a", "en")
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("query returned entry=%v, error=%v; want %v", entry, err, test.wantError)
				}
			} else if err != nil || entry.Locale != "en" || entry.EntityType != "post" {
				t.Fatalf("existing entry query returned entry=%v, error=%v", entry, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
