package filemedia

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func loadFileUsageCounts(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]int32, error) {
	rows, err := loadFileUsages(ctx, db, fileIDs)
	if err != nil {
		return nil, err
	}
	return countFileUsageRows(rows), nil
}

func countFileUsageRows(rows []fileUsageRow) map[string]int32 {
	result := make(map[string]int32)
	for _, row := range rows {
		if result[row.FileID] < int32(^uint32(0)>>1) {
			result[row.FileID]++
		}
	}
	return result
}

func (s *FileService) loadVisibleFileUsageCounts(ctx context.Context, fileIDs []string) (map[string]int32, error) {
	user := auth.GetUser(ctx)
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, can, s.spiceDB)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if isAdmin {
		return loadFileUsageCounts(ctx, s.db, fileIDs)
	}
	rows, err := loadFileUsages(ctx, s.db, fileIDs)
	if err != nil {
		return nil, err
	}
	rows, err = s.filterVisibleFileUsages(ctx, rows)
	if err != nil {
		return nil, err
	}
	return countFileUsageRows(rows), nil
}

func (s *FileService) fileManagerFileFromCatalogRow(
	row fileManagerCatalogRow,
	members map[string]*commonv1.MemberSummary,
	usageCount int32,
) (*managev1.FileManagerFile, error) {
	if row.Extension == nil || row.MimeType == nil || row.FileSize == nil {
		return nil, fmt.Errorf("file catalog row %s is incomplete", row.ID)
	}
	fullName := CanonicalDownloadFilename(&row.Name, row.ID, *row.Extension)
	deliveryResponse, err := s.fileURLsResponseFromStoredFile(row.ID, *row.Extension, *row.MimeType, *row.FileSize, &fullName)
	if err != nil {
		return nil, err
	}
	file := &managev1.FileManagerFile{
		Id: row.ID, FileName: row.Name, Extension: *row.Extension, MimeType: *row.MimeType,
		FileSize: *row.FileSize, DurationSeconds: row.DurationSeconds, FolderId: row.ParentID,
		CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt),
		Delivery: deliveryResponse.Delivery, UsageCount: usageCount,
	}
	if row.MemberID != nil {
		file.UploadedByMember = members[*row.MemberID]
	}
	return file, nil
}

