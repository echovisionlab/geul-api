package filemediapostgres

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
)

func (s *store) DeliveryFileOwnership(ctx context.Context, ids []string) ([]domain.File, error) {
	var rows []model.File
	if err := s.tx.WithContext(ctx).Select("id", "uploaded_by_member_id", "delete_requested_at").Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.File, len(rows))
	for i, row := range rows {
		result[i] = domain.File(row)
	}
	return result, nil
}
func (s *store) DeliveryIngestBindings(ctx context.Context, ids []string) ([]domain.IngestBinding, error) {
	var rows []model.FileIngestBinding
	if err := s.tx.WithContext(ctx).Where("file_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]domain.IngestBinding, len(rows))
	for i, row := range rows {
		result[i] = domain.IngestBinding(row)
	}
	return result, nil
}

type currentContentFileDeliveryOwner struct {
	table               string
	resourceType        string
	hasFeaturedImageRef bool
}

var currentContentFileDeliveryOwners = []currentContentFileDeliveryOwner{
	{table: "post", resourceType: "post", hasFeaturedImageRef: true},
	{table: "page", resourceType: "page", hasFeaturedImageRef: true},
	{table: "work", resourceType: "work", hasFeaturedImageRef: true},
	{table: "program_event", resourceType: "program_event"},
}

func (s *store) CurrentDeliveryUsages(ctx context.Context, fileIDs []string) ([]domain.CurrentDeliveryUsage, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	var rows []domain.CurrentDeliveryUsage
	queries := make([]string, 0, len(currentContentFileDeliveryOwners)*2)
	arguments := make([]any, 0, len(currentContentFileDeliveryOwners)*2)
	for _, owner := range currentContentFileDeliveryOwners {
		ownerFingerprint := `COALESCE(CAST(owner.content_document_id AS TEXT), '') || ':'`
		if owner.resourceType == "post" || owner.resourceType == "program_event" {
			ownerFingerprint = `COALESCE(CAST(owner.content_document_id AS TEXT), '') || ':' || COALESCE(CAST(owner.status AS TEXT), '')`
		}
		attachmentQuery := fmt.Sprintf(`SELECT attachment.file_id, '%s' AS resource_type, owner.id AS resource_id,
			'content_attachment' AS kind, attachment.block_id AS relation_id, attachment.reference_path,
			%s AS owner_fingerprint
			FROM content_block_attachment AS attachment
			JOIN content_block AS block ON block.id = attachment.block_id
			JOIN %s AS owner ON owner.content_document_id = block.document_id
			WHERE attachment.selector_kind = 'active' AND attachment.file_id IN ?`, owner.resourceType, ownerFingerprint, owner.table)
		queries = append(queries, attachmentQuery)
		arguments = append(arguments, fileIDs)
		if owner.hasFeaturedImageRef {
			featuredImageQuery := fmt.Sprintf(
				`SELECT owner.featured_image_file_id AS file_id, '%s' AS resource_type, owner.id AS resource_id,
				'featured_image' AS kind, owner.id AS relation_id, 'featured_image' AS reference_path,
				%s AS owner_fingerprint
				FROM %s AS owner
				WHERE owner.featured_image_file_id IN ?`,
				owner.resourceType,
				ownerFingerprint,
				owner.table,
			)
			queries = append(queries, featuredImageQuery)
			arguments = append(arguments, fileIDs)
		}
	}
	queries = append(queries,
		`SELECT relation.file_id, 'artist' AS resource_type, relation.artist_id AS resource_id,
			'artist_file' AS kind, relation.artist_id AS relation_id, '' AS reference_path, '' AS owner_fingerprint
		 FROM artist_file AS relation WHERE relation.file_id IN ?`,
		`SELECT relation.file_id, 'release' AS resource_type, relation.release_id AS resource_id,
			'release_file' AS kind, relation.release_id AS relation_id, '' AS reference_path, '' AS owner_fingerprint
		 FROM release_file AS relation WHERE relation.file_id IN ?`,
		`SELECT relation.file_id, 'program_event' AS resource_type, relation.event_id AS resource_id,
			'program_event_media' AS kind, relation.id AS relation_id, relation.role AS reference_path,
			COALESCE(CAST(owner.content_document_id AS TEXT), '') || ':' || COALESCE(CAST(owner.status AS TEXT), '') AS owner_fingerprint
		 FROM program_event_media AS relation JOIN program_event AS owner ON owner.id = relation.event_id WHERE relation.file_id IN ?`,
		`SELECT track.audio_original_file_id AS file_id, 'release' AS resource_type, track.release_id AS resource_id,
			'track' AS kind, track.id AS relation_id, 'audio_original' AS reference_path, '' AS owner_fingerprint
		 FROM track WHERE track.audio_original_file_id IN ?`,
		`SELECT label.logo_light_file_id AS file_id, 'label' AS resource_type, label.id AS resource_id,
			'label_logo' AS kind, label.id AS relation_id, 'logo_light' AS reference_path, '' AS owner_fingerprint
		 FROM label WHERE label.logo_light_file_id IN ?`,
		`SELECT label.logo_dark_file_id AS file_id, 'label' AS resource_type, label.id AS resource_id,
			'label_logo' AS kind, label.id AS relation_id, 'logo_dark' AS reference_path, '' AS owner_fingerprint
		 FROM label WHERE label.logo_dark_file_id IN ?`,
		`SELECT series.featured_image_file_id AS file_id, 'series' AS resource_type, series.id AS resource_id,
			'series_featured_image' AS kind, series.id AS relation_id, 'featured_image' AS reference_path, '' AS owner_fingerprint
		 FROM series WHERE series.featured_image_file_id IN ?`,
		`SELECT form.featured_image_file_id AS file_id, 'form' AS resource_type, form.id AS resource_id,
			'form_featured_image' AS kind, form.id AS relation_id, 'featured_image' AS reference_path, '' AS owner_fingerprint
		 FROM form WHERE form.featured_image_file_id IN ?`)
	for range 8 {
		arguments = append(arguments, fileIDs)
	}
	query := `SELECT DISTINCT current_content_files.file_id,
			current_content_files.resource_type,
			current_content_files.resource_id,
			current_content_files.kind,
			current_content_files.relation_id,
			current_content_files.reference_path,
			current_content_files.owner_fingerprint
		FROM (` + strings.Join(queries, ` UNION ALL `) + `) AS current_content_files`
	if err := s.tx.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("load current Content file usage: %w", err)
	}
	return rows, nil
}
