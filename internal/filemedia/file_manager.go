package filemedia

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

const (
	fileManagerDefaultPageSize      = 50
	fileManagerMaxDirectoryPageSize = 10_000
	fileManagerMaxSearchPageSize    = 100
	fileManagerMaxUsagePageSize     = 100
	fileManagerMaxMutationSize      = 100
	fileDeletionImpactPreviewSize   = 5
)

func fileManagerAuthority(ctx context.Context) (*auth.UserInfo, policyv1.Can, error) {
	user := auth.GetUser(ctx)
	if user == nil || !user.Authenticated || user.IdentityID == "" {
		return nil, policyv1.Can{}, domainerr.AuthenticationRequired()
	}
	if user.Banned {
		return nil, policyv1.Can{}, domainerr.AccountBanned()
	}
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return nil, policyv1.Can{}, err
	}
	return user, can, nil
}

func requireFileManagerAdmin(ctx context.Context, spiceDB authz.AuthorizationDecisionChecker) (*auth.UserInfo, error) {
	user, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAdminCan(ctx, spiceDB, can); err != nil {
		return nil, err
	}
	return user, nil
}

type fileManagerCatalogRow = domain.CatalogRow

type fileManagerPathSegmentJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type fileUsageRow = domain.FileUsageRow

func normalizeFileManagerUUID(value, field string, optional bool) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" && optional {
		return nil, nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil, domainerr.InvalidField(field, "must be a UUID")
	}
	normalized := parsed.String()
	return &normalized, nil
}

func normalizeFileManagerName(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 255 {
		return "", domainerr.InvalidField(field, "must contain 1 to 255 characters")
	}
	if value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return "", domainerr.InvalidField(field, "must not be '.', '..', or contain a path separator")
	}
	return value, nil
}

func fileManagerPageSize(value int32, maximum int) int {
	if value <= 0 {
		return fileManagerDefaultPageSize
	}
	if int(value) > maximum {
		return maximum
	}
	return int(value)
}

func decodeFileManagerPageToken(token string) (int, error) {
	if strings.TrimSpace(token) == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return 0, domainerr.InvalidField("page_token", "is invalid")
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 || parts[0] != "v1" {
		return 0, domainerr.InvalidField("page_token", "is invalid")
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return 0, domainerr.InvalidField("page_token", "is invalid")
	}
	return offset, nil
}

func encodeFileManagerPageToken(offset int) *string {
	value := base64.RawURLEncoding.EncodeToString([]byte("v1:" + strconv.Itoa(offset)))
	return &value
}

func (s *Service) requireFileManagerFolder(ctx context.Context, folderID *string) error {
	if folderID == nil {
		return nil
	}
	return s.units.Read(ctx, func(store Store) error {
		exists, err := store.FolderExists(ctx, *folderID)
		if err != nil {
			return err
		}
		if !exists {
			return domainerr.ResourceNotFound("folder", *folderID)
		}
		return nil
	})
}

func fileManagerFolderPath(raw []byte) ([]*FolderPathSegment, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var decoded []fileManagerPathSegmentJSON
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	path := make([]*FolderPathSegment, 0, len(decoded))
	for _, criteria := range decoded {
		path = append(path, &FolderPathSegment{ID: criteria.ID, Name: criteria.Name})
	}
	return path, nil
}

// ListFileManagerItemsQuery lists one File library folder, or searches the
// library when Query is set.
type ListFileManagerItemsQuery struct {
	FolderID           string
	UploadedByMemberID string
	Query              string
	MimeTypePrefix     string
	PageToken          string
	PageSize           int32
	SortField          LibrarySortField
	SortOrder          SortOrder
}

// FileManagerItemPage is one page of File library items.
type FileManagerItemPage struct {
	Items         []*LibraryItem
	NextPageToken *string
	Total         int64
}

func (s *Service) ListFileManagerItems(ctx context.Context, query ListFileManagerItemsQuery) (FileManagerItemPage, error) {
	return s.listFileManagerItems(ctx, query, true)
}

func (s *Service) listFileManagerItems(
	ctx context.Context,
	req ListFileManagerItemsQuery,
	allowChangedRetry bool,
) (FileManagerItemPage, error) {
	if err := requireFileDownloadAuthor(ctx, s.spiceDB); err != nil {
		return FileManagerItemPage{}, err
	}
	filter, err := s.fileManagerCatalogFilter(ctx, req)
	if err != nil {
		return FileManagerItemPage{}, err
	}
	rows, err := s.queryFileManagerCatalog(ctx, filter)
	if err != nil {
		return FileManagerItemPage{}, err
	}

	memberIDs := make([]string, 0, len(rows))
	fileIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.MemberID != nil {
			memberIDs = append(memberIDs, *row.MemberID)
		}
		if row.ItemType == "file" {
			fileIDs = append(fileIDs, row.ID)
		}
	}
	memberSummaries := s.memberSummaries
	members, err := memberSummaries.Load(ctx, sliceutil.SortedUniqueTrimmed(memberIDs))
	if err != nil {
		return FileManagerItemPage{}, err
	}
	usageCounts, err := s.loadVisibleFileUsageCounts(ctx, fileIDs)
	if err != nil {
		return FileManagerItemPage{}, err
	}
	fencedFiles, _, changed, err := s.finalizeFileManagerDeliveries(ctx, rows, members, usageCounts, nil)
	if err != nil {
		return FileManagerItemPage{}, err
	}
	if changed {
		if allowChangedRetry {
			return s.listFileManagerItems(ctx, req, false)
		}
		return FileManagerItemPage{}, domainerr.DependencyUnavailable("file catalog")
	}

	items, err := fileManagerItemsFromCatalog(rows, members, fencedFiles)
	if err != nil {
		return FileManagerItemPage{}, err
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].Total
	}
	var next *string
	if int64(filter.offset+len(rows)) < total {
		next = encodeFileManagerPageToken(filter.offset + len(rows))
	}
	return FileManagerItemPage{Items: items, NextPageToken: next, Total: total}, nil
}

