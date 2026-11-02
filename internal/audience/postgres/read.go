package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/model"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
)

var segmentSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":         "name",
		"created_at":   "created_at",
		"segment_type": "segment_type",
	},
	DefaultSort: "name ASC, id ASC",
}

var segmentFilterConfig = &queryutil.FilterConfig{
	Fields: map[string]queryutil.FieldDef{
		"search": {
			Type:          queryutil.TypeText,
			AllowedOps:    queryutil.SearchOps,
			SearchColumns: []string{"name"},
		},
	},
}

var authenticatedAccessSegmentTypes = []model.AudienceSegmentType{
	model.AudienceSegmentMemberTags,
	model.AudienceSegmentMembersByFilter,
}

func (s store) ListSegments(ctx context.Context, includeArchived bool, page audience.Page) ([]audience.Segment, int64, error) {
	list := s.tx.WithContext(ctx).Model(&model.AudienceSegment{})
	if !includeArchived {
		list = list.Where("archived_at IS NULL")
	}
	rows, total, err := listPage(list, page)
	if err != nil {
		return nil, 0, err
	}
	pointers := make([]*model.AudienceSegment, 0, len(rows))
	for i := range rows {
		pointers = append(pointers, &rows[i])
	}
	if err := LoadSegmentConfigs(ctx, s.tx, pointers); err != nil {
		return nil, 0, err
	}
	return segmentsOf(rows), total, nil
}

func (s store) ListAuthenticatedAccessSegments(ctx context.Context, page audience.Page) ([]audience.Segment, int64, error) {
	list := s.tx.WithContext(ctx).
		Model(&model.AudienceSegment{}).
		Where("segment_type IN ?", authenticatedAccessSegmentTypes).
		Where("archived_at IS NULL")
	rows, total, err := listPage(list, page)
	if err != nil {
		return nil, 0, err
	}
	return segmentsOf(rows), total, nil
}

// listPage counts what the page's filters select and reads the page in its
// sort order.
func listPage(list *gorm.DB, page audience.Page) ([]model.AudienceSegment, int64, error) {
	list, err := segmentFilterConfig.ApplyFilters(list, page.Filters)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	if err := list.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	list, err = segmentSortConfig.ApplySort(list, page.Sorts)
	if err != nil {
		return nil, 0, err
	}
	var rows []model.AudienceSegment
	if err := list.Limit(int(page.Limit)).Offset(int(page.Offset)).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func segmentsOf(rows []model.AudienceSegment) []audience.Segment {
	segments := make([]audience.Segment, len(rows))
	for i, row := range rows {
		segments[i] = SegmentOf(row)
	}
	return segments
}

func (s store) SegmentReferences(ctx context.Context, ids []string) (map[string]audience.References, error) {
	references := make(map[string]audience.References, len(ids))
	if len(ids) == 0 {
		return references, nil
	}
	campaigns, runs, err := s.campaigns.CountSegmentUsage(ctx, s.tx, ids)
	if err != nil {
		return nil, err
	}
	for id, count := range campaigns {
		counted := references[id]
		counted.Campaigns = count
		references[id] = counted
	}
	for id, count := range runs {
		counted := references[id]
		counted.DeliveryRuns = count
		references[id] = counted
	}
	var rows []struct {
		ID    string `gorm:"column:id"`
		Count int64  `gorm:"column:count"`
	}
	if err := s.tx.WithContext(ctx).Raw(`
		SELECT audience_segment_id AS id, COUNT(*) AS count
		FROM (
			SELECT audience_segment_id
			FROM content_block_attachment_download_audience_segment
			WHERE audience_segment_id IN ?
			UNION ALL
			SELECT audience_segment_id
			FROM track_download_audience_segment
			WHERE audience_segment_id IN ?
		) AS policy_reference
		GROUP BY audience_segment_id
	`, ids, ids).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		counted := references[row.ID]
		counted.DownloadPolicies = int32(row.Count)
		references[row.ID] = counted
	}
	return references, nil
}

// ValidateAuthenticatedAccessSegmentIDs locks the segments a download policy
// names, in the order relation-policy mutations lock them, and validates that
// each one exists and can grant authenticated access. File's policy
// mutations call it in their own transaction, before they lock the exact
// attachment or Track.
func ValidateAuthenticatedAccessSegmentIDs(ctx context.Context, db *gorm.DB, segmentIDs []string) error {
	if len(segmentIDs) == 0 {
		return nil
	}
	var rows []model.AudienceSegment
	if err := db.WithContext(ctx).
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where("id IN ?", segmentIDs).
		Order("id ASC").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("lock authenticated access segments: %w", err)
	}
	return audience.RequireAuthenticatedAccessSegments(segmentIDs, segmentsOf(rows))
}
