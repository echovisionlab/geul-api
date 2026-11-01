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
type FolderView struct {
	ID, Name             string
	ParentID             *string
	CreatedByMember      *MemberSummary
	CreatedAt, UpdatedAt *time.Time
}

type Store interface {
	MeshStore
	FaviconStore
	IngestMediaStore
	VerifiedIngestStore
	IngestReceiptStore
	UploadPartStore
	UploadCreationStore
	UploadSelectionStore
	UploadSessionStore
	SourcePromotionStore
	FileAlreadyAttached(context.Context, string, string) (bool, error)
	FileUploader(context.Context, string) (*string, error)
	VerifiedFileMetadata(context.Context, string) (File, error)
	ScopedDeliveryStore
	DeliveryStore
	DeletionLifecycleStore
	UsageStore
	DownloadPolicyStore
	LockFileForRename(context.Context, string) (File, error)
	LockFilesForMove(context.Context, []string) ([]File, error)
	RenameFile(context.Context, string, string) error
	MoveFile(context.Context, string, *string) error
	FileCount(context.Context, []string) (int64, error)
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
