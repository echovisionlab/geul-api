package filemedia

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func countFileUsageRows(rows []fileUsageRow) map[string]int32 {
	result := make(map[string]int32)
	for _, row := range rows {
		if result[row.FileID] < int32(^uint32(0)>>1) {
			result[row.FileID]++
		}
	}
	return result
}

func (s *Service) loadVisibleFileUsageCounts(ctx context.Context, fileIDs []string) (map[string]int32, error) {
	user := auth.GetUser(ctx)
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, can, s.spiceDB)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if isAdmin {
		return s.loadLibraryUsageCounts(ctx, fileIDs)
	}
	rows, err := s.loadLibraryFileUsages(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	rows, err = s.filterVisibleFileUsages(ctx, rows)
	if err != nil {
		return nil, err
	}
	return countFileUsageRows(rows), nil
}

func (s *Service) fileManagerFileFromCatalogRow(
	row fileManagerCatalogRow,
	members map[string]MemberSummary,
	usageCount int32,
) (*LibraryFile, error) {
	if row.Extension == nil || row.MimeType == nil || row.FileSize == nil {
		return nil, fmt.Errorf("file catalog row %s is incomplete", row.ID)
	}
	fullName := mediaauth.CanonicalDownloadFilename(&row.Name, row.ID, *row.Extension)
	deliveryResponse, err := s.fileURLsResponseFromStoredFile(row.ID, *row.Extension, *row.MimeType, *row.FileSize, &fullName)
	if err != nil {
		return nil, err
	}
	file := &LibraryFile{
		ID: row.ID, FileName: row.Name, Extension: *row.Extension, MimeType: *row.MimeType,
		FileSize: *row.FileSize, DurationSeconds: row.DurationSeconds, FolderID: row.ParentID,
		CreatedAt: &row.CreatedAt, UpdatedAt: &row.UpdatedAt,
		Delivery: deliveryResponse.Delivery, UsageCount: usageCount,
	}
	if row.MemberID != nil {
		file.UploadedByMember = memberSummaryForID(members, *row.MemberID)
	}
	return file, nil
}

func loadFileUsages(ctx context.Context, store Store, fileIDs []string) ([]fileUsageRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	rows, err := store.FileUsages(ctx, fileIDs)
	if err != nil {
		return nil, err
	}
	return hydrateFileUsageLinks(ctx, store, rows)
}

func hydrateFileUsageLinks(ctx context.Context, store Store, rows []fileUsageRow) ([]fileUsageRow, error) {
	entityIDSet := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		entityIDSet[row.EntityID] = struct{}{}
	}
	entityIDs := make([]string, 0, len(entityIDSet))
	for entityID := range entityIDSet {
		entityIDs = append(entityIDs, entityID)
	}
	sort.Strings(entityIDs)

	var slugs []domain.FileUsageSlug
	if len(entityIDs) > 0 {
		var err error
		slugs, err = store.FileUsageSlugs(ctx, entityIDs)
		if err != nil {
			return nil, err
		}
	}

	slugByResource := make(map[string]string, len(slugs))
	for _, row := range slugs {
		if row.Slug != nil {
			slugByResource[row.Domain+"\x00"+row.EntityID] = *row.Slug
		}
	}
	for index := range rows {
		slug := slugByResource[rows[index].Domain+"\x00"+rows[index].EntityID]
		if link := fileUsageLink(rows[index].Domain, rows[index].EntityID, slug); link != "" {
			rows[index].Link = &link
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		return fileUsageSortKey(rows[i]) < fileUsageSortKey(rows[j])
	})
	return rows, nil
}

func fileUsageSortKey(row fileUsageRow) string {
	return row.FileID + "\x00" + row.Domain + "\x00" + row.EntityID + "\x00" + row.ReferencePath + "\x00" + pointerValue(row.BlockID)
}

func fileUsageLink(domain, entityID, slug string) string {
	target := strings.Trim(strings.TrimSpace(slug), "/")
	if target == "" {
		target = entityID
	}
	switch domain {
	case "post":
		return "/posts/" + target
	case "page":
		return "/" + target
	case "work":
		return "/works/" + target
	case "program_event":
		return "/events/" + target
	case "artist":
		return "/artists/" + target
	case "label":
		return "/labels/" + target
	case "release":
		return "/releases/" + target
	case "track":
		return "/releases/" + target
	case "series":
		return "/series/" + target
	case "form":
		return "/forms/" + target
	case "map_place":
		return "/map/" + target
	case "site_settings":
		return "/admin/settings"
	default:
		return ""
	}
}

