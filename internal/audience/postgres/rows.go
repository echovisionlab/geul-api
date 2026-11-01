package postgres

import (
	"slices"

	"github.com/echovisionlab/geul-api/internal/audience"
	"github.com/echovisionlab/geul-api/internal/model"
)

// SegmentOf maps a segment row to the domain segment. The creation window
// comes from the row's columns; the filter relations from the row's Config,
// which LoadSegmentConfigs fills.
func SegmentOf(row model.AudienceSegment) audience.Segment {
	return audience.Segment{
		ID:          row.ID,
		Name:        row.Name,
		Description: row.Description,
		Type:        audience.SegmentType(row.SegmentType),
		Config: audience.Config{
			MemberTagIDs:     slices.Clone(row.Config.MemberTagIDs),
			AccountRoles:     slices.Clone(row.Config.AccountRoles),
			CreatedAfter:     row.CreatedAfter,
			CreatedBefore:    row.CreatedBefore,
			ExcludeMemberIDs: slices.Clone(row.Config.ExcludeMemberIDs),
		},
		ArchivedAt: row.ArchivedAt,
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

// SegmentRow maps a domain segment to its row, with Config filled as
// LoadSegmentConfigs fills it.
func SegmentRow(segment audience.Segment) model.AudienceSegment {
	return model.AudienceSegment{
		ID:            segment.ID,
		Name:          segment.Name,
		Description:   segment.Description,
		SegmentType:   model.AudienceSegmentType(segment.Type),
		CreatedAfter:  segment.Config.CreatedAfter,
		CreatedBefore: segment.Config.CreatedBefore,
		Config: model.AudienceSegmentConfig{
			MemberTagIDs:     slices.Clone(segment.Config.MemberTagIDs),
			AccountRoles:     slices.Clone(segment.Config.AccountRoles),
			CreatedAfter:     segment.Config.CreatedAfter,
			CreatedBefore:    segment.Config.CreatedBefore,
			ExcludeMemberIDs: slices.Clone(segment.Config.ExcludeMemberIDs),
		},
		ArchivedAt: segment.ArchivedAt,
		CreatedAt:  segment.CreatedAt,
		UpdatedAt:  segment.UpdatedAt,
	}
}
