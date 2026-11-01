package member

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	queryutil "github.com/echovisionlab/geul-api/internal/query"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Tag is an administrator-defined Member tag. MemberCount is set only by
// ListMemberTags.
type Tag struct {
	ID          string
	Name        string
	CreatedAt   time.Time
	MemberCount int32
}

var memberTagAdminFilterConfig = queryutil.FilterConfig{Fields: map[string]queryutil.FieldDef{
	"search": {
		Type: queryutil.TypeText, AllowedOps: queryutil.SearchOps, SearchColumns: []string{"t.name"},
	},
	"name": {
		Column: "t.name", Type: queryutil.TypeText, AllowedOps: queryutil.TextOps,
	},
	"created_at": {
		Column: "t.created_at", Type: queryutil.TypeDate, AllowedOps: queryutil.DateOps,
	},
}}

var memberTagAdminSortConfig = queryutil.SortConfig{
	AllowedFields: map[string]string{
		"name":       "t.name",
		"user_count": "COUNT(m.member_id)",
		"created_at": "t.created_at",
	},
	DefaultSort: "t.name ASC, t.id ASC",
}

func (s *Service) memberTagIDs(ctx context.Context, memberID string) ([]string, error) {
	values := []string{}
	err := s.db.WithContext(ctx).Model(&model.UserTagMapping{}).Where("member_id = ?", memberID).Order("tag_id").Pluck("tag_id", &values).Error
	return values, err
}

func (s *Service) memberTagIDsBatch(ctx context.Context, memberIDs []string) (map[string][]string, error) {
	result := make(map[string][]string, len(memberIDs))
	if len(memberIDs) == 0 {
		return result, nil
	}
	var rows []model.UserTagMapping
	if err := s.db.WithContext(ctx).Where("member_id IN ?", memberIDs).Order("member_id, tag_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		result[row.MemberID] = append(result[row.MemberID], row.TagID)
	}
	return result, nil
}

// ListMemberTagsQuery pages through Member tags.
type ListMemberTagsQuery struct {
	// Limit defaults to 50 and is capped at 500.
	Limit   int32
	Offset  int32
	Filters []*commonv1.FilterSpec
	Sorts   []*commonv1.SortSpec
}

// TagPage is one page of Member tags.
type TagPage struct {
	Tags       []Tag
	Pagination *commonv1.PaginationResponse
}

// ListMemberTags returns one page of tags with their member counts.
func (s *Service) ListMemberTags(ctx context.Context, query ListMemberTagsQuery) (TagPage, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return TagPage{}, err
	}
	limit, offset := 50, 0
	if query.Limit > 0 {
		limit = int(query.Limit)
	}
	if query.Offset > 0 {
		offset = int(query.Offset)
	}
	if limit > 500 {
		limit = 500
	}
	list := s.db.WithContext(ctx).Table("user_tag AS t")
	var err error
	list, err = memberTagAdminFilterConfig.ApplyFilters(list, query.Filters)
	if err != nil {
		return TagPage{}, err
	}
	var total int64
	if err := list.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return TagPage{}, err
	}
	type row struct {
		model.UserTag
		MemberCount int32 `gorm:"column:member_count"`
	}
	var rows []row
	list = list.Select("t.*, COUNT(m.member_id)::int AS member_count").
		Joins("LEFT JOIN user_tag_mapping AS m ON m.tag_id = t.id").
		Group("t.id")
	list, err = memberTagAdminSortConfig.ApplySort(list, query.Sorts)
	if err != nil {
		return TagPage{}, err
	}
	if len(query.Sorts) != 0 {
		list = list.Order("t.id ASC")
	}
	if err := list.Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return TagPage{}, err
	}
	tags := make([]Tag, len(rows))
	for i, row := range rows {
		tags[i] = Tag{ID: row.ID, Name: row.Name, CreatedAt: row.CreatedAt, MemberCount: row.MemberCount}
	}
	return TagPage{Tags: tags, Pagination: &commonv1.PaginationResponse{Total: int32(min(total, math.MaxInt32)), Limit: int32(limit), Offset: int32(offset), HasMore: int64(offset+len(rows)) < total}}, nil
}

// CreateMemberTagCommand creates a Member tag.
type CreateMemberTagCommand struct {
	Name string
}

// CreateMemberTag creates the tag and returns it.
func (s *Service) CreateMemberTag(ctx context.Context, command CreateMemberTagCommand) (Tag, error) {
	return mutation.Run(ctx, s.mutations, mutation.Spec[Tag]{
		Access: freshAdmin(),
		Change: func(_ context.Context, tx *gorm.DB) (Tag, error) {
			name := strings.TrimSpace(command.Name)
			if name == "" {
				return Tag{}, domainerr.Required("name")
			}
			tag := model.UserTag{Name: name}
			if err := tx.Create(&tag).Error; err != nil {
				return Tag{}, err
			}
			return Tag{ID: tag.ID, Name: tag.Name, CreatedAt: tag.CreatedAt}, nil
		},
		Audit: func(tag Tag) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberTagCreated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, tag.ID, tag.Name)
			})
		},
	})
}

