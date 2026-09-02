package filemedia

import (
	"context"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func (s *FileService) RenameFile(ctx context.Context, req *connect.Request[managev1.RenameFileRequest]) (*connect.Response[managev1.RenameFileResponse], error) {
	_, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	fileID, err := normalizeFileManagerUUID(req.Msg.FileId, "file_id", false)
	if err != nil {
		return nil, err
	}
	name, err := normalizeFileManagerName(req.Msg.FileName, "file_name")
	if err != nil {
		return nil, err
	}
	var file model.File
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if s.testBeforeFileMutationPrincipal != nil {
			s.testBeforeFileMutationPrincipal(tx, []string{*fileID})
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, can); err != nil {
			return err
		}
		if s.testAfterFileMutationPrincipal != nil {
			s.testAfterFileMutationPrincipal([]string{*fileID})
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "file_name", "extension", "delete_requested_at").Where("id = ?", *fileID).First(&file).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return errs.NotFound("file", *fileID)
			}
			return errs.Internal(err)
		}
		if file.DeleteRequestedAt != nil {
			return errs.FailedPrecondition("file is pending deletion")
		}
		if strings.EqualFold(filepathExtension(name), file.Extension) {
			return errs.InvalidArgument("file_name", "must not include the file extension")
		}
		if file.FileName == name {
			return nil
		}
		if err := tx.Table("file").Where("id = ?", *fileID).Updates(structured.Fields{"file_name": name, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
			return errs.Internal(err)
		}
		return appendFileRenamedAudit(ctx, tx, s.auditWriter, *fileID)
	}); err != nil {
		return nil, err
	}
	response, err := s.GetFile(ctx, connect.NewRequest(&managev1.GetFileRequest{FileId: *fileID}))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.RenameFileResponse{File: response.Msg.File}), nil
}

func (s *FileService) MoveFiles(ctx context.Context, req *connect.Request[managev1.MoveFilesRequest]) (*connect.Response[managev1.MoveFilesResponse], error) {
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
	folderID, err := normalizeFileManagerUUID(req.Msg.GetFolderId(), "folder_id", true)
	if err != nil {
		return nil, err
	}
	if err := s.requireFileManagerFolder(ctx, folderID); err != nil {
		return nil, err
	}
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
		var files []model.File
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "folder_id", "delete_requested_at").Where("id IN ?", fileIDs).Order("id").Find(&files).Error; err != nil {
			return err
		}
		if len(files) != len(fileIDs) {
			return errs.FailedPrecondition("one or more files no longer exist")
		}
		nextParentID := pointerValue(folderID)
		for _, file := range files {
			if file.DeleteRequestedAt != nil {
				return errs.FailedPrecondition("file is pending deletion")
			}
			previousParentID := pointerValue(file.FolderID)
			if previousParentID == nextParentID {
				continue
			}
			if err := tx.Table("file").Where("id = ?", file.ID).Updates(structured.Fields{"folder_id": folderID, "updated_at": gorm.Expr("CURRENT_TIMESTAMP")}).Error; err != nil {
				return err
			}
			if err := appendFileMovedAudit(ctx, tx, s.auditWriter, file.ID, previousParentID, nextParentID); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	files, err := s.loadFileManagerFilesByIDs(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.MoveFilesResponse{Files: files}), nil
}

func (s *FileService) loadFileManagerFilesByIDs(ctx context.Context, fileIDs []string) ([]*managev1.FileManagerFile, error) {
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
	memberSummaries, err := requireMemberSummaries(s.memberSummaries)
	if err != nil {
		return nil, err
	}
	members, err := memberSummaries.Load(ctx, normalizedSortedFileIDs(memberIDs))
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

func (s *FileService) validateFileManagerFileIDs(ctx context.Context, fileIDs []string) error {
	var count int64
	if err := s.db.WithContext(ctx).Table("file").Where("id IN ?", fileIDs).Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count != int64(len(fileIDs)) {
		return errs.FailedPrecondition("one or more files no longer exist")
	}
	return nil
}
