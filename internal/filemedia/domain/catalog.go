package domain

import "time"

type CatalogSort int

const (
	CatalogSortName CatalogSort = iota
	CatalogSortCreated
	CatalogSortUpdated
	CatalogSortSize
)

type CatalogQuery struct {
	Parent, Uploader, Query, MimePrefix string
	IncludeFolders                      bool
	SortField                           CatalogSort
	Descending                          bool
	PageSize, Offset                    int
}
type CatalogRow struct {
	ItemType        string
	ID              string
	ParentID        *string
	Name            string
	Extension       *string
	MimeType        *string
	FileSize        *int64
	DurationSeconds *int32
	MemberID        *string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Total           int64
	FolderPath      []byte
}
