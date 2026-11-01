package filemedia

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// GetFileDeletionImpactQuery reads what deleting each File would affect.
type GetFileDeletionImpactQuery struct {
	FileIDs []string
}

func (s *Service) GetFileDeletionImpact(ctx context.Context, query GetFileDeletionImpactQuery) ([]*managev1.FileDeletionImpact, error) {
	if _, err := requireFileManagerAdmin(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	fileIDs, err := validateFileManagerMutationIDs(query.FileIDs)
	if err != nil {
		return nil, err
	}
	if err := s.validateFileManagerFileIDs(ctx, fileIDs); err != nil {
		return nil, err
	}
	rows, err := loadFileUsages(ctx, s.db, fileIDs)
	if err != nil {
		return nil, err
	}
	return impactsForFiles(fileIDs, rows), nil
}

// DeleteFilesCommand schedules the deletion of each unreferenced File.
type DeleteFilesCommand struct {
	FileIDs []string
}

// FileDeletionOutcome lists the Files whose deletion was scheduled and the
// referenced Files it rejected.
type FileDeletionOutcome struct {
	AcceptedFileIDs []string
	RejectedFiles   []*managev1.FileDeletionImpact
}

func (s *Service) DeleteFiles(ctx context.Context, command DeleteFilesCommand) (FileDeletionOutcome, error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return FileDeletionOutcome{}, err
	}
	fileIDs, err := validateFileManagerMutationIDs(command.FileIDs)
	if err != nil {
		return FileDeletionOutcome{}, err
	}
	now := time.Now().UTC()
	deletion, err := mutation.Run(ctx, s.mutations, mutation.Spec[fileManagerDeletion]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (fileManagerDeletion, error) {
			s.afterFileMutationPrincipal(fileIDs)
			return requestFileManagerDeletions(ctx, tx, fileIDs, now)
		},
		Audit: func(deletion fileManagerDeletion) mutation.Audit {
			audits := make([]mutation.Audit, 0, len(deletion.requested))
			for _, fileID := range deletion.requested {
				audits = append(audits, fileDeletedAudit(fileID))
			}
			return mutation.Audits(audits...)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, deletion fileManagerDeletion) error {
			return s.publishFileDeletionMutations(ctx, tx, deletion.mutations, now)
		},
	})
	if err != nil {
		return FileDeletionOutcome{}, err
	}
	return FileDeletionOutcome{AcceptedFileIDs: deletion.accepted, RejectedFiles: deletion.rejected}, nil
}

// fileManagerDeletion is what one File library deletion request scheduled:
// the accepted Files, the referenced Files it rejected, the Files whose
// deletion it newly requested, and the deletion commands to publish.
type fileManagerDeletion struct {
	accepted  []string
	rejected  []*managev1.FileDeletionImpact
	requested []string
	mutations []*FileDeletionMutation
}

func requestFileManagerDeletions(ctx context.Context, tx *gorm.DB, fileIDs []string, now time.Time) (fileManagerDeletion, error) {
	deletion := fileManagerDeletion{
		accepted:  make([]string, 0, len(fileIDs)),
		rejected:  make([]*managev1.FileDeletionImpact, 0),
		mutations: make([]*FileDeletionMutation, 0, len(fileIDs)),
	}
	files, err := lockFileManagerDeletionRows(ctx, tx, fileIDs)
	if err != nil {
		return deletion, err
	}
	activeReferences, err := ActiveFileReferenceIDs(ctx, tx, fileIDs)
	if err != nil {
		return deletion, err
	}
	usageRows, err := loadFileUsages(ctx, tx, fileIDs)
	if err != nil {
		return deletion, err
	}
	byFile := make(map[string][]fileUsageRow)
	for _, row := range usageRows {
		byFile[row.FileID] = append(byFile[row.FileID], row)
	}
	for _, fileID := range fileIDs {
		if _, referenced := activeReferences[fileID]; referenced {
			deletion.rejected = append(deletion.rejected, buildFileDeletionImpact(fileID, byFile[fileID]))
			continue
		}
		file := files[fileID]
		if file.DeleteRequestedAt != nil {
			deletion.mutations = append(deletion.mutations, &FileDeletionMutation{FileID: fileID, AlreadyPending: true})
			deletion.accepted = append(deletion.accepted, fileID)
			continue
		}
		requested, err := markLockedFileDeletionRequestedWithDB(ctx, tx, file, now)
		if err != nil {
			return deletion, err
		}
		if requested == nil {
			continue
		}
		deletion.mutations = append(deletion.mutations, requested)
		deletion.accepted = append(deletion.accepted, fileID)
		if !requested.AlreadyPending {
			deletion.requested = append(deletion.requested, fileID)
		}
	}
	return deletion, nil
}

