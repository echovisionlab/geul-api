package filemediatest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type CatalogHarness struct {
	mutationtest.Harness[domain.Store]
	SeedFile func(*testing.T, domain.CatalogRow, bool, bool)
}

func RunCatalogContract(t *testing.T, open func(*testing.T) CatalogHarness) {
	t.Helper()
	cases := map[string]func(*testing.T, CatalogHarness){
		"directory scope and folder priority": func(t *testing.T, h CatalogHarness) {
			parent := insertFolder(t, h.Harness, nil, "z-folder")
			insertFolder(t, h.Harness, &parent.ID, "child")
			root, child := CatalogFile("a-root", nil), CatalogFile("a-child", &parent.ID)
			h.SeedFile(t, root, false, false)
			h.SeedFile(t, child, false, false)
			rows := readCatalog(t, h, domain.CatalogQuery{IncludeFolders: true, PageSize: 50})
			require.Equal(t, []string{parent.ID, root.ID}, catalogIDs(rows))
			require.Equal(t, int64(2), rows[0].Total)
			rows = readCatalog(t, h, domain.CatalogQuery{Parent: parent.ID, IncludeFolders: true, PageSize: 50})
			require.Len(t, rows, 2)
			require.Equal(t, "folder", rows[0].ItemType)
			require.Equal(t, child.ID, rows[1].ID)
			for _, row := range rows {
				require.JSONEq(t, "[]", string(row.FolderPath))
			}
		},
		"pending and generated output filters differ from direct lookup": func(t *testing.T, h CatalogHarness) {
			ready, pending, output := CatalogFile("ready", nil), CatalogFile("pending", nil), CatalogFile("generated", nil)
			h.SeedFile(t, ready, false, false)
			h.SeedFile(t, pending, true, false)
			h.SeedFile(t, output, false, true)
			rows := readCatalog(t, h, domain.CatalogQuery{PageSize: 50})
			require.Equal(t, []string{ready.ID}, catalogIDs(rows))
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				row, err := s.FileCatalog(ctx, output.ID)
				require.NoError(t, err)
				require.Equal(t, output.ID, row.ID)
				row, err = s.FileCatalog(ctx, pending.ID)
				require.NoError(t, err)
				require.Empty(t, row.ID)
				return nil
			}))
		},
		"file projections and ordered ID lookup": func(t *testing.T, h CatalogHarness) {
			folder := insertFolder(t, h.Harness, nil, "folder")
			a, b := CatalogFile("first", &folder.ID), CatalogFile("second", nil)
			a.ID = "11111111-1111-4111-8111-111111111111"
			b.ID = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
			h.SeedFile(t, a, false, false)
			h.SeedFile(t, b, false, false)
			require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
				got, err := s.FileCatalog(ctx, a.ID)
				require.NoError(t, err)
				require.Equal(t, a.ID, got.ID)
				require.Equal(t, a.Name, got.Name)
				require.Equal(t, a.ParentID, got.ParentID)
				require.Equal(t, a.Extension, got.Extension)
				require.Equal(t, a.MimeType, got.MimeType)
				require.Equal(t, a.FileSize, got.FileSize)
				require.Equal(t, a.DurationSeconds, got.DurationSeconds)
				require.True(t, a.CreatedAt.Equal(got.CreatedAt))
				require.True(t, a.UpdatedAt.Equal(got.UpdatedAt))
				require.Equal(t, int64(1), got.Total)
				require.Nil(t, got.FolderPath)
				rows, err := s.FileCatalogs(ctx, []string{b.ID, a.ID, b.ID, uuid.NewString()})
				require.NoError(t, err)
				require.Equal(t, []string{a.ID, b.ID}, catalogIDs(rows))
				missing, err := s.FileCatalog(ctx, uuid.NewString())
				require.NoError(t, err)
				require.Empty(t, missing.ID)
				return nil
			}))
		},
		"pagination keeps total and stable ties": func(t *testing.T, h CatalogHarness) {
			for _, id := range []string{"eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "11111111-1111-4111-8111-111111111111"} {
				row := CatalogFile("same", nil)
				row.ID = id
				h.SeedFile(t, row, false, false)
			}
			q := domain.CatalogQuery{PageSize: 1}
			first := readCatalog(t, h, q)
			require.Equal(t, "11111111-1111-4111-8111-111111111111", first[0].ID)
			require.Equal(t, int64(2), first[0].Total)
			q.Offset = 1
			second := readCatalog(t, h, q)
			require.Equal(t, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", second[0].ID)
			require.Equal(t, int64(2), second[0].Total)
			q.Offset = 2
			require.Empty(t, readCatalog(t, h, q))
		},
		"all sort fields preserve ascending ID ties": func(t *testing.T, h CatalogHarness) {
			at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			ids := []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"}
			for i, name := range []string{"alpha", "Bravo", "charlie", "ALPHA"} {
				row := CatalogFile(name, nil)
				row.ID = ids[i]
				created := []int{1, 2, 3, 1}[i]
				updated := []int{4, 3, 2, 4}[i]
				size := []int64{3, 1, 2, 3}[i]
				row.CreatedAt = at.Add(time.Duration(created) * time.Hour)
				row.UpdatedAt = at.Add(time.Duration(updated) * time.Hour)
				row.FileSize = &size
				h.SeedFile(t, row, false, false)
			}
			for _, tc := range []struct {
				field domain.CatalogSort
				desc  bool
				order []int
			}{{domain.CatalogSortName, false, []int{0, 3, 1, 2}}, {domain.CatalogSortName, true, []int{2, 1, 0, 3}}, {domain.CatalogSortCreated, false, []int{0, 3, 1, 2}}, {domain.CatalogSortCreated, true, []int{2, 1, 0, 3}}, {domain.CatalogSortUpdated, false, []int{2, 1, 0, 3}}, {domain.CatalogSortUpdated, true, []int{0, 3, 1, 2}}, {domain.CatalogSortSize, false, []int{1, 2, 0, 3}}, {domain.CatalogSortSize, true, []int{0, 3, 2, 1}}} {
				rows := readCatalog(t, h, domain.CatalogQuery{PageSize: 50, SortField: tc.field, Descending: tc.desc})
				want := []string{}
				for _, i := range tc.order {
					want = append(want, ids[i])
				}
				require.Equal(t, want, catalogIDs(rows))
			}
		},
		"search scopes descendants and projects full folder path": func(t *testing.T, h CatalogHarness) {
			root := insertFolder(t, h.Harness, nil, "needle-root")
			child := insertFolder(t, h.Harness, &root.ID, "needle-child")
			other := insertFolder(t, h.Harness, nil, "other")
			a, b, c := CatalogFile("needle-root-file", &root.ID), CatalogFile("needle-child-file", &child.ID), CatalogFile("needle-outside", &other.ID)
			for _, row := range []domain.CatalogRow{a, b, c} {
				h.SeedFile(t, row, false, false)
			}
			rows := readCatalog(t, h, domain.CatalogQuery{Parent: root.ID, Query: "needle", IncludeFolders: true, PageSize: 50})
			require.ElementsMatch(t, []string{child.ID, a.ID, b.ID}, catalogIDs(rows))
			for _, row := range rows {
				require.Equal(t, int64(3), row.Total)
				var path []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				require.NoError(t, json.Unmarshal(row.FolderPath, &path))
				require.Equal(t, root.ID, path[0].ID)
				if row.ID == a.ID {
					require.Len(t, path, 1)
				} else {
					require.Len(t, path, 2)
					require.Equal(t, child.ID, path[1].ID)
					require.Equal(t, "needle-child", path[1].Name)
				}
			}
		},
		"search includes extension and MIME filters": func(t *testing.T, h CatalogHarness) {
			row := CatalogFile("source", nil)
			h.SeedFile(t, row, false, false)
			rows := readCatalog(t, h, domain.CatalogQuery{Query: ".WEBP", MimePrefix: "IMAGE/", PageSize: 50})
			require.Equal(t, []string{row.ID}, catalogIDs(rows))
			require.Empty(t, readCatalog(t, h, domain.CatalogQuery{MimePrefix: "audio/", PageSize: 50}))
			require.Empty(t, readCatalog(t, h, domain.CatalogQuery{Uploader: uuid.NewString(), PageSize: 50}))
		},
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) { t.Parallel(); run(t, open(t)) })
	}
}
func CatalogFile(name string, parent *string) domain.CatalogRow {
	ext, mime, size, duration := "webp", "image/webp", int64(1024), int32(4)
	at := time.Now().UTC().Truncate(time.Microsecond)
	return domain.CatalogRow{ItemType: "file", ID: uuid.NewString(), ParentID: parent, Name: name, Extension: &ext, MimeType: &mime, FileSize: &size, DurationSeconds: &duration, CreatedAt: at, UpdatedAt: at}
}
func readCatalog(t *testing.T, h CatalogHarness, q domain.CatalogQuery) []domain.CatalogRow {
	t.Helper()
	var rows []domain.CatalogRow
	require.NoError(t, mutationtest.Read(t, h.Harness, func(ctx context.Context, s domain.Store) error {
		var err error
		rows, err = s.Catalog(ctx, q)
		return err
	}))
	return rows
}
func catalogIDs(rows []domain.CatalogRow) []string {
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}
