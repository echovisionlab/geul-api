package filemedia

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) GetFileDeletionImpact(ctx context.Context, req *connect.Request[managev1.GetFileDeletionImpactRequest]) (*connect.Response[managev1.GetFileDeletionImpactResponse], error) {
	if _, err := requireFileManagerAdmin(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	fileIDs, err := uniqueUUIDs(req.Msg.FileIds, "file_ids")
	if err != nil {
		return nil, err
	}
	if len(fileIDs) == 0 {
		return nil, errs.Required("file_ids")
	}
	if len(fileIDs) > fileManagerMaxMutationSize {
		return nil, errs.InvalidArgument("file_ids", "at most 100 files are allowed")
	}
	if err := s.validateFileManagerFileIDs(ctx, fileIDs); err != nil {
		return nil, err
	}
	rows, err := loadFileUsages(ctx, s.db, fileIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(&managev1.GetFileDeletionImpactResponse{Impacts: impactsForFiles(fileIDs, rows)}), nil
}

func (s *FileService) DeleteFiles(ctx context.Context, req *connect.Request[managev1.DeleteFilesRequest]) (*connect.Response[managev1.DeleteFilesResponse], error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	fileIDs, err := uniqueUUIDs(req.Msg.FileIds, "file_ids")
	if err != nil {
		return nil, err
	}
	if len(fileIDs) == 0 {
		return nil, errs.Required("file_ids")
	}
	if len(fileIDs) > fileManagerMaxMutationSize {
		return nil, errs.InvalidArgument("file_ids", "at most 100 files are allowed")
	}
	accepted := make([]string, 0, len(fileIDs))
	rejected := make([]*managev1.FileDeletionImpact, 0)
	mutations := make([]*FileDeletionMutation, 0, len(fileIDs))
	now := time.Now().UTC()
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.testBeforeFileMutationPrincipal != nil {
			s.testBeforeFileMutationPrincipal(tx, append([]string(nil), fileIDs...))
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if s.testAfterFileMutationPrincipal != nil {
			s.testAfterFileMutationPrincipal(append([]string(nil), fileIDs...))
		}
		files, err := lockFileManagerDeletionRows(ctx, tx, fileIDs)
		if err != nil {
			return err
		}
		activeReferences, err := ActiveFileReferenceIDs(ctx, tx, fileIDs)
		if err != nil {
			return err
		}
		usageRows, err := loadFileUsages(ctx, tx, fileIDs)
		if err != nil {
			return err
		}
		byFile := make(map[string][]fileUsageRow)
		for _, row := range usageRows {
			byFile[row.FileID] = append(byFile[row.FileID], row)
		}
		for _, fileID := range fileIDs {
			if _, referenced := activeReferences[fileID]; referenced {
				rejected = append(rejected, buildFileDeletionImpact(fileID, byFile[fileID]))
				continue
			}
			file := files[fileID]
			if file.DeleteRequestedAt != nil {
				mutations = append(mutations, &FileDeletionMutation{FileID: fileID, AlreadyPending: true})
				accepted = append(accepted, fileID)
				continue
			}
			mutation, markErr := markLockedFileDeletionRequestedWithDB(ctx, tx, file, now)
			if markErr != nil {
				return markErr
			}
			if mutation != nil {
				mutations = append(mutations, mutation)
				accepted = append(accepted, fileID)
				if !mutation.AlreadyPending {
					if err := appendFileDeletedAudit(ctx, tx, s.auditWriter, fileID); err != nil {
						return err
					}
				}
			}
		}
		for _, mutation := range mutations {
			if err := publishFileDeletionMutationWithDB(
				ctx,
				tx,
				fileDeleteAsyncPublisher{s.asyncPublisher},
				mutation,
				now,
			); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.DeleteFilesResponse{AcceptedFileIds: accepted, RejectedFiles: rejected}), nil
}

func lockFileManagerDeletionRows(ctx context.Context, tx *gorm.DB, fileIDs []string) (map[string]model.File, error) {
	var files []model.File
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id", "delete_requested_at").Where("id IN ?", fileIDs).Order("id ASC").Find(&files).Error; err != nil {
		return nil, err
	}
	if len(files) != len(fileIDs) {
		return nil, errs.FailedPrecondition("one or more files no longer exist")
	}
	result := make(map[string]model.File, len(files))
	for _, file := range files {
		result[file.ID] = file
	}
	return result, nil
}

func (s *FileService) DeleteFileFolder(ctx context.Context, req *connect.Request[managev1.DeleteFileFolderRequest]) (*connect.Response[managev1.DeleteFileFolderResponse], error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(req.Msg.FolderId, "folder_id", false)
	if err != nil {
		return nil, err
	}
	var plan fileFolderDeletionPlan
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		prepared, err := prepareFileFolderDeletion(ctx, tx, *folderID, time.Now().UTC())
		if err == nil {
			for _, fileID := range prepared.fileIDs {
				if err := appendFileDeletedAudit(ctx, tx, s.auditWriter, fileID); err != nil {
					return err
				}
			}
			for _, deletedFolderID := range prepared.folderIDs {
				if err := appendFileFolderDeletedAudit(ctx, tx, s.auditWriter, deletedFolderID); err != nil {
					return err
				}
			}
		}
		plan = prepared
		if err != nil {
			return err
		}
		for _, mutation := range plan.mutations {
			if err := publishFileDeletionMutationWithDB(
				ctx,
				tx,
				fileDeleteAsyncPublisher{s.asyncPublisher},
				mutation,
				time.Now().UTC(),
			); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.DeleteFileFolderResponse{AcceptedFileIds: plan.fileIDs}), nil
}

type fileFolderDeletionPlan struct {
	fileIDs   []string
	folderIDs []string
	mutations []*FileDeletionMutation
}

func prepareFileFolderDeletion(
	ctx context.Context,
	tx *gorm.DB,
	folderID string,
	now time.Time,
) (fileFolderDeletionPlan, error) {
	subtreeIDs, err := lockFileFolderSubtree(ctx, tx, folderID)
	if err != nil {
		return fileFolderDeletionPlan{}, err
	}
	fileIDs, err := loadFolderDeletionFileIDs(ctx, tx, subtreeIDs)
	if err != nil {
		return fileFolderDeletionPlan{}, err
	}
	mutations, err := prepareFolderFileDeletionMutations(ctx, tx, fileIDs, now)
	if err != nil {
		return fileFolderDeletionPlan{}, err
	}
	if len(fileIDs) > 0 {
		if err := tx.Table("file").Where("id IN ?", fileIDs).Updates(structured.Fields{
			"folder_id": nil, "updated_at": gorm.Expr("CURRENT_TIMESTAMP"),
		}).Error; err != nil {
			return fileFolderDeletionPlan{}, err
		}
	}
	if err := tx.Delete(&model.FileFolder{}, "id = ?", folderID).Error; err != nil {
		return fileFolderDeletionPlan{}, err
	}
	return fileFolderDeletionPlan{fileIDs: fileIDs, folderIDs: subtreeIDs, mutations: mutations}, nil
}

func lockFileFolderSubtree(ctx context.Context, tx *gorm.DB, folderID string) ([]string, error) {
	if err := lockFileFolderHierarchy(tx); err != nil {
		return nil, err
	}
	var folder model.FileFolder
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", folderID).First(&folder).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFound("folder", folderID)
		}
		return nil, err
	}
	var subtreeIDs []string
	if err := tx.WithContext(ctx).Raw(`WITH RECURSIVE subtree AS (SELECT id FROM file_folder WHERE id = ? UNION ALL SELECT child.id FROM file_folder child JOIN subtree parent ON child.parent_id = parent.id) SELECT id::text FROM subtree ORDER BY id`, folderID).Scan(&subtreeIDs).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Table("file_folder").Clauses(clause.Locking{Strength: "UPDATE"}).Where("id IN ?", subtreeIDs).Order("id").Pluck("id", &subtreeIDs).Error; err != nil {
		return nil, err
	}
	return subtreeIDs, nil
}