func fileUsageResourceTarget(row fileUsageRow, releaseIDByTrackID map[string]string) (string, string, bool) {
	switch row.Domain {
	case "post", "page", "work", "program_event", "release", "artist", "label", "series", "form":
		return row.Domain, row.EntityID, true
	case "track":
		releaseID := releaseIDByTrackID[row.EntityID]
		return "release", releaseID, releaseID != ""
	default:
		return "", "", false
	}
}

func (s *Service) filterVisibleFileUsages(ctx context.Context, rows []fileUsageRow) ([]fileUsageRow, error) {
	user := auth.GetUser(ctx)
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, can, s.spiceDB)
	if err != nil {
		return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(err)
	}
	if isAdmin || len(rows) == 0 {
		return rows, nil
	}
	if user == nil || strings.TrimSpace(user.MemberID.String()) == "" {
		return nil, fmt.Errorf("file usage viewer is missing")
	}
	if user.IdentityID.String() == "" {
		return nil, domainerr.DependencyUnavailable("SpiceDB")
	}

	releaseIDByTrackID, err := s.fileUsageTrackReleaseIDs(ctx, rows)
	if err != nil {
		return nil, err
	}
	allowedTargets, err := s.editableFileUsageTargets(ctx, rows, releaseIDByTrackID)
	if err != nil {
		return nil, err
	}

	visible := make([]fileUsageRow, 0, len(rows))
	for _, row := range rows {
		if row.Domain == "map_place" {
			visible = append(visible, row)
			continue
		}
		resourceType, resourceID, ok := fileUsageResourceTarget(row, releaseIDByTrackID)
		if !ok {
			continue
		}
		if _, allowed := allowedTargets[resourceType+"\x00"+resourceID]; allowed {
			visible = append(visible, row)
		}
	}
	return visible, nil
}

// fileUsageTrackReleaseIDs maps each Track a usage names to its Release,
// whose authority governs the usage.
func (s *Service) fileUsageTrackReleaseIDs(ctx context.Context, rows []fileUsageRow) (map[string]string, error) {
	trackIDs := make([]string, 0)
	for _, row := range rows {
		if row.Domain == "track" {
			trackIDs = append(trackIDs, row.EntityID)
		}
	}
	releaseIDByTrackID := make(map[string]string, len(trackIDs))
	if len(trackIDs) == 0 {
		return releaseIDByTrackID, nil
	}
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		releaseIDByTrackID, err = store.TrackReleaseIDs(ctx, sliceutil.SortedUniqueTrimmed(trackIDs))
		return err
	})
	if err != nil {
		return nil, err
	}
	return releaseIDByTrackID, nil
}

// editableFileUsageTargets checks, in key order, which usage owners the
// viewer may edit, keyed by resource type and ID.
func (s *Service) editableFileUsageTargets(ctx context.Context, rows []fileUsageRow, releaseIDByTrackID map[string]string) (map[string]struct{}, error) {
	type permissionTarget struct {
		resourceType string
		resourceID   string
	}
	targetsByKey := make(map[string]permissionTarget)
	for _, row := range rows {
		resourceType, resourceID, ok := fileUsageResourceTarget(row, releaseIDByTrackID)
		if ok {
			targetsByKey[resourceType+"\x00"+resourceID] = permissionTarget{resourceType: resourceType, resourceID: resourceID}
		}
	}
	targetKeys := make([]string, 0, len(targetsByKey))
	for key := range targetsByKey {
		targetKeys = append(targetKeys, key)
	}
	sort.Strings(targetKeys)
	allowedTargets := make(map[string]struct{}, len(targetKeys))
	for _, key := range targetKeys {
		target := targetsByKey[key]
		can, canErr := uploadPermissionEditCan(target.resourceType, target.resourceID)
		if canErr != nil {
			slog.ErrorContext(ctx, "Failed to build file usage visibility policy", failurecause.Attr(canErr), "resource_type", target.resourceType, "resource_id", target.resourceID)
			continue
		}
		decision, decisionErr := auth.AuthorizationDecision(ctx, can)
		if decisionErr != nil {
			return nil, domainerr.AuthenticationRequired().WithCause(decisionErr)
		}
		allowed, checkErr := s.spiceDB.Can(ctx, decision)
		if checkErr != nil {
			return nil, domainerr.DependencyUnavailable("SpiceDB").WithCause(checkErr)
		}
		if allowed {
			allowedTargets[key] = struct{}{}
		}
	}
	return allowedTargets, nil
}

