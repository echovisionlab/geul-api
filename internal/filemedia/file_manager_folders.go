package filemedia

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// CreateFileFolderCommand creates a File library folder under ParentID, or at
// the root when ParentID is empty.
type CreateFileFolderCommand struct {
	ParentID string
	Name     string
}

func (s *Service) CreateFileFolder(ctx context.Context, command CreateFileFolderCommand) (_ *managev1.FileFolder, resultErr error) {
	defer failurecause.AnnotatePrivate(&resultErr, command.Name)

	user, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	parentID, err := normalizeFileManagerUUID(command.ParentID, "parent_id", true)
	if err != nil {
		return nil, err
	}
	if err := s.requireFileManagerFolder(ctx, parentID); err != nil {
		return nil, err
	}
	name, err := normalizeFileManagerName(command.Name, "name")
	if err != nil {
		return nil, err
	}
	creator := user.MemberID.String()
	folder, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[FileFolder, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (FileFolder, error) {
			folder := FileFolder{ID: uuid.NewString(), ParentID: parentID, Name: name, CreatedByMemberID: &creator}
			return store.InsertFolder(ctx, folder)
		},
		Audit: func(folder FileFolder) mutation.Audit { return fileFolderCreatedAudit(folder.ID) },
	})
	if err != nil {
		return nil, fileManagerMutationError(err, "folder name already exists at this location", name)
	}
	return s.fileFolderProto(ctx, folder)
}

// fileFolderChange is a folder after a rename or move, and whether it moved
// from previousParentID or changed its name.
type fileFolderChange struct {
	folder           FileFolder
	changed          bool
	previousParentID string
	nextParentID     string
}

// RenameFileFolderCommand renames a File library folder.
type RenameFileFolderCommand struct {
	FolderID string
	Name     string
}

func (s *Service) RenameFileFolder(ctx context.Context, command RenameFileFolderCommand) (_ *managev1.FileFolder, resultErr error) {
	defer failurecause.AnnotatePrivate(&resultErr, command.Name)

	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(command.FolderID, "folder_id", false)
	if err != nil {
		return nil, err
	}
	name, err := normalizeFileManagerName(command.Name, "name")
	if err != nil {
		return nil, err
	}
	var folder FileFolder
	change, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[fileFolderChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return lockFileFolderForUpdate(ctx, store, *folderID, &folder)
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (fileFolderChange, error) {
			if folder.Name == name {
				return fileFolderChange{folder: folder}, nil
			}
			if err := store.RenameFolder(ctx, *folderID, name); err != nil {
				return fileFolderChange{}, fileManagerMutationError(err, "folder name already exists at this location", name)
			}
			stored, err := store.Folder(ctx, *folderID)
			if err != nil {
				return fileFolderChange{}, err
			}
			folder = stored
			return fileFolderChange{folder: folder, changed: true}, nil
		},
		Audit: func(change fileFolderChange) mutation.Audit {
			if !change.changed {
				return mutation.Unchanged()
			}
			return fileFolderRenamedAudit(change.folder.ID)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.fileFolderProto(ctx, change.folder)
}

// lockFileFolderForUpdate locks a folder row into folder, answering a missing
// folder as not found.
func lockFileFolderForUpdate(ctx context.Context, store Store, folderID string, folder *FileFolder) error {
	value, err := store.LockFolder(ctx, folderID)
	if errors.Is(err, ErrFolderNotFound) {
		return domainerr.ResourceNotFound("folder", folderID)
	}
	if err != nil {
		return err
	}
	*folder = value
	return nil
}

// MoveFileFolderCommand moves a File library folder under ParentID, or to the
// root when ParentID is empty.
type MoveFileFolderCommand struct {
	FolderID string
	ParentID string
}

func (s *Service) MoveFileFolder(ctx context.Context, command MoveFileFolderCommand) (*managev1.FileFolder, error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(command.FolderID, "folder_id", false)
	if err != nil {
		return nil, err
	}
	parentID, err := normalizeFileManagerUUID(command.ParentID, "parent_id", true)
	if err != nil {
		return nil, err
	}
	if parentID != nil && *parentID == *folderID {
		return nil, domainerr.Precondition("folder cannot be moved into itself")
	}
	var folder FileFolder
	change, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[fileFolderChange, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return lockFileFolderMove(ctx, store, *folderID, parentID, &folder)
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (fileFolderChange, error) {
			change := fileFolderChange{folder: folder, previousParentID: pointerValue(folder.ParentID), nextParentID: pointerValue(parentID)}
			if change.previousParentID == change.nextParentID {
				return change, nil
			}
			if err := store.MoveFolder(ctx, *folderID, parentID); err != nil {
				return fileFolderChange{}, fileManagerMutationError(err, "folder move would create a cycle or duplicate name", folder.Name)
			}
			stored, err := store.Folder(ctx, *folderID)
			if err != nil {
				return fileFolderChange{}, err
			}
			change.folder = stored
			change.changed = true
			return change, nil
		},
		Audit: func(change fileFolderChange) mutation.Audit {
			if !change.changed {
				return mutation.Unchanged()
			}
			return fileFolderMovedAudit(change.folder.ID, change.previousParentID, change.nextParentID)
		},
	})
	if err != nil {
		return nil, err
	}
	return s.fileFolderProto(ctx, change.folder)
}

// lockFileFolderMove takes the folder hierarchy lock, then the moved folder
// and its new parent, and rejects a move under the folder's own subtree.
func lockFileFolderMove(ctx context.Context, store Store, folderID string, parentID *string, folder *FileFolder) error {
	if err := store.LockFolderHierarchy(ctx); err != nil {
		return err
	}
	if err := lockFileFolderForUpdate(ctx, store, folderID, folder); err != nil {
		return err
	}
	if parentID == nil {
		return nil
	}
	var parent FileFolder
	if err := lockFileFolderForUpdate(ctx, store, *parentID, &parent); err != nil {
		return err
	}
	cycleFound, err := store.FolderMoveCreatesCycle(ctx, *parentID, folderID)
	if err != nil {
		return err
	}
	if cycleFound {
		return domainerr.Precondition("folder move would create a cycle")
	}
	return nil
}

func fileManagerMutationError(err error, message string, privateName string) error {
	cause := failurecause.WithPrivate(err, privateName)
	if errors.Is(err, ErrFolderNameExists) {
		return domainerr.New(domainerr.AlreadyExists, "", message).WithCause(cause)
	}
	if errors.Is(err, ErrFolderCycle) {
		return domainerr.Precondition(message).WithCause(cause)
	}
	return cause
}

func (s *Service) fileFolderProto(ctx context.Context, folder FileFolder) (*managev1.FileFolder, error) {
	result := &managev1.FileFolder{Id: folder.ID, ParentId: folder.ParentID, Name: folder.Name, CreatedAt: timestamppb.New(folder.CreatedAt), UpdatedAt: timestamppb.New(folder.UpdatedAt)}
	if folder.CreatedByMemberID != nil {
		memberSummaries := s.memberSummaries
		members, err := memberSummaries.Load(ctx, []string{*folder.CreatedByMemberID})
		if err != nil {
			return nil, err
		}
		result.CreatedByMember = memberSummaryProto(members, *folder.CreatedByMemberID)
	}
	return result, nil
}