func loadFolderDeletionFileIDs(ctx context.Context, tx *gorm.DB, subtreeIDs []string) ([]string, error) {
	var fileIDs []string
	err := tx.WithContext(ctx).Table("file").Where("folder_id IN ? AND delete_requested_at IS NULL", subtreeIDs).Order("id").Pluck("id", &fileIDs).Error
	return fileIDs, err
}

func prepareFolderFileDeletionMutations(
	ctx context.Context,
	tx *gorm.DB,
	fileIDs []string,
	now time.Time,
) ([]*FileDeletionMutation, error) {
	files, err := lockFileManagerDeletionRows(ctx, tx, fileIDs)
	if err != nil {
		return nil, err
	}
	activeReferences, err := ActiveFileReferenceIDs(ctx, tx, fileIDs)
	if err != nil {
		return nil, err
	}
	usageRows, err := loadFileUsages(ctx, tx, fileIDs)
	if err != nil {
		return nil, err
	}
	if len(activeReferences) > 0 || len(usageRows) > 0 {
		return nil, errs.FailedPrecondition("folder contains one or more files that are still in use")
	}
	mutations := make([]*FileDeletionMutation, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		file := files[fileID]
		if file.DeleteRequestedAt != nil {
			mutations = append(mutations, &FileDeletionMutation{FileID: fileID, AlreadyPending: true})
			continue
		}
		mutation, err := markLockedFileDeletionRequestedWithDB(ctx, tx, file, now)
		if err != nil {
			return nil, err
		}
		if mutation != nil {
			mutations = append(mutations, mutation)
		}
	}
	return mutations, nil
}
