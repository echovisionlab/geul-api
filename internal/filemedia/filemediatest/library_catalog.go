package filemediatest

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

type CatalogFileRecord struct {
	SHA256                                             []byte
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
	if q.IncludeFolders {
		for _, folder := range s.Folders {
			if !s.catalogFolderMatches(folder, q) {
				continue
			}
			rows = append(rows, domain.CatalogRow{ItemType: "folder", ID: folder.ID, ParentID: folder.ParentID, Name: folder.Name, MemberID: folder.CreatedByMemberID, CreatedAt: folder.CreatedAt, UpdatedAt: folder.UpdatedAt})
		}
	}
	for _, value := range s.Files {
		if !s.catalogFileMatches(value, q) {
			continue
		}
		rows = append(rows, value.Row)
	}
	slices.SortFunc(rows, func(a, b domain.CatalogRow) int {
		return compareCatalogRows(a, b, q)
	})
	return s.catalogPage(rows, q), nil
}

func (s *LibraryStore) catalogScope(id, parent string) bool {
	if parent == "" {
		return true
	}
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		if id == parent {
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

func (s *LibraryStore) catalogFolderMatches(folder domain.FileFolder, q domain.CatalogQuery) bool {
	if q.Query == "" {
		return catalogParent(folder.ParentID, q.Parent)
	}
	return strings.Contains(strings.ToLower(folder.Name), strings.ToLower(q.Query)) && folder.ID != q.Parent && s.catalogScope(folder.ID, q.Parent)
}

func (s *LibraryStore) catalogFileMatches(value CatalogFileRecord, q domain.CatalogQuery) bool {
	if value.PendingDeletion || value.MeshOutput || !s.catalogFileLocationMatches(value.Row, q) {
		return false
	}
	row := value.Row
	if q.MimePrefix != "" && (row.MimeType == nil || !strings.HasPrefix(strings.ToLower(*row.MimeType), strings.ToLower(q.MimePrefix))) {
		return false
	}
	return q.Uploader == "" || row.MemberID != nil && *row.MemberID == q.Uploader
}

func (s *LibraryStore) catalogFileLocationMatches(row domain.CatalogRow, q domain.CatalogQuery) bool {
	if q.Query == "" {
		return catalogParent(row.ParentID, q.Parent)
	}
	extension := ""
	if row.Extension != nil {
		extension = *row.Extension
	}
	if !strings.Contains(strings.ToLower(row.Name+"."+extension), strings.ToLower(q.Query)) {
		return false
	}
	parent := ""
	if row.ParentID != nil {
		parent = *row.ParentID
	}
	return s.catalogScope(parent, q.Parent)
}

func compareCatalogRows(a, b domain.CatalogRow, q domain.CatalogQuery) int {
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
		c = cmp.Compare(catalogFileSize(a), catalogFileSize(b))
	}
	if q.Descending {
		c = -c
	}
	if c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

func catalogFileSize(row domain.CatalogRow) int64 {
	if row.FileSize == nil {
		return 0
	}
	return *row.FileSize
}

func (s *LibraryStore) catalogPage(rows []domain.CatalogRow, q domain.CatalogQuery) []domain.CatalogRow {
	total := len(rows)
	if q.Offset >= total || q.PageSize <= 0 {
		return nil
	}
	end := min(total, q.Offset+q.PageSize)
	rows = slices.Clone(rows[q.Offset:end])
	for i := range rows {
		rows[i].Total = int64(total)
		rows[i].FolderPath = s.catalogRowPath(rows[i], q.Query)
	}
	return rows
}

func (s *LibraryStore) catalogRowPath(row domain.CatalogRow, query string) []byte {
	if query == "" {
		return []byte("[]")
	}
	folder := row.ID
	if row.ItemType != "folder" {
		folder = ""
		if row.ParentID != nil {
			folder = *row.ParentID
		}
	}
	return s.catalogPath(folder)
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
