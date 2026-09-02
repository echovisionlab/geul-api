package filemedia

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authz"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
		return nil, policyv1.Can{}, errs.AuthenticationRequired()
	}
	if user.Banned {
		return nil, policyv1.Can{}, errs.AccountBanned()
	}
	can, err := policyv1.File.ManageLibrary()
	if err != nil {
		return nil, policyv1.Can{}, errs.Internal(err)
	}
	return user, can, nil
}

func requireFileManagerAdmin(ctx context.Context, spiceDB *auth.SpiceDBClient) (*auth.UserInfo, error) {
	user, can, err := fileManagerAuthority(ctx)
	if err != nil {
		return nil, err
	}
	if err := authz.RequireAdminCan(ctx, spiceDB, can); err != nil {
		return nil, err
	}
	return user, nil
}

type fileManagerCatalogRow struct {
	ItemType        string    `gorm:"column:item_type"`
	ID              string    `gorm:"column:id"`
	ParentID        *string   `gorm:"column:parent_id"`
	Name            string    `gorm:"column:name"`
	Extension       *string   `gorm:"column:extension"`
	MimeType        *string   `gorm:"column:mime_type"`
	FileSize        *int64    `gorm:"column:file_size"`
	DurationSeconds *int32    `gorm:"column:duration_seconds"`
	MemberID        *string   `gorm:"column:member_id"`
	CreatedAt       time.Time `gorm:"column:created_at"`
	UpdatedAt       time.Time `gorm:"column:updated_at"`
	Total           int64     `gorm:"column:total"`
	FolderPath      []byte    `gorm:"column:folder_path"`
}

type fileManagerPathSegmentJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type fileUsageRow struct {
	FileID        string  `gorm:"column:file_id"`
	Domain        string  `gorm:"column:domain"`
	EntityID      string  `gorm:"column:entity_id"`
	ReferencePath string  `gorm:"column:reference_path"`
	BlockID       *string `gorm:"column:block_id"`
	BlockType     *string `gorm:"column:block_type"`
	Title         *string `gorm:"column:label"`
	Link          *string `gorm:"-"`
}

