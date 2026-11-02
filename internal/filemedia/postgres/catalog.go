package filemediapostgres

import (
	"context"
	"fmt"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"gorm.io/gorm"
)

func catalogSortClause(field domain.CatalogSort, descending bool, prefix string) string {
	direction := "ASC"
	if descending {
		direction = "DESC"
	}
	column := "LOWER(" + prefix + "name)"
	switch field {
	case domain.CatalogSortCreated:
		column = prefix + "created_at"
	case domain.CatalogSortUpdated:
		column = prefix + "updated_at"
	case domain.CatalogSortSize:
		column = "COALESCE(" + prefix + "file_size, 0)"
	}
	return "CASE WHEN " + prefix + "item_type = 'folder' THEN 0 ELSE 1 END ASC, " + column + " " + direction + ", " + prefix + "id ASC"
}
func (s *store) Catalog(ctx context.Context, filter domain.CatalogQuery) ([]domain.CatalogRow, error) {
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

	parent, uploader, mimePrefix := filter.Parent, filter.Uploader, filter.MimePrefix
	sortClause := catalogSortClause(filter.SortField, filter.Descending, "")
	var rows []domain.CatalogRow
	var queryResult *gorm.DB
	if filter.Query == "" {
		sql := fmt.Sprintf(directoryCatalogSQL, sortClause)
		queryResult = s.tx.WithContext(ctx).Raw(sql,
			filter.IncludeFolders, parent, parent,
			parent, parent, mimePrefix, mimePrefix, uploader, uploader,
			filter.PageSize, filter.Offset,
		).Scan(&rows)
	} else {
		sql := fmt.Sprintf(searchCatalogSQL, sortClause, catalogSortClause(filter.SortField, filter.Descending, "paged."))
		queryResult = s.tx.WithContext(ctx).Raw(sql,
			parent,
			filter.IncludeFolders, filter.Query, parent, parent,
			filter.Query, parent,
			mimePrefix, mimePrefix, uploader, uploader,
			filter.PageSize, filter.Offset,
		).Scan(&rows)
	}
	if err := queryResult.Error; err != nil {
		return nil, err
	}
	return rows, nil
}
func (s *store) FileCatalog(ctx context.Context, id string) (domain.CatalogRow, error) {
	var row domain.CatalogRow
	err := s.tx.WithContext(ctx).Raw(`SELECT 'file' AS item_type, id, folder_id AS parent_id, file_name AS name, extension, mime_type, file_size, duration_seconds, uploaded_by_member_id AS member_id, created_at, updated_at, 1 AS total FROM file WHERE id = ? AND delete_requested_at IS NULL`, id).Scan(&row).Error
	return row, err
}
func (s *store) FileCatalogs(ctx context.Context, ids []string) ([]domain.CatalogRow, error) {
	var rows []domain.CatalogRow
	err := s.tx.WithContext(ctx).Raw(`SELECT 'file' AS item_type, id, folder_id AS parent_id, file_name AS name, extension, mime_type, file_size, duration_seconds, uploaded_by_member_id AS member_id, created_at, updated_at, 1 AS total FROM file WHERE id IN ? AND delete_requested_at IS NULL ORDER BY id`, ids).Scan(&rows).Error
	return rows, err
}
