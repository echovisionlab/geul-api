package work

import (
	"reflect"
	"testing"

	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/mattn/go-sqlite3"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestMergeWorkClientIntentPositionsLocalAdditions(t *testing.T) {
	for _, test := range []struct {
		name     string
		current  []string
		observed []string
		desired  []string
		want     []string
	}{
		{"insert middle", []string{"a", "b"}, []string{"a", "b"}, []string{"a", "c", "b"}, []string{"a", "c", "b"}},
		{"insert first", []string{"a", "b"}, []string{"a", "b"}, []string{"c", "a", "b"}, []string{"c", "a", "b"}},
		{"multiple insertions", []string{"a", "b"}, []string{"a", "b"}, []string{"c", "a", "d", "e", "b"}, []string{"c", "a", "d", "e", "b"}},
		{"append preserves peer reorder and addition", []string{"b", "peer", "a"}, []string{"a", "b"}, []string{"a", "b", "c"}, []string{"b", "peer", "a", "c"}},
		{"insertion preserves peer reorder and addition", []string{"b", "peer", "a"}, []string{"a", "b"}, []string{"a", "c", "b"}, []string{"c", "b", "peer", "a"}},
		{"multiple insertions preserve their order across reversed anchors", []string{"b", "a"}, []string{"a", "b"}, []string{"c", "a", "d", "b"}, []string{"b", "c", "d", "a"}},
		{"removed anchor appends without resurrection", []string{"a", "peer"}, []string{"a", "b"}, []string{"a", "c", "d", "b"}, []string{"a", "peer", "c", "d"}},
		{"explicit reorder positions new clients and preserves peer addition", []string{"a", "peer", "b"}, []string{"a", "b"}, []string{"b", "c", "a"}, []string{"b", "c", "a", "peer"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := mergeWorkClientIntent(test.current, test.observed, test.desired)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("merged client IDs = %v, want %v", got, test.want)
			}
		})
	}
}

func TestWorkClientSetChangedPersistsInsertedClientOrder(t *testing.T) {
	const workID = "22222222-2222-4222-8222-222222222222"
	const a = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	const b = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	const c = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	for _, test := range []struct {
		name    string
		desired []string
	}{
		{"insert middle", []string{a, c, b}},
		{"insert first", []string{c, a, b}},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = sqlDB.Close() })
			sqlDB.SetMaxOpenConns(1)
			conn, err := sqlDB.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			// The production insert uses PostgreSQL NOW(); expose the same function
			// on the fixture connection so the actual write path runs unchanged.
			err = conn.Raw(func(driverConn any) error {
				return driverConn.(*sqlite3.SQLiteConn).RegisterFunc("NOW", func() string { return "2026-10-05 00:00:00" }, true)
			})
			if closeErr := conn.Close(); err == nil {
				err = closeErr
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{
				`CREATE TABLE client (id TEXT PRIMARY KEY)`,
				`CREATE TABLE work_client (work_id TEXT, client_id TEXT, sort_order INTEGER, created_at DATETIME)`,
			} {
				if err := db.Exec(statement).Error; err != nil {
					t.Fatal(err)
				}
			}
			for _, clientID := range []string{a, b, c} {
				if err := db.Exec(`INSERT INTO client (id) VALUES (?)`, clientID).Error; err != nil {
					t.Fatal(err)
				}
			}
			baseline := []string{a, b}
			for index, clientID := range baseline {
				if err := db.Exec(`INSERT INTO work_client (work_id, client_id, sort_order) VALUES (?, ?, ?)`, workID, clientID, index).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Transaction(func(tx *gorm.DB) error {
				changed, next, err := workClientSetChanged(t.Context(), tx, workID,
					&managev1.WorkClientsUpdate{ClientIds: test.desired}, &managev1.WorkClientsUpdate{ClientIds: baseline})
				if err != nil {
					return err
				}
				if !changed || !reflect.DeepEqual(next, test.desired) {
					t.Fatalf("client update = %v, changed=%v; want %v", next, changed, test.desired)
				}
				return replaceWorkClients(t.Context(), tx, workID, next)
			}); err != nil {
				t.Fatal(err)
			}
			var stored []string
			if err := db.Table("work_client").Where("work_id = ?", workID).Order("sort_order").Pluck("client_id", &stored).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(stored, test.desired) {
				t.Fatalf("stored client order = %v, want %v", stored, test.desired)
			}
		})
	}
}