// fileUsageUnionSQL is the read projection for product references. Deletion
// authority remains fileAttachmentReferenceRegistry; this projection only
// supplies File Manager usage details and preflight summaries.
const fileUsageUnionSQL = `
SELECT file_id, 'artist' AS domain, artist_id::text AS entity_id, 'editor' AS reference_path, NULL::text AS block_id, NULL::text AS block_type, NULL::text AS label FROM artist_file
UNION ALL SELECT file_id, 'release', release_id::text, 'files', NULL::text, NULL::text, NULL::text FROM release_file
UNION ALL SELECT cba.file_id, 'post', p.id::text, cba.reference_path, cb.id::text, cb.kind::text, COALESCE(NULLIF(pt.title, ''), NULLIF(p.slug, ''), p.id::text) FROM content_block_attachment cba JOIN content_block cb ON cb.id = cba.block_id JOIN post p ON p.content_document_id = cb.document_id LEFT JOIN post_translation pt ON pt.entity_id = p.id AND pt.locale = p.source_locale WHERE cba.selector_kind = 'active'
UNION ALL SELECT cba.file_id, 'page', p.id::text, cba.reference_path, cb.id::text, cb.kind::text, COALESCE(NULLIF(pt.title, ''), NULLIF(p.slug, ''), p.id::text) FROM content_block_attachment cba JOIN content_block cb ON cb.id = cba.block_id JOIN page p ON p.content_document_id = cb.document_id LEFT JOIN page_translation pt ON pt.entity_id = p.id AND pt.locale = p.source_locale WHERE cba.selector_kind = 'active'
UNION ALL SELECT cba.file_id, 'work', w.id::text, cba.reference_path, cb.id::text, cb.kind::text, COALESCE(NULLIF(wt.title, ''), NULLIF(w.slug, ''), w.id::text) FROM content_block_attachment cba JOIN content_block cb ON cb.id = cba.block_id JOIN work w ON w.content_document_id = cb.document_id LEFT JOIN work_translation wt ON wt.entity_id = w.id AND wt.locale = w.source_locale WHERE cba.selector_kind = 'active'
UNION ALL SELECT cba.file_id, 'program_event', pe.id::text, cba.reference_path, cb.id::text, cb.kind::text, COALESCE(NULLIF(pe.title, ''), NULLIF(pe.slug, ''), pe.id::text) FROM content_block_attachment cba JOIN content_block cb ON cb.id = cba.block_id JOIN program_event pe ON pe.content_document_id = cb.document_id WHERE cba.selector_kind = 'active'
UNION ALL SELECT file_id, 'program_event', event_id::text, role::text, NULL::text, NULL::text, NULL::text FROM program_event_media
UNION ALL SELECT audio_original_file_id, 'track', id::text, 'audio_original', NULL::text, NULL::text, title::text FROM track WHERE audio_original_file_id IS NOT NULL
UNION ALL SELECT logo_light_file_id, 'client', id::text, 'logo_light', NULL::text, NULL::text, name::text FROM client WHERE logo_light_file_id IS NOT NULL
UNION ALL SELECT logo_dark_file_id, 'client', id::text, 'logo_dark', NULL::text, NULL::text, name::text FROM client WHERE logo_dark_file_id IS NOT NULL
UNION ALL SELECT logo_light_file_id, 'label', id::text, 'logo_light', NULL::text, NULL::text, NULL::text FROM label WHERE logo_light_file_id IS NOT NULL
UNION ALL SELECT logo_dark_file_id, 'label', id::text, 'logo_dark', NULL::text, NULL::text, NULL::text FROM label WHERE logo_dark_file_id IS NOT NULL
UNION ALL SELECT s.featured_image_file_id, 'series', s.id::text, 'featured_image', NULL::text, NULL::text, COALESCE(NULLIF(st.title, ''), NULLIF(s.slug, ''), s.id::text) FROM series s LEFT JOIN series_translation st ON st.entity_id = s.id AND st.locale = s.source_locale WHERE s.featured_image_file_id IS NOT NULL
UNION ALL SELECT p.featured_image_file_id, 'post', p.id::text, 'featured_image', NULL::text, NULL::text, COALESCE(NULLIF(pt.title, ''), NULLIF(p.slug, ''), p.id::text) FROM post p LEFT JOIN post_translation pt ON pt.entity_id = p.id AND pt.locale = p.source_locale WHERE p.featured_image_file_id IS NOT NULL
UNION ALL SELECT p.featured_image_file_id, 'page', p.id::text, 'featured_image', NULL::text, NULL::text, COALESCE(NULLIF(pt.title, ''), NULLIF(p.slug, ''), p.id::text) FROM page p LEFT JOIN page_translation pt ON pt.entity_id = p.id AND pt.locale = p.source_locale WHERE p.featured_image_file_id IS NOT NULL
UNION ALL SELECT w.featured_image_file_id, 'work', w.id::text, 'featured_image', NULL::text, NULL::text, COALESCE(NULLIF(wt.title, ''), NULLIF(w.slug, ''), w.id::text) FROM work w LEFT JOIN work_translation wt ON wt.entity_id = w.id AND wt.locale = w.source_locale WHERE w.featured_image_file_id IS NOT NULL
UNION ALL SELECT featured_image_file_id, 'form', id::text, 'featured_image', NULL::text, NULL::text, NULL::text FROM form WHERE featured_image_file_id IS NOT NULL
UNION ALL SELECT image_file_id, 'map_place', id::text, 'image', NULL::text, NULL::text, name::text FROM map_place WHERE image_file_id IS NOT NULL
UNION ALL SELECT poster_file_id, 'program_event', id::text, 'series_poster', NULL::text, NULL::text, title::text FROM program_event_series WHERE poster_file_id IS NOT NULL
UNION ALL SELECT logo_light_file_id, 'site_settings', id::text, 'logo_light', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_light_file_id IS NOT NULL
UNION ALL SELECT logo_dark_file_id, 'site_settings', id::text, 'logo_dark', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_dark_file_id IS NOT NULL
UNION ALL SELECT logo_email_file_id, 'site_settings', id::text, 'logo_email', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_email_file_id IS NOT NULL
UNION ALL SELECT favicon_file_id, 'site_settings', id::text, 'favicon', NULL::text, NULL::text, NULL::text FROM site_settings WHERE favicon_file_id IS NOT NULL
UNION ALL SELECT site_og_background_file_id, 'site_settings', id::text, 'site_og_background', NULL::text, NULL::text, NULL::text FROM site_settings WHERE site_og_background_file_id IS NOT NULL
UNION ALL SELECT privacy_og_background_file_id, 'site_settings', id::text, 'privacy_og_background', NULL::text, NULL::text, NULL::text FROM site_settings WHERE privacy_og_background_file_id IS NOT NULL
UNION ALL SELECT terms_og_background_file_id, 'site_settings', id::text, 'terms_og_background', NULL::text, NULL::text, NULL::text FROM site_settings WHERE terms_og_background_file_id IS NOT NULL
UNION ALL SELECT file_id, 'site_settings', site_setting_id::text, 'loader', NULL::text, NULL::text, NULL::text FROM site_setting_loader_file
UNION ALL SELECT output_file_id, CASE entity_type WHEN 'TRANSCODE_ENTITY_TYPE_POST' THEN 'post' WHEN 'TRANSCODE_ENTITY_TYPE_PAGE' THEN 'page' WHEN 'TRANSCODE_ENTITY_TYPE_WORK' THEN 'work' ELSE 'unspecified' END, COALESCE(entity_id::text, id::text), 'mesh_optimization_output', NULL::text, NULL::text, NULL::text FROM mesh_optimization_candidate WHERE status IN ('pending', 'processing', 'ready')
`