// fileManagerCatalogFilter is a validated File Manager listing: a directory
// when query is empty, a search under the folder otherwise.
type fileManagerCatalogFilter struct {
	parent         string
	uploader       string
	query          string
	mimePrefix     string
	includeFolders bool
	sortField      LibrarySortField
	sortOrder      SortOrder
	pageSize       int
	offset         int
}

func (s *Service) fileManagerCatalogFilter(ctx context.Context, req ListFileManagerItemsQuery) (fileManagerCatalogFilter, error) {
	folderID, err := normalizeFileManagerUUID(req.FolderID, "folder_id", true)
	if err != nil {
		return fileManagerCatalogFilter{}, err
	}
	uploaderID, err := normalizeFileManagerUUID(req.UploadedByMemberID, "uploaded_by_member_id", true)
	if err != nil {
		return fileManagerCatalogFilter{}, err
	}
	offset, err := decodeFileManagerPageToken(req.PageToken)
	if err != nil {
		return fileManagerCatalogFilter{}, err
	}
	query := strings.TrimSpace(req.Query)
	mimePrefix := strings.TrimSpace(strings.ToLower(req.MimeTypePrefix))
	if query == "" || folderID != nil {
		if err := s.requireFileManagerFolder(ctx, folderID); err != nil {
			return fileManagerCatalogFilter{}, err
		}
	}
	pageSizeMaximum := fileManagerMaxDirectoryPageSize
	if query != "" {
		pageSizeMaximum = fileManagerMaxSearchPageSize
	}
	filter := fileManagerCatalogFilter{
		query: query, mimePrefix: mimePrefix, includeFolders: mimePrefix == "" && uploaderID == nil,
		sortField: req.SortField, sortOrder: req.SortOrder,
		pageSize: fileManagerPageSize(req.PageSize, pageSizeMaximum), offset: offset,
	}
	if folderID != nil {
		filter.parent = *folderID
	}
	if uploaderID != nil {
		filter.uploader = *uploaderID
	}
	return filter, nil
}

// fileManagerItemsFromCatalog renders catalog rows in order, with each File
// as the delivery fence built it.
func fileManagerItemsFromCatalog(
	rows []fileManagerCatalogRow,
	members map[string]MemberSummary,
	fencedFiles map[string]*LibraryFile,
) ([]*LibraryItem, error) {
	items := make([]*LibraryItem, 0, len(rows))
	for _, row := range rows {
		folderPath, pathErr := fileManagerFolderPath(row.FolderPath)
		if pathErr != nil {
			return nil, pathErr
		}
		if row.ItemType == "folder" {
			folder := &FolderView{
				ID: row.ID, ParentID: row.ParentID, Name: row.Name,
				CreatedAt: &row.CreatedAt, UpdatedAt: &row.UpdatedAt,
			}
			if row.MemberID != nil {
				folder.CreatedByMember = memberSummaryForID(members, *row.MemberID)
			}
			items = append(items, &LibraryItem{
				Type:   domain.LibraryItemFolder,
				Folder: folder, FolderPath: folderPath,
			})
			continue
		}
		file := fencedFiles[row.ID]
		if file == nil {
			return nil, domainerr.DependencyUnavailable("file catalog")
		}
		items = append(items, &LibraryItem{
			Type: domain.LibraryItemFile,
			File: file, FolderPath: folderPath,
		})
	}
	return items, nil
}

func (s *Service) queryFileManagerCatalog(ctx context.Context, filter fileManagerCatalogFilter) ([]fileManagerCatalogRow, error) {
	sort := domain.CatalogSortName
	switch filter.sortField {
	case LibrarySortCreatedAt:
		sort = domain.CatalogSortCreated
	case LibrarySortUpdatedAt:
		sort = domain.CatalogSortUpdated
	case LibrarySortFileSize:
		sort = domain.CatalogSortSize
	}
	query := domain.CatalogQuery{Parent: filter.parent, Uploader: filter.uploader, Query: filter.query, MimePrefix: filter.mimePrefix, IncludeFolders: filter.includeFolders, SortField: sort, Descending: filter.sortOrder == SortDesc, PageSize: filter.pageSize, Offset: filter.offset}
	var rows []fileManagerCatalogRow
	err := s.units.Read(ctx, func(store Store) error { var err error; rows, err = store.Catalog(ctx, query); return err })
	return rows, err
}