// DeleteMemberTagCommand deletes a Member tag that no audience segment uses.
type DeleteMemberTagCommand struct {
	ID string
}

// DeleteMemberTag deletes the tag and its Member assignments.
func (s *Service) DeleteMemberTag(ctx context.Context, command DeleteMemberTagCommand) error {
	_, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.UserTag]{
		Access: freshAdmin(),
		Change: func(_ context.Context, tx *gorm.DB) (model.UserTag, error) {
			var tag model.UserTag
			if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&tag, "id = ?", command.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return tag, domainerr.ResourceNotFound("member tag", command.ID)
				}
				return tag, err
			}
			var referenced bool
			if err := tx.Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM audience_segment_user_tag
					WHERE user_tag_id = ?
				)
			`, tag.ID).Scan(&referenced).Error; err != nil {
				return tag, err
			}
			if referenced {
				return tag, domainerr.Precondition("member tag is referenced by an audience segment")
			}
			if err := tx.Where("tag_id = ?", command.ID).Delete(&model.UserTagMapping{}).Error; err != nil {
				return tag, err
			}
			return tag, tx.Delete(&tag).Error
		},
		Audit: func(tag model.UserTag) mutation.Audit {
			return mutation.RequestAudit(sharedtelemetry.AuditMemberTagDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagDeletedAuditRecord(metadata, tag.ID, tag.Name)
			})
		},
	})
	return err
}

// SetMemberTagsCommand replaces the tags assigned to a Member.
type SetMemberTagsCommand struct {
	MemberID string
	TagIDs   []string
}

// SetMemberTags applies command and returns the refreshed administrator view.
func (s *Service) SetMemberTags(ctx context.Context, command SetMemberTagsCommand) (*managev1.AdminMember, error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	if _, err := s.replaceMemberTags(ctx, command.MemberID, command.TagIDs); err != nil {
		return nil, err
	}
	return s.adminMember(ctx, command.MemberID)
}

func (s *Service) replaceMemberTags(ctx context.Context, memberID string, rawTagIDs []string) ([]string, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return nil, domainerr.InvalidField("member_id", "must be a canonical UUID")
	}
	tagIDs, err := memberTagIDs(rawTagIDs)
	if err != nil {
		return nil, err
	}
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, false)
	if err != nil {
		return nil, err
	}
	_, err = mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Subjects: []string{target.IdentityID},
		Access:   freshAdmin(),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			if err := recheckTarget(tx, memberID, target, linkedMember(false)); err != nil {
				return false, err
			}
			return storeMemberTags(tx, memberID, tagIDs)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditMemberUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagsUpdatedAuditRecord(metadata, memberID, tagIDs)
			})
		},
	})
	if err != nil {
		return nil, err
	}
	return tagIDs, nil
}

// memberTagIDs validates requested tag IDs and returns them deduplicated
// and sorted, the order the mapping is compared in.
func memberTagIDs(rawTagIDs []string) ([]string, error) {
	tagIDs := make([]string, 0, len(rawTagIDs))
	seen := make(map[string]struct{}, len(rawTagIDs))
	for _, tagID := range rawTagIDs {
		if _, err := uuidutil.ParseCanonical(tagID, "tag_id"); err != nil {
			return nil, domainerr.InvalidField("tag_ids", "must contain canonical UUIDs")
		}
		if _, exists := seen[tagID]; !exists {
			seen[tagID] = struct{}{}
			tagIDs = append(tagIDs, tagID)
		}
	}
	sort.Strings(tagIDs)
	return tagIDs, nil
}

// storeMemberTags replaces the Member's tag mapping with the existing tags
// tagIDs and reports whether it changed.
func storeMemberTags(tx *gorm.DB, memberID string, tagIDs []string) (bool, error) {
	if len(tagIDs) != 0 {
		var count int64
		if err := tx.Model(&model.UserTag{}).Where("id IN ?", tagIDs).Count(&count).Error; err != nil {
			return false, err
		}
		if count != int64(len(tagIDs)) {
			return false, domainerr.InvalidField("tag_ids", "contains an unknown member tag")
		}
	}
	var currentTagIDs []string
	if err := tx.Model(&model.UserTagMapping{}).
		Where("member_id = ?", memberID).
		Order("tag_id ASC").
		Pluck("tag_id", &currentTagIDs).Error; err != nil {
		return false, err
	}
	if slices.Equal(currentTagIDs, tagIDs) {
		return false, nil
	}
	if err := tx.Where("member_id = ?", memberID).Delete(&model.UserTagMapping{}).Error; err != nil {
		return false, err
	}
	for _, tagID := range tagIDs {
		if err := tx.Create(&model.UserTagMapping{MemberID: memberID, TagID: tagID}).Error; err != nil {
			return false, err
		}
	}
	return true, nil
}