func lockFileFolderHierarchy(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	return tx.Exec("SELECT pg_advisory_xact_lock(hashtextextended('public.file_folder.hierarchy', 0))").Error
}

func normalizeFileManagerUUID(value, field string, optional bool) (*string, error) {
	value = strings.TrimSpace(value)
	if value == "" && optional {
		return nil, nil
	}
	parsed, err := uuid.Parse(value)
	if err != nil {
		return nil, errs.InvalidArgument(field, "must be a UUID")
	}
	normalized := parsed.String()
	return &normalized, nil
}

func normalizeFileManagerName(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 255 {
		return "", errs.InvalidArgument(field, "must contain 1 to 255 characters")
	}
	if value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
		return "", errs.InvalidArgument(field, "must not be '.', '..', or contain a path separator")
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

func decodeFileManagerPageToken(token *string) (int, error) {
	if token == nil || strings.TrimSpace(*token) == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(*token))
	if err != nil {
		return 0, errs.InvalidArgument("page_token", "is invalid")
	}
	parts := strings.Split(string(decoded), ":")
	if len(parts) != 2 || parts[0] != "v1" {
		return 0, errs.InvalidArgument("page_token", "is invalid")
	}
	offset, err := strconv.Atoi(parts[1])
	if err != nil || offset < 0 {
		return 0, errs.InvalidArgument("page_token", "is invalid")
	}
	return offset, nil
}

func encodeFileManagerPageToken(offset int) *string {
	value := base64.RawURLEncoding.EncodeToString([]byte("v1:" + strconv.Itoa(offset)))
	return &value
}

func (s *FileService) requireFileManagerFolder(ctx context.Context, folderID *string) error {
	if folderID == nil {
		return nil
	}
	var count int64
	if err := s.db.WithContext(ctx).Table("file_folder").Where("id = ?", *folderID).Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count == 0 {
		return errs.NotFound("folder", *folderID)
	}
	return nil
}

func fileManagerSortClause(field managev1.FileManagerSortField, order commonv1.SortOrder, prefix string) string {
	direction := "ASC"
	if order == commonv1.SortOrder_SORT_ORDER_DESC {
		direction = "DESC"
	}
	column := "LOWER(" + prefix + "name)"
	switch field {
	case managev1.FileManagerSortField_FILE_MANAGER_SORT_FIELD_CREATED_AT:
		column = prefix + "created_at"
	case managev1.FileManagerSortField_FILE_MANAGER_SORT_FIELD_UPDATED_AT:
		column = prefix + "updated_at"
	case managev1.FileManagerSortField_FILE_MANAGER_SORT_FIELD_FILE_SIZE:
		column = "COALESCE(" + prefix + "file_size, 0)"
	}
	return "CASE WHEN " + prefix + "item_type = 'folder' THEN 0 ELSE 1 END ASC, " + column + " " + direction + ", " + prefix + "id ASC"
}

func fileManagerFolderPath(raw []byte) ([]*managev1.FileManagerPathSegment, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var decoded []fileManagerPathSegmentJSON
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	path := make([]*managev1.FileManagerPathSegment, 0, len(decoded))
	for _, segment := range decoded {
		path = append(path, &managev1.FileManagerPathSegment{Id: segment.ID, Name: segment.Name})
	}
	return path, nil
}

func (s *FileService) ListFileManagerItems(
	ctx context.Context,
	req *connect.Request[managev1.ListFileManagerItemsRequest],
) (*connect.Response[managev1.ListFileManagerItemsResponse], error) {
	return s.listFileManagerItems(ctx, req, true)
}

