package domain

import (
	"context"
	"errors"
	"time"
)

var (
	ErrFolderNotFound   = errors.New("folder not found")
	ErrFolderNameExists = errors.New("folder name already exists")
	ErrFolderCycle      = errors.New("folder hierarchy would contain a cycle")
)

type FileFolder struct {
	ID                   string
	ParentID             *string
	Name                 string
	CreatedByMemberID    *string
	CreatedAt, UpdatedAt time.Time
}
type Store interface {
	Catalog(context.Context, CatalogQuery) ([]CatalogRow, error)
	FileCatalog(context.Context, string) (CatalogRow, error)
	FileCatalogs(context.Context, []string) ([]CatalogRow, error)
	FolderExists(context.Context, string) (bool, error)
	InsertFolder(context.Context, FileFolder) (FileFolder, error)
	Folder(context.Context, string) (FileFolder, error)
	LockFolder(context.Context, string) (FileFolder, error)
	LockFolderHierarchy(context.Context) error
	FolderMoveCreatesCycle(context.Context, string, string) (bool, error)
	RenameFolder(context.Context, string, string) error
	MoveFolder(context.Context, string, *string) error
}