func loadFileUsages(ctx context.Context, db *gorm.DB, fileIDs []string) ([]fileUsageRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []fileUsageRow
	if err := db.WithContext(ctx).Raw(
		"SELECT file_id::text AS file_id, domain, entity_id, reference_path, block_id, block_type, label FROM ("+fileUsageUnionSQL+") AS usage WHERE file_id IN ? ORDER BY file_id, domain, entity_id, reference_path, COALESCE(block_id, '')",
		fileIDs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	return hydrateFileUsageLinks(ctx, db, rows)
}

func hydrateFileUsageLinks(ctx context.Context, db *gorm.DB, rows []fileUsageRow) ([]fileUsageRow, error) {
	entityIDSet := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		entityIDSet[row.EntityID] = struct{}{}
	}
	entityIDs := make([]string, 0, len(entityIDSet))
	for entityID := range entityIDSet {
		entityIDs = append(entityIDs, entityID)
	}
	sort.Strings(entityIDs)

	type slugRow struct {
		Domain   string  `gorm:"column:domain"`
		EntityID string  `gorm:"column:entity_id"`
		Slug     *string `gorm:"column:slug"`
	}
	var slugs []slugRow
	if len(entityIDs) > 0 {
		if err := db.WithContext(ctx).Raw(`
			SELECT 'post' AS domain, id::text AS entity_id, slug FROM post WHERE id::text IN ?
			UNION ALL SELECT 'page', id::text, slug FROM page WHERE id::text IN ?
			UNION ALL SELECT 'work', id::text, slug FROM work WHERE id::text IN ?
			UNION ALL SELECT 'program_event', id::text, slug FROM program_event WHERE id::text IN ?
			UNION ALL SELECT 'artist', id::text, slug FROM artist WHERE id::text IN ?
			UNION ALL SELECT 'label', id::text, slug FROM label WHERE id::text IN ?
			UNION ALL SELECT 'release', id::text, slug FROM release WHERE id::text IN ?
			UNION ALL SELECT 'series', id::text, slug FROM series WHERE id::text IN ?
			UNION ALL SELECT 'form', id::text, slug FROM form WHERE id::text IN ?
			UNION ALL SELECT 'track', t.id::text, COALESCE(NULLIF(r.slug, ''), r.id::text)
			FROM track t JOIN release r ON r.id = t.release_id WHERE t.id::text IN ?
		`, entityIDs, entityIDs, entityIDs, entityIDs, entityIDs, entityIDs, entityIDs, entityIDs, entityIDs, entityIDs).Scan(&slugs).Error; err != nil {
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

func (s *FileService) filterVisibleFileUsages(ctx context.Context, rows []fileUsageRow) ([]fileUsageRow, error) {
	user := auth.GetUser(ctx)
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	isAdmin, err := checkSpiceDBCan(ctx, user, can, s.spiceDB)
	if err != nil {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}
	if isAdmin || len(rows) == 0 {
		return rows, nil
	}
	if user == nil || strings.TrimSpace(user.MemberID.String()) == "" {
		return nil, fmt.Errorf("file usage viewer is missing")
	}
	if s.spiceDB == nil || user.IdentityID.String() == "" {
		return nil, errs.DependencyUnavailable("SpiceDB")
	}

	trackIDs := make([]string, 0)
	for _, row := range rows {
		if row.Domain == "track" {
			trackIDs = append(trackIDs, row.EntityID)
		}
	}
	releaseIDByTrackID := make(map[string]string, len(trackIDs))
	if len(trackIDs) > 0 {
		var tracks []struct {
			ID        string `gorm:"column:id"`
			ReleaseID string `gorm:"column:release_id"`
		}
		if err := s.db.WithContext(ctx).Table("track").Select("id, release_id").Where("id IN ?", normalizedSortedFileIDs(trackIDs)).Find(&tracks).Error; err != nil {
			return nil, err
		}
		for _, track := range tracks {
			releaseIDByTrackID[track.ID] = track.ReleaseID
		}
	}

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
			continue
		}
		decision, decisionErr := auth.AuthorizationDecision(ctx, can)
		if decisionErr != nil {
			return nil, errs.AuthenticationRequired()
		}
		allowed, checkErr := s.spiceDB.Can(ctx, decision)
		if checkErr != nil {
			return nil, errs.DependencyUnavailable("SpiceDB")
		}
		if allowed {
			allowedTargets[key] = struct{}{}
		}
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

func fileUsageDomain(domain string) managev1.FileUsageDomain {
	switch domain {
	case "post":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_POST
	case "page":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PAGE
	case "work":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_WORK
	case "site_settings":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_SITE_SETTINGS
	case "release":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_RELEASE
	case "track":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_TRACK
	case "artist":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_ARTIST
	case "label":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_LABEL
	case "client":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_CLIENT
	case "series":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_SERIES
	case "form":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_FORM
	case "program_event":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_PROGRAM_EVENT
	case "map_place":
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_MAP_PLACE
	default:
		return managev1.FileUsageDomain_FILE_USAGE_DOMAIN_UNSPECIFIED
	}
}

func fileUsageProto(row fileUsageRow) *managev1.FileUsage {
	return &managev1.FileUsage{
		Domain: fileUsageDomain(row.Domain), EntityId: row.EntityID, ReferencePath: row.ReferencePath,
		BlockId: row.BlockID, Count: 1, BlockType: row.BlockType, Title: row.Title, Link: row.Link,
	}
}

func (s *FileService) ListFileUsages(
	ctx context.Context,
	req *connect.Request[managev1.ListFileUsagesRequest],
) (*connect.Response[managev1.ListFileUsagesResponse], error) {
	if _, err := requireFileDownloadAuthor(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	fileID, err := normalizeFileManagerUUID(req.Msg.FileId, "file_id", false)
	if err != nil {
		return nil, err
	}
	offset, err := decodeFileManagerPageToken(req.Msg.PageToken)
	if err != nil {
		return nil, err
	}
	if err := s.requireFileManagerFile(ctx, *fileID); err != nil {
		return nil, err
	}
	rows, err := loadFileUsages(ctx, s.db, []string{*fileID})
	if err != nil {
		return nil, errs.Internal(err)
	}
	rows, err = s.filterVisibleFileUsages(ctx, rows)
	if err != nil {
		return nil, errs.Internal(err)
	}
	total := len(rows)
	pageSize := fileManagerPageSize(req.Msg.PageSize, fileManagerMaxUsagePageSize)
	start := min(offset, total)
	end := min(start+pageSize, total)
	rows = rows[start:end]
	usages := make([]*managev1.FileUsage, 0, len(rows))
	for _, row := range rows {
		usages = append(usages, fileUsageProto(row))
	}
	var next *string
	if offset+len(rows) < total {
		next = encodeFileManagerPageToken(offset + len(rows))
	}
	return connect.NewResponse(&managev1.ListFileUsagesResponse{Usages: usages, NextPageToken: next, Total: int64(total)}), nil
}

func (s *FileService) requireFileManagerFile(ctx context.Context, fileID string) error {
	var count int64
	if err := s.db.WithContext(ctx).Table("file").Where("id = ? AND delete_requested_at IS NULL", fileID).Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count == 0 {
		return errs.NotFound("file", fileID)
	}
	return nil
}