func (s *FileService) listFileManagerItems(
	ctx context.Context,
	req *connect.Request[managev1.ListFileManagerItemsRequest],
	allowChangedRetry bool,
) (*connect.Response[managev1.ListFileManagerItemsResponse], error) {
	if _, err := requireFileDownloadAuthor(ctx, s.spiceDB); err != nil {
		return nil, err
	}
	folderID, err := normalizeFileManagerUUID(req.Msg.GetFolderId(), "folder_id", true)
	if err != nil {
		return nil, err
	}
	uploaderID, err := normalizeFileManagerUUID(req.Msg.GetUploadedByMemberId(), "uploaded_by_member_id", true)
	if err != nil {
		return nil, err
	}
	offset, err := decodeFileManagerPageToken(req.Msg.PageToken)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(req.Msg.GetQuery())
	mimePrefix := strings.TrimSpace(strings.ToLower(req.Msg.GetMimeTypePrefix()))
	includeFolders := mimePrefix == "" && uploaderID == nil
	if query == "" || folderID != nil {
		if err := s.requireFileManagerFolder(ctx, folderID); err != nil {
			return nil, err
		}
	}
	pageSizeMaximum := fileManagerMaxDirectoryPageSize
	if query != "" {
		pageSizeMaximum = fileManagerMaxSearchPageSize
	}
	pageSize := fileManagerPageSize(req.Msg.PageSize, pageSizeMaximum)
	parent := ""
	if folderID != nil {
		parent = *folderID
	}
	uploader := ""
	if uploaderID != nil {
		uploader = *uploaderID
	}

	const directoryCatalogSQL = `WITH catalog AS (
		SELECT 'folder'::text AS item_type, id, parent_id, name, NULL::text AS extension,
		       NULL::text AS mime_type, NULL::bigint AS file_size, NULL::integer AS duration_seconds,
		       created_by_member_id AS member_id, created_at, updated_at
		  FROM file_folder
		 WHERE ? AND ((? = '' AND parent_id IS NULL) OR parent_id::text = ?)
		UNION ALL
		SELECT 'file'::text, id, folder_id, file_name, extension, mime_type, file_size,
		       duration_seconds, uploaded_by_member_id, created_at, updated_at
		  FROM file
		 WHERE delete_requested_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM mesh_optimization_candidate moc WHERE moc.output_file_id = file.id)
		   AND ((? = '' AND folder_id IS NULL) OR folder_id::text = ?)
		   AND (? = '' OR mime_type ILIKE ? || '%%')
		   AND (? = '' OR uploaded_by_member_id::text = ?)
	)
	SELECT *, COUNT(*) OVER() AS total, '[]'::jsonb AS folder_path
	  FROM catalog ORDER BY %s LIMIT ? OFFSET ?`

	const searchCatalogSQL = `WITH RECURSIVE search_scope AS (
		SELECT id
		  FROM file_folder
		 WHERE id::text = ?
		UNION ALL
		SELECT child.id
		  FROM file_folder child
		  JOIN search_scope parent ON child.parent_id = parent.id
	), catalog AS (
		SELECT 'folder'::text AS item_type, id, parent_id, name, NULL::text AS extension,
		       NULL::text AS mime_type, NULL::bigint AS file_size, NULL::integer AS duration_seconds,
		       created_by_member_id AS member_id, created_at, updated_at
		  FROM file_folder
		 WHERE ? AND name ILIKE '%%' || ? || '%%'
		   AND (? = '' OR (id IN (SELECT id FROM search_scope) AND id::text <> ?))
		UNION ALL
		SELECT 'file'::text, id, folder_id, file_name, extension, mime_type, file_size,
		       duration_seconds, uploaded_by_member_id, created_at, updated_at
		  FROM file
		 WHERE delete_requested_at IS NULL
		   AND NOT EXISTS (SELECT 1 FROM mesh_optimization_candidate moc WHERE moc.output_file_id = file.id)
		   AND (file_name || '.' || extension) ILIKE '%%' || ? || '%%'
		   AND (? = '' OR folder_id IN (SELECT id FROM search_scope))
		   AND (? = '' OR mime_type ILIKE ? || '%%')
		   AND (? = '' OR uploaded_by_member_id::text = ?)
	), paged AS (
		SELECT *, COUNT(*) OVER() AS total FROM catalog ORDER BY %s LIMIT ? OFFSET ?
	), path_walk AS (
		SELECT paged.item_type, paged.id AS item_id, folder.id AS folder_id, folder.parent_id,
		       folder.name, 1 AS depth
		  FROM paged
		  JOIN file_folder folder
		    ON folder.id = CASE WHEN paged.item_type = 'folder' THEN paged.id ELSE paged.parent_id END
		UNION ALL
		SELECT path_walk.item_type, path_walk.item_id, parent.id, parent.parent_id,
		       parent.name, path_walk.depth + 1
		  FROM path_walk
		  JOIN file_folder parent ON parent.id = path_walk.parent_id
	), paths AS (
		SELECT item_type, item_id,
		       jsonb_agg(jsonb_build_object('id', folder_id::text, 'name', name) ORDER BY depth DESC) AS folder_path
		  FROM path_walk
		 GROUP BY item_type, item_id
	)
	SELECT paged.*, COALESCE(paths.folder_path, '[]'::jsonb) AS folder_path
	  FROM paged
	  LEFT JOIN paths ON paths.item_type = paged.item_type AND paths.item_id = paged.id
	 ORDER BY %s`

	sortClause := fileManagerSortClause(req.Msg.SortField, req.Msg.SortOrder, "")
	var rows []fileManagerCatalogRow
	var queryResult *gorm.DB
	if query == "" {
		sql := fmt.Sprintf(directoryCatalogSQL, sortClause)
		queryResult = s.db.WithContext(ctx).Raw(sql,
			includeFolders, parent, parent,
			parent, parent, mimePrefix, mimePrefix, uploader, uploader,
			pageSize, offset,
		).Scan(&rows)
	} else {
		sql := fmt.Sprintf(searchCatalogSQL, sortClause, fileManagerSortClause(req.Msg.SortField, req.Msg.SortOrder, "paged."))
		queryResult = s.db.WithContext(ctx).Raw(sql,
			parent,
			includeFolders, query, parent, parent,
			query, parent,
			mimePrefix, mimePrefix, uploader, uploader,
			pageSize, offset,
		).Scan(&rows)
	}
	if err := queryResult.Error; err != nil {
		return nil, errs.Internal(err)
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
	memberSummaries, err := requireMemberSummaries(s.memberSummaries)
	if err != nil {
		return nil, err
	}
	members, err := memberSummaries.Load(ctx, normalizedSortedFileIDs(memberIDs))
	if err != nil {
		return nil, errs.Internal(err)
	}
	usageCounts, err := s.loadVisibleFileUsageCounts(ctx, fileIDs)
	if err != nil {
		return nil, errs.Internal(err)
	}
	fencedFiles, _, changed, err := s.finalizeFileManagerDeliveries(ctx, rows, members, usageCounts, nil)
	if err != nil {
		return nil, errs.Internal(err)
	}
	if changed {
		if allowChangedRetry {
			return s.listFileManagerItems(ctx, req, false)
		}
		return nil, errs.DependencyUnavailable("file catalog")
	}

	items := make([]*managev1.FileManagerItem, 0, len(rows))
	for _, row := range rows {
		folderPath, pathErr := fileManagerFolderPath(row.FolderPath)
		if pathErr != nil {
			return nil, errs.Internal(pathErr)
		}
		if row.ItemType == "folder" {
			folder := &managev1.FileFolder{
				Id: row.ID, ParentId: row.ParentID, Name: row.Name,
				CreatedAt: timestamppb.New(row.CreatedAt), UpdatedAt: timestamppb.New(row.UpdatedAt),
			}
			if row.MemberID != nil {
				folder.CreatedByMember = members[*row.MemberID]
			}
			items = append(items, &managev1.FileManagerItem{
				Type: managev1.FileManagerItemType_FILE_MANAGER_ITEM_TYPE_FOLDER,
				Item: &managev1.FileManagerItem_Folder{Folder: folder}, FolderPath: folderPath,
			})
			continue
		}
		file := fencedFiles[row.ID]
		if file == nil {
			return nil, errs.DependencyUnavailable("file catalog")
		}
		items = append(items, &managev1.FileManagerItem{
			Type: managev1.FileManagerItemType_FILE_MANAGER_ITEM_TYPE_FILE,
			Item: &managev1.FileManagerItem_File{File: file}, FolderPath: folderPath,
		})
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].Total
	}
	var next *string
	if int64(offset+len(rows)) < total {
		next = encodeFileManagerPageToken(offset + len(rows))
	}
	return connect.NewResponse(&managev1.ListFileManagerItemsResponse{Items: items, NextPageToken: next, Total: total}), nil
}
