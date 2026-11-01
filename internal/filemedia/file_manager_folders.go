package filemedia

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/dberrors"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
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
	folder, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.FileFolder]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (model.FileFolder, error) {
			folder := model.FileFolder{ID: uuid.NewString(), ParentID: parentID, Name: name, CreatedByMemberID: &creator}
			return folder, tx.Create(&folder).Error
		},
		Audit: func(folder model.FileFolder) mutation.Audit { return fileFolderCreatedAudit(folder.ID) },
	})
	if err != nil {
		return nil, fileManagerMutationError(err, "folder name already exists at this location", name)
	}
	return s.fileFolderProto(ctx, folder)
}

// fileFolderChange is a folder after a rename or move, and whether it moved
// from previousParentID or changed its name.
type fileFolderChange struct {
	folder           model.FileFolder
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
	var folder model.FileFolder
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[fileFolderChange]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockFileFolderForUpdate(ctx, tx, *folderID, &folder)
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (fileFolderChange, error) {
			if folder.Name == name {
				return fileFolderChange{folder: folder}, nil
			}
			if err := tx.Model(&model.FileFolder{}).Where("id = ?", *folderID).Updates(structured.Fields{"name": name, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
				return fileFolderChange{}, fileManagerMutationError(err, "folder name already exists at this location", name)
			}
			if err := tx.Where("id = ?", *folderID).First(&folder).Error; err != nil {
				return fileFolderChange{}, err
			}
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
func lockFileFolderForUpdate(ctx context.Context, tx *gorm.DB, folderID string, folder *model.FileFolder) error {
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id = ?", folderID).First(folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("folder", folderID)
		}
		return err
	}
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
	var folder model.FileFolder
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[fileFolderChange]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockFileFolderMove(ctx, tx, *folderID, parentID, &folder)
		},
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (fileFolderChange, error) {
			change := fileFolderChange{folder: folder, previousParentID: pointerValue(folder.ParentID), nextParentID: pointerValue(parentID)}
			if change.previousParentID == change.nextParentID {
				return change, nil
			}
			if err := tx.WithContext(ctx).Model(&model.FileFolder{}).
				Where("id = ?", *folderID).
				Updates(structured.Fields{"parent_id": parentID, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
				return fileFolderChange{}, fileManagerMutationError(err, "folder move would create a cycle or duplicate name", folder.Name)
			}
			if err := tx.WithContext(ctx).Where("id = ?", *folderID).First(&change.folder).Error; err != nil {
				return fileFolderChange{}, err
			}
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
func lockFileFolderMove(ctx context.Context, tx *gorm.DB, folderID string, parentID *string, folder *model.FileFolder) error {
	if err := lockFileFolderHierarchy(tx); err != nil {
		return err
	}
	if err := lockFileFolderForUpdate(ctx, tx, folderID, folder); err != nil {
		return err
	}
	if parentID == nil {
		return nil
	}
	var parent model.FileFolder
	if err := lockFileFolderForUpdate(ctx, tx, *parentID, &parent); err != nil {
		return err
	}
	var cycleFound bool
	if err := tx.WithContext(ctx).Raw(`
		WITH RECURSIVE ancestors AS (
			SELECT id, parent_id FROM file_folder WHERE id = ?
			UNION
			SELECT folder.id, folder.parent_id
			FROM file_folder AS folder
			JOIN ancestors ON ancestors.parent_id = folder.id
		)
		SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = ?)
	`, *parentID, folderID).Scan(&cycleFound).Error; err != nil {
		return err
	}
	if cycleFound {
		return domainerr.Precondition("folder move would create a cycle")
	}
	return nil
}

func fileManagerMutationError(err error, message string, privateName string) error {
	cause := failurecause.WithPrivate(err, privateName)
	if dberrors.IsUniqueViolation(err) {
		return domainerr.New(domainerr.AlreadyExists, "", message).WithCause(cause)
	}
	if value := strings.ToLower(err.Error()); strings.Contains(value, "cycle") || strings.Contains(value, "cannot be its own parent") {
		return domainerr.Precondition(message).WithCause(cause)
	}
	return cause
}

func (s *Service) fileFolderProto(ctx context.Context, folder model.FileFolder) (*managev1.FileFolder, error) {
	result := &managev1.FileFolder{Id: folder.ID, ParentId: folder.ParentID, Name: folder.Name, CreatedAt: timestamppb.New(folder.CreatedAt), UpdatedAt: timestamppb.New(folder.UpdatedAt)}
	if folder.CreatedByMemberID != nil {
		memberSummaries := s.memberSummaries
		members, err := memberSummaries.Load(ctx, []string{*folder.CreatedByMemberID})
		if err != nil {
			return nil, err
		}
		result.CreatedByMember = members[*folder.CreatedByMemberID]
	}
	return result, nil
}
