package filemediatest

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type CatalogFileRecord struct {
	DeleteRequestedAt                                  *time.Time
	IngestSlotID, IngestAttemptID, ClientMediaBundleID *string
	Row                                                domain.CatalogRow
	PendingDeletion, MeshOutput                        bool
}

func (s *LibraryStore) FileCatalog(_ context.Context, id string) (domain.CatalogRow, error) {
	if s.Fail != nil {
		return domain.CatalogRow{}, s.Fail
	}
	value, ok := s.Files[id]
	if !ok || value.PendingDeletion {
		return domain.CatalogRow{}, nil
	}
	row := value.Row
	row.Total = 1
	row.FolderPath = nil
	return row, nil
}
func (s *LibraryStore) FileCatalogs(ctx context.Context, ids []string) ([]domain.CatalogRow, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []domain.CatalogRow
	for id := range s.Files {
		if slices.Contains(ids, id) {
			row, err := s.FileCatalog(ctx, id)
			if err != nil {
				return nil, err
			}
			if row.ID != "" {
				rows = append(rows, row)
			}
		}
	}
	slices.SortFunc(rows, func(a, b domain.CatalogRow) int { return strings.Compare(a.ID, b.ID) })
	return rows, nil
}
func (s *LibraryStore) Catalog(_ context.Context, q domain.CatalogQuery) ([]domain.CatalogRow, error) {
	if s.Fail != nil {
		return nil, s.Fail
	}
	var rows []domain.CatalogRow
	scope := func(id string) bool {
		if q.Parent == "" {
			return true
		}
		seen := map[string]bool{}
		for id != "" && !seen[id] {
			if id == q.Parent {
				return true
			}
			seen[id] = true
			folder, ok := s.Folders[id]
			if !ok || folder.ParentID == nil {
				break
			}
			id = *folder.ParentID
		}
		return false
	}
	if q.IncludeFolders {
		for _, folder := range s.Folders {
			if q.Query == "" {
				if !catalogParent(folder.ParentID, q.Parent) {
					continue
				}
			} else if !strings.Contains(strings.ToLower(folder.Name), strings.ToLower(q.Query)) || folder.ID == q.Parent || !scope(folder.ID) {
				continue
			}
			rows = append(rows, domain.CatalogRow{ItemType: "folder", ID: folder.ID, ParentID: folder.ParentID, Name: folder.Name, MemberID: folder.CreatedByMemberID, CreatedAt: folder.CreatedAt, UpdatedAt: folder.UpdatedAt})
		}
	}
	for _, value := range s.Files {
		if value.PendingDeletion || value.MeshOutput {
			continue
		}
		row := value.Row
		if q.Query == "" {
			if !catalogParent(row.ParentID, q.Parent) {
				continue
			}
		} else {
			extension := ""
			if row.Extension != nil {
				extension = *row.Extension
			}
			if !strings.Contains(strings.ToLower(row.Name+"."+extension), strings.ToLower(q.Query)) {
				continue
			}
			parent := ""
			if row.ParentID != nil {
				parent = *row.ParentID
			}
			if !scope(parent) {
				continue
			}
		}
		if q.MimePrefix != "" && (row.MimeType == nil || !strings.HasPrefix(strings.ToLower(*row.MimeType), strings.ToLower(q.MimePrefix))) {
			continue
		}
		if q.Uploader != "" && (row.MemberID == nil || *row.MemberID != q.Uploader) {
			continue
		}
		rows = append(rows, row)
	}
	slices.SortFunc(rows, func(a, b domain.CatalogRow) int {
		if a.ItemType != b.ItemType {
			if a.ItemType == "folder" {
				return -1
			}
			return 1
		}
		c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		switch q.SortField {
		case domain.CatalogSortCreated:
			c = a.CreatedAt.Compare(b.CreatedAt)
		case domain.CatalogSortUpdated:
			c = a.UpdatedAt.Compare(b.UpdatedAt)
		case domain.CatalogSortSize:
			var x, y int64
			if a.FileSize != nil {
				x = *a.FileSize
			}
			if b.FileSize != nil {
				y = *b.FileSize
			}
			if x < y {
				c = -1
			} else if x > y {
				c = 1
			} else {
				c = 0
			}
		}
		if q.Descending {
			c = -c
		}
		if c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	total := len(rows)
	if q.Offset >= total || q.PageSize <= 0 {
		return nil, nil
	}
	end := min(total, q.Offset+q.PageSize)
	rows = slices.Clone(rows[q.Offset:end])
	for i := range rows {
		rows[i].Total = int64(total)
		rows[i].FolderPath = []byte("[]")
		if q.Query != "" {
			folder := rows[i].ID
			if rows[i].ItemType != "folder" {
				folder = ""
				if rows[i].ParentID != nil {
					folder = *rows[i].ParentID
				}
			}
			rows[i].FolderPath = s.catalogPath(folder)
		}
	}
	return rows, nil
}
func catalogParent(parent *string, id string) bool {
	return parent == nil && id == "" || parent != nil && *parent == id
}
func (s *LibraryStore) catalogPath(id string) []byte {
	type segment struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	path := []segment{}
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		folder, ok := s.Folders[id]
		if !ok {
			break
		}
		path = append(path, segment{ID: folder.ID, Name: folder.Name})
		if folder.ParentID == nil {
			break
		}
		id = *folder.ParentID
	}
	slices.Reverse(path)
	raw, _ := json.Marshal(path)
	return raw
}
