package filemedia

import (
	"context"
	"errors"
	"slices"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// RenameFileCommand renames a File without changing its extension.
type RenameFileCommand struct {
	FileID   string
	FileName string
}

func (s *Service) RenameFile(ctx context.Context, command RenameFileCommand) (_ *managev1.FileManagerFile, resultErr error) {
	defer failurecause.AnnotatePrivate(&resultErr, command.FileName)

	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	fileID, err := normalizeFileManagerUUID(command.FileID, "file_id", false)
	if err != nil {
		return nil, err
	}
	name, err := normalizeFileManagerName(command.FileName, "file_name")
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock:   s.fileMutationPrincipalHook([]string{*fileID}),
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			s.afterFileMutationPrincipal([]string{*fileID})
			return renameLockedFile(tx, *fileID, name)
		},
		Audit: func(renamed bool) mutation.Audit {
			if !renamed {
				return mutation.Unchanged()
			}
			return fileRenamedAudit(*fileID)
		},
	}); err != nil {
		return nil, err
	}
	response, err := s.GetFile(ctx, GetFileQuery{FileID: *fileID})
	if err != nil {
		return nil, err
	}
	return response.File, nil
}

// renameLockedFile locks the File row and renames it, reporting whether the
// name changed.
func renameLockedFile(tx *gorm.DB, fileID, name string) (bool, error) {
	var file model.File
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "file_name", "extension", "delete_requested_at").Where("id = ?", fileID).First(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, errs.NotFound("file", fileID)
		}
		return false, err
	}
	if file.DeleteRequestedAt != nil {
		return false, errs.FailedPrecondition("file is pending deletion")
	}
	if strings.EqualFold(mediaauth.FilenameExtension(name), file.Extension) {
		return false, errs.InvalidArgument("file_name", "must not include the file extension")
	}
	if file.FileName == name {
		return false, nil
	}
	if err := tx.Table("file").Where("id = ?", fileID).Updates(structured.Fields{"file_name": name, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
		return false, err
	}
	return true, nil
}

// MoveFilesCommand moves Files into FolderID, or to the root when FolderID is
// empty.
type MoveFilesCommand struct {
	FileIDs  []string
	FolderID string
}

func (s *Service) MoveFiles(ctx context.Context, command MoveFilesCommand) ([]*managev1.FileManagerFile, error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	fileIDs, err := validateFileManagerMutationIDs(command.FileIDs)
	if err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(command.FolderID, "folder_id", true)
	if err != nil {
		return nil, err
	}
	if err := s.requireFileManagerFolder(ctx, folderID); err != nil {
		return nil, err
	}
	nextParentID := pointerValue(folderID)
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[[]fileMove]{
		Lock:   s.fileMutationPrincipalHook(fileIDs),
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(_ context.Context, tx *gorm.DB) ([]fileMove, error) {
			s.afterFileMutationPrincipal(fileIDs)
			return moveLockedFiles(tx, fileIDs, folderID)
		},
		Audit: func(moves []fileMove) mutation.Audit {
			audits := make([]mutation.Audit, 0, len(moves))
			for _, move := range moves {
				audits = append(audits, fileMovedAudit(move.fileID, move.previousParentID, nextParentID))
			}
			return mutation.Audits(audits...)
		},
	}); err != nil {
		return nil, err
	}
	return s.loadFileManagerFilesByIDs(ctx, fileIDs)
}

// fileMove is one File a move took out of previousParentID.
type fileMove struct {
	fileID           string
	previousParentID string
}