func (s *Service) publishFileDeletionMutations(ctx context.Context, tx *gorm.DB, mutations []*FileDeletionMutation, now time.Time) error {
	for _, deletion := range mutations {
		if err := publishFileDeletionMutationWithDB(ctx, tx, fileDeleteAsyncPublisher{s.asyncPublisher}, deletion, now); err != nil {
			return err
		}
	}
	return nil
}

// validateFileManagerMutationIDs normalizes the File IDs of one File library
// mutation, at most fileManagerMaxMutationSize of them.
func validateFileManagerMutationIDs(values []string) ([]string, error) {
	fileIDs, err := uniqueUUIDs(values, "file_ids")
	if err != nil {
		return nil, err
	}
	if len(fileIDs) == 0 {
		return nil, domainerr.Required("file_ids")
	}
	if len(fileIDs) > fileManagerMaxMutationSize {
		return nil, domainerr.InvalidField("file_ids", "at most 100 files are allowed")
	}
	return fileIDs, nil
}

func (s *Service) afterFileMutationPrincipal(fileIDs []string) {
	if s.testAfterFileMutationPrincipal != nil {
		s.testAfterFileMutationPrincipal(append([]string(nil), fileIDs...))
	}
}

func lockFileManagerDeletionRows(ctx context.Context, tx *gorm.DB, fileIDs []string) (map[string]model.File, error) {
	var files []model.File
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id", "delete_requested_at").Where("id IN ?", fileIDs).Order("id ASC").Find(&files).Error; err != nil {
		return nil, err
	}
	if len(files) != len(fileIDs) {
		return nil, domainerr.Precondition("one or more files no longer exist")
	}
	result := make(map[string]model.File, len(files))
	for _, file := range files {
		result[file.ID] = file
	}
	return result, nil
}

// DeleteFileFolderCommand deletes a File library folder, its subfolders and
// their unreferenced Files.
type DeleteFileFolderCommand struct {
	FolderID string
}

// DeleteFileFolder returns the IDs of the Files whose deletion it scheduled.
func (s *Service) DeleteFileFolder(ctx context.Context, command DeleteFileFolderCommand) ([]string, error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(command.FolderID, "folder_id", false)
	if err != nil {
		return nil, err
	}
	plan, err := mutation.Run(ctx, s.mutations, mutation.Spec[fileFolderDeletionPlan]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, tx *gorm.DB) (fileFolderDeletionPlan, error) {
			return prepareFileFolderDeletion(ctx, tx, *folderID, time.Now().UTC())
		},
		Audit: func(plan fileFolderDeletionPlan) mutation.Audit {
			audits := make([]mutation.Audit, 0, len(plan.fileIDs)+len(plan.folderIDs))
			for _, fileID := range plan.fileIDs {
				audits = append(audits, fileDeletedAudit(fileID))
			}
			for _, deletedFolderID := range plan.folderIDs {
				audits = append(audits, fileFolderDeletedAudit(deletedFolderID))
			}
			return mutation.Audits(audits...)
		},
		Outbox: func(ctx context.Context, tx *gorm.DB, plan fileFolderDeletionPlan) error {
			return s.publishFileDeletionMutations(ctx, tx, plan.mutations, time.Now().UTC())
		},
	})
	if err != nil {
		return nil, err
	}
	return plan.fileIDs, nil
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
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id = ?", folderID).First(&folder).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, domainerr.ResourceNotFound("folder", folderID)
		}
		return nil, err
	}
	var subtreeIDs []string
	if err := tx.WithContext(ctx).Raw(`WITH RECURSIVE subtree AS (SELECT id FROM file_folder WHERE id = ? UNION ALL SELECT child.id FROM file_folder child JOIN subtree parent ON child.parent_id = parent.id) SELECT id::text FROM subtree ORDER BY id`, folderID).Scan(&subtreeIDs).Error; err != nil {
		return nil, err
	}
	if err := tx.WithContext(ctx).Table("file_folder").Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Where("id IN ?", subtreeIDs).Order("id").Pluck("id", &subtreeIDs).Error; err != nil {
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
		return nil, domainerr.Precondition("folder contains one or more files that are still in use")
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
