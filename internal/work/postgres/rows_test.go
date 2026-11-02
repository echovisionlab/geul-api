package workpostgres

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/work"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

func openSQLite(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func TestArtistSourceTitleSQLReadsTheSourceLocaleTitle(t *testing.T) {
	db := openSQLite(t)
	require.NoError(t, db.Exec(`CREATE TABLE artist (id TEXT PRIMARY KEY, source_locale TEXT NOT NULL)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE artist_translation (entity_id TEXT NOT NULL, locale TEXT NOT NULL, title TEXT)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO artist (id, source_locale) VALUES ('artist-1', 'ko'), ('artist-2', 'en')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO artist_translation (entity_id, locale, title) VALUES
		('artist-1', 'en', 'Echo'), ('artist-1', 'ko', '에코'), ('artist-2', 'ko', '번역만')`).Error)

	var names []string
	require.NoError(t, db.Table("artist").Select(ArtistSourceTitleSQL("artist")+" AS name").Order("artist.id").Pluck("name", &names).Error)
	// The source-locale title; an Artist without one reads as empty, never as a translation.
	require.Equal(t, []string{"에코", ""}, names)
}

func TestListWorksPagesKeepTiesUniqueAndRequestedDirection(t *testing.T) {
	db := openSQLite(t)
	require.NoError(t, db.Exec(`CREATE TABLE work (id TEXT PRIMARY KEY, updated_at DATETIME NOT NULL)`).Error)
	base := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	const a = "11111111-1111-4111-8111-111111111111"
	const b = "22222222-2222-4222-8222-222222222222"
	const c = "33333333-3333-4333-8333-333333333333"
	const old = "44444444-4444-4444-8444-444444444444"
	const newest = "55555555-5555-4555-8555-555555555555"
	for _, row := range []struct {
		id        string
		updatedAt time.Time
	}{
		{c, base}, {a, base}, {b, base}, {old, base.Add(-time.Hour)}, {newest, base.Add(time.Hour)},
	} {
		require.NoError(t, db.Exec(`INSERT INTO work (id, updated_at) VALUES (?, ?)`, row.id, row.updatedAt).Error)
	}
	store := NewStore(db, Ports{})
	for _, test := range []struct {
		name  string
		order commonv1.SortOrder
		want  []string
	}{
		{"ascending", commonv1.SortOrder_SORT_ORDER_ASC, []string{old, a, b, c, newest}},
		{"descending", commonv1.SortOrder_SORT_ORDER_DESC, []string{newest, a, b, c, old}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var ids []string
			for offset := range test.want {
				works, total, err := store.ListWorks(t.Context(), work.AdminListPage{
					Sorts: []*commonv1.SortSpec{{Field: "updated_at", Order: test.order}},
					Limit: 1, Offset: int32(offset),
				})
				require.NoError(t, err)
				require.Equal(t, int64(len(test.want)), total)
				require.Len(t, works, 1)
				ids = append(ids, works[0].ID)
			}
			if !reflect.DeepEqual(ids, test.want) {
				t.Fatalf("one-row pages = %v, want %v", ids, test.want)
			}
		})
	}
}

func TestClientsDistinguishesEmptyResultFromQueryFailure(t *testing.T) {
	db := openSQLite(t)
	for _, statement := range []string{
		`CREATE TABLE client (id TEXT PRIMARY KEY, name TEXT, website TEXT, logo_light_file_id TEXT, logo_dark_file_id TEXT)`,
		`CREATE TABLE work_client (work_id TEXT, client_id TEXT, sort_order INTEGER)`,
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	const workID = "22222222-2222-4222-8222-222222222222"
	const clientID = "11111111-1111-4111-8111-111111111111"
	store := NewStore(db, Ports{})
	clients, err := store.Clients(t.Context(), workID)
	require.NoError(t, err)
	require.Empty(t, clients)
	require.NoError(t, db.Exec(`INSERT INTO client (id, name) VALUES (?, ?)`, clientID, "Existing client").Error)
	require.NoError(t, db.Exec(`INSERT INTO work_client (work_id, client_id, sort_order) VALUES (?, ?, 0)`, workID, clientID).Error)
	clients, err = store.Clients(t.Context(), workID)
	require.NoError(t, err)
	require.Len(t, clients, 1)
	require.Equal(t, clientID, clients[0].ID)
	queryFailure := errors.New("client query unavailable")
	failures := 0
	// Inject failure only into the real client JOIN, after verifying its success.
	require.NoError(t, db.Callback().Row().Before("gorm:row").Register("test:fail_work_client_read", func(query *gorm.DB) {
		if query.Statement.TableExpr != nil && query.Statement.TableExpr.SQL == "work_client wc" {
			failures++
			query.AddError(queryFailure)
		}
	}))
	clients, err = store.Clients(t.Context(), workID)
	require.Equal(t, 1, failures)
	require.Nil(t, clients)
	require.ErrorIs(t, err, queryFailure)
}
