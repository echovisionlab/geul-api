package filemedia

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) CreateFileFolder(ctx context.Context, req *connect.Request[managev1.CreateFileFolderRequest]) (*connect.Response[managev1.CreateFileFolderResponse], error) {
	user, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	parentID, err := normalizeFileManagerUUID(req.Msg.GetParentId(), "parent_id", true)
	if err != nil {
		return nil, err
	}
	if err := s.requireFileManagerFolder(ctx, parentID); err != nil {
		return nil, err
	}
	name, err := normalizeFileManagerName(req.Msg.Name, "name")
	if err != nil {
		return nil, err
	}
	creator := user.MemberID.String()
	folder := model.FileFolder{ID: uuid.NewString(), ParentID: parentID, Name: name, CreatedByMemberID: &creator}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if err := tx.Create(&folder).Error; err != nil {
			return err
		}
		return appendFileFolderCreatedAudit(ctx, tx, s.auditWriter, folder.ID)
	}); err != nil {
		return nil, fileManagerMutationError(err, "folder name already exists at this location")
	}
	proto, err := s.fileFolderProto(ctx, folder)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.CreateFileFolderResponse{Folder: proto}), nil
}

func (s *FileService) RenameFileFolder(ctx context.Context, req *connect.Request[managev1.RenameFileFolderRequest]) (*connect.Response[managev1.RenameFileFolderResponse], error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(req.Msg.FolderId, "folder_id", false)
	if err != nil {
		return nil, err
	}
	name, err := normalizeFileManagerName(req.Msg.Name, "name")
	if err != nil {
		return nil, err
	}
	var folder model.FileFolder
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", *folderID).First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("folder", *folderID)
			}
			return err
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if folder.Name == name {
			return nil
		}
		if err := tx.Model(&model.FileFolder{}).Where("id = ?", *folderID).Updates(structured.Fields{"name": name, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
			return fileManagerMutationError(err, "folder name already exists at this location")
		}
		if err := tx.Where("id = ?", *folderID).First(&folder).Error; err != nil {
			return err
		}
		return appendFileFolderRenamedAudit(ctx, tx, s.auditWriter, *folderID)
	}); err != nil {
		return nil, err
	}
	proto, err := s.fileFolderProto(ctx, folder)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.RenameFileFolderResponse{Folder: proto}), nil
}

func (s *FileService) MoveFileFolder(ctx context.Context, req *connect.Request[managev1.MoveFileFolderRequest]) (*connect.Response[managev1.MoveFileFolderResponse], error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(req.Msg.FolderId, "folder_id", false)
	if err != nil {
		return nil, err
	}
	parentID, err := normalizeFileManagerUUID(req.Msg.GetParentId(), "parent_id", true)
	if err != nil {
		return nil, err
	}
	if parentID != nil && *parentID == *folderID {
		return nil, errs.FailedPrecondition("folder cannot be moved into itself")
	}
	var folder model.FileFolder
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockFileFolderHierarchy(tx); err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", *folderID).First(&folder).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("folder", *folderID)
			}
			return err
		}
		if parentID != nil {
			var parent model.FileFolder
			if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ?", *parentID).First(&parent).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return errs.NotFound("folder", *parentID)
				}
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
			`, *parentID, *folderID).Scan(&cycleFound).Error; err != nil {
				return err
			}
			if cycleFound {
				return errs.FailedPrecondition("folder move would create a cycle")
			}
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		previousParentID := ""
		if folder.ParentID != nil {
			previousParentID = *folder.ParentID
		}
		nextParentID := ""
		if parentID != nil {
			nextParentID = *parentID
		}
		if previousParentID == nextParentID {
			return nil
		}
		if err := tx.WithContext(ctx).Model(&model.FileFolder{}).
			Where("id = ?", *folderID).
			Updates(structured.Fields{"parent_id": parentID, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
			return fileManagerMutationError(err, "folder move would create a cycle or duplicate name")
		}
		if err := tx.WithContext(ctx).Where("id = ?", *folderID).First(&folder).Error; err != nil {
			return err
		}
		return appendFileFolderMovedAudit(ctx, tx, s.auditWriter, *folderID, previousParentID, nextParentID)
	}); err != nil {
		return nil, err
	}
	proto, err := s.fileFolderProto(ctx, folder)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.MoveFileFolderResponse{Folder: proto}), nil
}

func fileManagerMutationError(err error, message string) error {
	value := strings.ToLower(err.Error())
	if strings.Contains(value, "duplicate key") || strings.Contains(value, "unique constraint") {
		return errs.AlreadyExistsMsg(message)
	}
	if strings.Contains(value, "cycle") || strings.Contains(value, "cannot be its own parent") {
		return errs.FailedPrecondition(message)
	}
	return errs.Internal(err)
}

func (s *FileService) fileFolderProto(ctx context.Context, folder model.FileFolder) (*managev1.FileFolder, error) {
	result := &managev1.FileFolder{Id: folder.ID, ParentId: folder.ParentID, Name: folder.Name, CreatedAt: timestamppb.New(folder.CreatedAt), UpdatedAt: timestamppb.New(folder.UpdatedAt)}
	if folder.CreatedByMemberID != nil {
		memberSummaries, dependencyErr := requireMemberSummaries(s.memberSummaries)
		if dependencyErr != nil {
			return nil, dependencyErr
		}
		members, err := memberSummaries.Load(ctx, []string{*folder.CreatedByMemberID})
		if err != nil {
			return nil, errs.Internal(err)
		}
		result.CreatedByMember = members[*folder.CreatedByMemberID]
	}
	return result, nil
}
