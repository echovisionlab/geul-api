package filemediapostgres

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
)

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
// authority remains the AttachmentReferences port; this projection only
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
UNION ALL SELECT logo_full_light_file_id, 'site_settings', id::text, 'logo_full_light', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_full_light_file_id IS NOT NULL
UNION ALL SELECT logo_full_dark_file_id, 'site_settings', id::text, 'logo_full_dark', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_full_dark_file_id IS NOT NULL
UNION ALL SELECT logo_symbol_light_file_id, 'site_settings', id::text, 'logo_symbol_light', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_symbol_light_file_id IS NOT NULL
UNION ALL SELECT logo_symbol_dark_file_id, 'site_settings', id::text, 'logo_symbol_dark', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_symbol_dark_file_id IS NOT NULL
UNION ALL SELECT logo_email_light_file_id, 'site_settings', id::text, 'logo_email_light', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_email_light_file_id IS NOT NULL
UNION ALL SELECT logo_email_dark_file_id, 'site_settings', id::text, 'logo_email_dark', NULL::text, NULL::text, NULL::text FROM site_settings WHERE logo_email_dark_file_id IS NOT NULL
UNION ALL SELECT favicon_file_id, 'site_settings', id::text, 'favicon', NULL::text, NULL::text, NULL::text FROM site_settings WHERE favicon_file_id IS NOT NULL
UNION ALL SELECT site_og_background_file_id, 'site_settings', id::text, 'site_og_background', NULL::text, NULL::text, NULL::text FROM site_settings WHERE site_og_background_file_id IS NOT NULL
UNION ALL SELECT privacy_og_background_file_id, 'site_settings', id::text, 'privacy_og_background', NULL::text, NULL::text, NULL::text FROM site_settings WHERE privacy_og_background_file_id IS NOT NULL
UNION ALL SELECT terms_og_background_file_id, 'site_settings', id::text, 'terms_og_background', NULL::text, NULL::text, NULL::text FROM site_settings WHERE terms_og_background_file_id IS NOT NULL
UNION ALL SELECT file_id, 'site_settings', site_setting_id::text, 'loader', NULL::text, NULL::text, NULL::text FROM site_setting_loader_file
UNION ALL SELECT output_file_id, CASE entity_type WHEN 'TRANSCODE_ENTITY_TYPE_POST' THEN 'post' WHEN 'TRANSCODE_ENTITY_TYPE_PAGE' THEN 'page' WHEN 'TRANSCODE_ENTITY_TYPE_WORK' THEN 'work' ELSE 'unspecified' END, COALESCE(entity_id::text, id::text), 'mesh_optimization_output', NULL::text, NULL::text, NULL::text FROM mesh_optimization_candidate WHERE status IN ('pending', 'processing', 'ready')
`

func (s *store) FileUsages(ctx context.Context, fileIDs []string) ([]domain.FileUsageRow, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []fileUsageRow
	if err := s.tx.WithContext(ctx).Raw(
		"SELECT file_id::text AS file_id, domain, entity_id, reference_path, block_id, block_type, label FROM ("+fileUsageUnionSQL+") AS usage WHERE file_id IN ? ORDER BY file_id, domain, entity_id, reference_path, COALESCE(block_id, '')",
		fileIDs,
	).Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.FileUsageRow, len(rows))
	for i, r := range rows {
		result[i] = domain.FileUsageRow(r)
	}
	return result, nil
}
func (s *store) FileUsageSlugs(ctx context.Context, entityIDs []string) ([]domain.FileUsageSlug, error) {
	if len(entityIDs) == 0 {
		return nil, nil
	}
	var slugs []domain.FileUsageSlug
	if err := s.tx.WithContext(ctx).Raw(`
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
	return slugs, nil
}
func (s *store) TrackReleaseIDs(ctx context.Context, ids []string) (map[string]string, error) {
	var rows []struct{ ID, ReleaseID string }
	if err := s.tx.WithContext(ctx).Table("track").Select("id, release_id").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]string, len(rows))
	for _, r := range rows {
		result[r.ID] = r.ReleaseID
	}
	return result, nil
}
func (s *store) ActiveFileCount(ctx context.Context, id string) (int64, error) {
	var n int64
	err := s.tx.WithContext(ctx).Table("file").Where("id = ? AND delete_requested_at IS NULL", id).Count(&n).Error
	return n, err
}