// moveLockedFiles locks the File rows in ID order and moves each one not
// already in folderID.
func moveLockedFiles(tx *gorm.DB, fileIDs []string, folderID *string) ([]fileMove, error) {
	var files []model.File
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id", "folder_id", "delete_requested_at").Where("id IN ?", fileIDs).Order("id").Find(&files).Error; err != nil {
		return nil, err
	}
	if len(files) != len(fileIDs) {
		return nil, errs.FailedPrecondition("one or more files no longer exist")
	}
	nextParentID := pointerValue(folderID)
	moves := make([]fileMove, 0, len(files))
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			return nil, errs.FailedPrecondition("file is pending deletion")
		}
		previousParentID := pointerValue(file.FolderID)
		if previousParentID == nextParentID {
			continue
		}
		if err := tx.Table("file").Where("id = ?", file.ID).Updates(structured.Fields{"folder_id": folderID, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
			return nil, err
		}
		moves = append(moves, fileMove{fileID: file.ID, previousParentID: previousParentID})
	}
	return moves, nil
}

func (s *Service) loadFileManagerFilesByIDs(ctx context.Context, fileIDs []string) ([]*managev1.FileManagerFile, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []fileManagerCatalogRow
	if err := s.db.WithContext(ctx).Raw(`SELECT 'file' AS item_type, id, folder_id AS parent_id, file_name AS name, extension, mime_type, file_size, duration_seconds, uploaded_by_member_id AS member_id, created_at, updated_at, 1 AS total FROM file WHERE id IN ? AND delete_requested_at IS NULL ORDER BY id`, fileIDs).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if len(rows) != len(fileIDs) {
		return nil, errs.FailedPrecondition("one or more files no longer exist")
	}
	memberIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.MemberID != nil {
			memberIDs = append(memberIDs, *row.MemberID)
		}
	}
	memberSummaries := s.memberSummaries
	members, err := memberSummaries.Load(ctx, sliceutil.SortedUniqueTrimmed(memberIDs))
	if err != nil {
		return nil, errs.Internal(err)
	}
	usageCounts, err := loadFileUsageCounts(ctx, s.db, fileIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	byID, _, changed, err := s.finalizeFileManagerDeliveries(ctx, rows, members, usageCounts, nil)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if changed {
		return nil, errs.FailedPrecondition("one or more files changed before delivery")
	}
	result := make([]*managev1.FileManagerFile, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		result = append(result, byID[fileID])
	}
	return result, nil
}

func buildFileDeletionImpact(fileID string, rows []fileUsageRow) *managev1.FileDeletionImpact {
	counts := make(map[managev1.FileUsageDomain]int64)
	for _, row := range rows {
		counts[fileUsageDomain(row.Domain)]++
	}
	domains := make([]managev1.FileUsageDomain, 0, len(counts))
	for domain := range counts {
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	domainCounts := make([]*managev1.FileUsageDomainCount, 0, len(domains))
	for _, domain := range domains {
		domainCounts = append(domainCounts, &managev1.FileUsageDomainCount{Domain: domain, Count: counts[domain]})
	}
	previewSize := min(fileDeletionImpactPreviewSize, len(rows))
	preview := make([]*managev1.FileUsage, 0, previewSize)
	for _, row := range rows[:previewSize] {
		preview = append(preview, fileUsageProto(row))
	}
	return &managev1.FileDeletionImpact{FileId: fileID, TotalUsageCount: int64(len(rows)), DomainCounts: domainCounts, FirstUsages: preview, HasMoreUsages: len(rows) > previewSize}
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func impactsForFiles(fileIDs []string, rows []fileUsageRow) []*managev1.FileDeletionImpact {
	byFile := make(map[string][]fileUsageRow, len(fileIDs))
	for _, row := range rows {
		byFile[row.FileID] = append(byFile[row.FileID], row)
	}
	result := make([]*managev1.FileDeletionImpact, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		result = append(result, buildFileDeletionImpact(fileID, byFile[fileID]))
	}
	return result
}

func (s *Service) validateFileManagerFileIDs(ctx context.Context, fileIDs []string) error {
	var count int64
	if err := s.db.WithContext(ctx).Table("file").Where("id IN ?", fileIDs).Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count != int64(len(fileIDs)) {
		return errs.FailedPrecondition("one or more files no longer exist")
	}
	return nil
}
