package filemedia

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/structured"
)

func normalizedSortedFileIDs(fileIDs []string) []string {
	seen := make(map[string]struct{}, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID != "" {
			seen[fileID] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for fileID := range seen {
		ids = append(ids, fileID)
	}
	sort.Strings(ids)
	return ids
}

type fileAttachmentReferenceSpec struct {
	name      string
	table     string
	predicate string
	active    string
	args      structured.Values
}

// This is the single registry of product references that keep an original
// file alive. Provenance and transport tables (upload_session,
// file_ingest_binding, public_asset, file_derivative and download policy rows)
// are intentionally not attachment authorities.
var fileAttachmentReferenceRegistry = []fileAttachmentReferenceSpec{
	{name: "artist_file", table: "artist_file", predicate: "ref.file_id = %s.id"},
	{name: "release_file", table: "release_file", predicate: "ref.file_id = %s.id"},
	{name: "content_block_attachment", table: "content_block_attachment", predicate: "ref.selector_kind = 'active' AND ref.file_id = %s.id"},
	{name: "program_event_media", table: "program_event_media", predicate: "ref.file_id = %s.id"},
	{name: "track.audio_original_file_id", table: "track", predicate: "ref.audio_original_file_id = %s.id"},
	{name: "client.logo_file_ids", table: "client", predicate: "(ref.logo_light_file_id = %s.id OR ref.logo_dark_file_id = %s.id)"},
	{name: "label.logo_file_ids", table: "label", predicate: "(ref.logo_light_file_id = %s.id OR ref.logo_dark_file_id = %s.id)"},
	{name: "series.featured_image_file_id", table: "series", predicate: "ref.featured_image_file_id = %s.id"},
	{name: "post.featured_image_file_id", table: "post", predicate: "ref.featured_image_file_id = %s.id"},
	{name: "page.featured_image_file_id", table: "page", predicate: "ref.featured_image_file_id = %s.id"},
	{name: "work.featured_image_file_id", table: "work", predicate: "ref.featured_image_file_id = %s.id"},
	{name: "form.featured_image_file_id", table: "form", predicate: "ref.featured_image_file_id = %s.id"},
	{name: "map_place.image_file_id", table: "map_place", predicate: "ref.image_file_id = %s.id"},
	{name: "program_event_series.poster_file_id", table: "program_event_series", predicate: "ref.poster_file_id = %s.id"},
	{name: "site_settings.file_ids", table: "site_settings", predicate: "(ref.logo_light_file_id = %s.id OR ref.logo_dark_file_id = %s.id OR ref.logo_email_file_id = %s.id OR ref.favicon_file_id = %s.id OR ref.site_og_background_file_id = %s.id OR ref.privacy_og_background_file_id = %s.id OR ref.terms_og_background_file_id = %s.id)"},
	{name: "site_setting_loader_file", table: "site_setting_loader_file", predicate: "ref.file_id = %s.id"},
	{
		name:      "mesh_optimization_candidate.output_file_id",
		table:     "mesh_optimization_candidate",
		predicate: "ref.output_file_id = %s.id",
		active:    "ref.status IN ?",
		args:      structured.Values{MeshOptimizationOutputProtectionStatuses()},
	},
}

func (spec fileAttachmentReferenceSpec) predicateForAlias(fileAlias string) string {
	placeholderCount := strings.Count(spec.predicate, "%s")
	values := make(structured.Values, placeholderCount)
	for index := range values {
		values[index] = fileAlias
	}
	predicate := fmt.Sprintf(spec.predicate, values...)
	if spec.active != "" {
		predicate += " AND " + spec.active
	}
	return predicate
}

// ApplyNoActiveFileReferences adds the registry's NOT EXISTS predicates to a
// query whose file table is available under fileAlias.
func ApplyNoActiveFileReferences(query *gorm.DB, fileAlias string) *gorm.DB {
	for _, spec := range fileAttachmentReferenceRegistry {
		query = query.Where(
			"NOT EXISTS (SELECT 1 FROM "+spec.table+" AS ref WHERE "+spec.predicateForAlias(fileAlias)+")",
			spec.args...,
		)
	}
	return query
}

// ActiveFileReferenceNames is used while holding the file row lock. Returning
// names makes defensive refusals observable without inventing persisted state.
func ActiveFileReferenceNames(ctx context.Context, db *gorm.DB, fileID string) ([]string, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return nil, nil
	}
	result := make([]string, 0)
	for _, spec := range fileAttachmentReferenceRegistry {
		query := db.WithContext(ctx).Table(spec.table+" AS ref").Where(
			strings.ReplaceAll(spec.predicateForAlias("file"), "file.id", "?"),
			append(repeatStructuredValue(fileID, strings.Count(spec.predicate, "%s")), spec.args...)...,
		)
		var count int64
		if err := query.Limit(1).Count(&count).Error; err != nil {
			return nil, fmt.Errorf("check %s reference: %w", spec.name, err)
		}
		if count > 0 {
			result = append(result, spec.name)
		}
	}
	return result, nil
}

// ActiveFileReferenceIDs performs the deletion-reference preflight for a
// bounded file set in one query. Callers must hold the corresponding file row
// locks before using the result to mutate deletion state.
func ActiveFileReferenceIDs(ctx context.Context, db *gorm.DB, fileIDs []string) (map[string]struct{}, error) {
	ids := normalizedSortedFileIDs(fileIDs)
	result := make(map[string]struct{})
	if len(ids) == 0 {
		return result, nil
	}
	query := db.WithContext(ctx).Table("file AS file").Select("file.id").Where("file.id IN ?", ids)
	query = ApplyNoActiveFileReferences(query, "file")
	var unreferenced []string
	if err := query.Order("file.id ASC").Pluck("file.id", &unreferenced).Error; err != nil {
		return nil, err
	}
	unreferencedSet := make(map[string]struct{}, len(unreferenced))
	for _, fileID := range unreferenced {
		unreferencedSet[fileID] = struct{}{}
	}
	for _, fileID := range ids {
		if _, ok := unreferencedSet[fileID]; !ok {
			result[fileID] = struct{}{}
		}
	}
	return result, nil
}

func repeatStructuredValue(value structured.Value, count int) structured.Values {
	values := make(structured.Values, count)
	for index := range values {
		values[index] = value
	}
	return values
}