func fileUsageDomain(kind string) UsageDomain {
	switch kind {
	case "post":
		return domain.UsagePost
	case "page":
		return domain.UsagePage
	case "work":
		return domain.UsageWork
	case "site_settings":
		return domain.UsageSiteSettings
	case "release":
		return domain.UsageRelease
	case "track":
		return domain.UsageTrack
	case "artist":
		return domain.UsageArtist
	case "label":
		return domain.UsageLabel
	case "client":
		return domain.UsageClient
	case "series":
		return domain.UsageSeries
	case "form":
		return domain.UsageForm
	case "program_event":
		return domain.UsageProgramEvent
	case "map_place":
		return domain.UsageMapPlace
	default:
		return domain.UsageUnspecified
	}
}

func fileUsageView(row fileUsageRow) *UsageView {
	return &UsageView{
		Domain: fileUsageDomain(row.Domain), EntityID: row.EntityID, ReferencePath: row.ReferencePath,
		BlockID: row.BlockID, Count: 1, BlockType: row.BlockType, Title: row.Title, Link: row.Link,
	}
}

// ListFileUsagesQuery pages the usages of one File the caller may see.
type ListFileUsagesQuery struct {
	FileID    string
	PageToken string
	PageSize  int32
}

// FileUsagePage is one page of a File's usages.
type FileUsagePage struct {
	Usages        []*UsageView
	NextPageToken *string
	Total         int64
}

func (s *Service) ListFileUsages(ctx context.Context, req ListFileUsagesQuery) (FileUsagePage, error) {
	if err := requireFileDownloadAuthor(ctx, s.spiceDB); err != nil {
		return FileUsagePage{}, err
	}
	fileID, err := normalizeFileManagerUUID(req.FileID, "file_id", false)
	if err != nil {
		return FileUsagePage{}, err
	}
	offset, err := decodeFileManagerPageToken(req.PageToken)
	if err != nil {
		return FileUsagePage{}, err
	}
	if err := s.requireFileManagerFile(ctx, *fileID); err != nil {
		return FileUsagePage{}, err
	}
	rows, err := s.loadLibraryFileUsages(ctx, []string{*fileID})
	if err != nil {
		return FileUsagePage{}, err
	}
	rows, err = s.filterVisibleFileUsages(ctx, rows)
	if err != nil {
		return FileUsagePage{}, err
	}
	total := len(rows)
	pageSize := fileManagerPageSize(req.PageSize, fileManagerMaxUsagePageSize)
	start := min(offset, total)
	end := min(start+pageSize, total)
	rows = rows[start:end]
	usages := make([]*UsageView, 0, len(rows))
	for _, row := range rows {
		usages = append(usages, fileUsageView(row))
	}
	var next *string
	if offset+len(rows) < total {
		next = encodeFileManagerPageToken(offset + len(rows))
	}
	return FileUsagePage{Usages: usages, NextPageToken: next, Total: int64(total)}, nil
}

func (s *Service) requireFileManagerFile(ctx context.Context, fileID string) error {
	var count int64
	if err := s.units.Read(ctx, func(store Store) error { var err error; count, err = store.ActiveFileCount(ctx, fileID); return err }); err != nil {
		return err
	}
	if count == 0 {
		return domainerr.ResourceNotFound("file", fileID)
	}
	return nil
}

func (s *Service) loadLibraryFileUsages(ctx context.Context, ids []string) ([]fileUsageRow, error) {
	var rows []fileUsageRow
	err := s.units.Read(ctx, func(store Store) error { var err error; rows, err = loadFileUsages(ctx, store, ids); return err })
	return rows, err
}
func (s *Service) loadLibraryUsageCounts(ctx context.Context, ids []string) (map[string]int32, error) {
	rows, err := s.loadLibraryFileUsages(ctx, ids)
	if err != nil {
		return nil, err
	}
	return countFileUsageRows(rows), nil
}
