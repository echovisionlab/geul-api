package filemedia

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
)

// RenameFileCommand renames a File without changing its extension.
type RenameFileCommand struct {
	FileID   string
	FileName string
}

func (s *Service) RenameFile(ctx context.Context, command RenameFileCommand) (_ *LibraryFile, resultErr error) {
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
	if _, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[bool, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) (bool, error) {
			s.afterFileMutationPrincipal([]string{*fileID})
			return renameLockedFile(ctx, store, *fileID, name)
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
func renameLockedFile(ctx context.Context, store Store, fileID, name string) (bool, error) {
	file, err := store.LockFileForRename(ctx, fileID)
	if errors.Is(err, ErrFileNotFound) {
		return false, domainerr.ResourceNotFound("file", fileID)
	}
	if err != nil {
		return false, err
	}
	if file.DeleteRequestedAt != nil {
		return false, domainerr.Precondition("file is pending deletion")
	}
	if strings.EqualFold(mediaauth.FilenameExtension(name), file.Extension) {
		return false, domainerr.InvalidField("file_name", "must not include the file extension")
	}
	if file.FileName == name {
		return false, nil
	}
	if err := store.RenameFile(ctx, fileID, name); err != nil {
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

func (s *Service) MoveFiles(ctx context.Context, command MoveFilesCommand) ([]*LibraryFile, error) {
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
	if _, err := mutation.Run(ctx, s.storeMutations, mutation.StoreSpec[[]fileMove, Store]{
		Access: mutation.FreshAdminCan(can, nil),
		Change: func(ctx context.Context, store Store) ([]fileMove, error) {
			s.afterFileMutationPrincipal(fileIDs)
			return moveLockedFiles(ctx, store, fileIDs, folderID)
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
func moveLockedFiles(ctx context.Context, store Store, fileIDs []string, folderID *string) ([]fileMove, error) {
	files, err := store.LockFilesForMove(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	if len(files) != len(fileIDs) {
		return nil, domainerr.Precondition("one or more files no longer exist")
	}
	nextParentID := pointerValue(folderID)
	moves := make([]fileMove, 0, len(files))
	for _, file := range files {
		if file.DeleteRequestedAt != nil {
			return nil, domainerr.Precondition("file is pending deletion")
		}
		previousParentID := pointerValue(file.FolderID)
		if previousParentID == nextParentID {
			continue
		}
		if err := store.MoveFile(ctx, file.ID, folderID); err != nil {
			return nil, err
		}
		moves = append(moves, fileMove{fileID: file.ID, previousParentID: previousParentID})
	}
	return moves, nil
}

func (s *Service) loadFileManagerFilesByIDs(ctx context.Context, fileIDs []string) ([]*LibraryFile, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []fileManagerCatalogRow
	if err := s.units.Read(ctx, func(store Store) error { var err error; rows, err = store.FileCatalogs(ctx, fileIDs); return err }); err != nil {
		return nil, err
	}

	if len(rows) != len(fileIDs) {
		return nil, domainerr.Precondition("one or more files no longer exist")
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
		return nil, err
	}
	usageCounts, err := s.loadLibraryUsageCounts(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	byID, _, changed, err := s.finalizeFileManagerDeliveries(ctx, rows, members, usageCounts, nil)
	if err != nil {
		return nil, err
	}
	if changed {
		return nil, domainerr.Precondition("one or more files changed before delivery")
	}
	result := make([]*LibraryFile, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		result = append(result, byID[fileID])
	}
	return result, nil
}

func buildFileDeletionImpact(fileID string, rows []fileUsageRow) *DeletionImpact {
	counts := make(map[UsageDomain]int64)
	for _, row := range rows {
		counts[fileUsageDomain(row.Domain)]++
	}
	domains := make([]UsageDomain, 0, len(counts))
	for domain := range counts {
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	domainCounts := make([]*UsageDomainCount, 0, len(domains))
	for _, domain := range domains {
		domainCounts = append(domainCounts, &UsageDomainCount{Domain: domain, Count: counts[domain]})
	}
	previewSize := min(fileDeletionImpactPreviewSize, len(rows))
	preview := make([]*UsageView, 0, previewSize)
	for _, row := range rows[:previewSize] {
		preview = append(preview, fileUsageView(row))
	}
	return &DeletionImpact{FileID: fileID, TotalUsageCount: int64(len(rows)), DomainCounts: domainCounts, FirstUsages: preview, HasMoreUsages: len(rows) > previewSize}
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func impactsForFiles(fileIDs []string, rows []fileUsageRow) []*DeletionImpact {
	byFile := make(map[string][]fileUsageRow, len(fileIDs))
	for _, row := range rows {
		byFile[row.FileID] = append(byFile[row.FileID], row)
	}
	result := make([]*DeletionImpact, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		result = append(result, buildFileDeletionImpact(fileID, byFile[fileID]))
	}
	return result
}

func (s *Service) validateFileManagerFileIDs(ctx context.Context, fileIDs []string) error {
	var count int64
	if err := s.units.Read(ctx, func(store Store) error { var err error; count, err = store.FileCount(ctx, fileIDs); return err }); err != nil {
		return err
	}
	if count != int64(len(fileIDs)) {
		return domainerr.Precondition("one or more files no longer exist")
	}
	return nil
}
