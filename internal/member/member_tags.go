package member

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *MemberService) getMemberTagIDs(ctx context.Context, memberID string) ([]string, error) {
	values := []string{}
	err := s.db.WithContext(ctx).Model(&model.UserTagMapping{}).Where("member_id = ?", memberID).Order("tag_id").Pluck("tag_id", &values).Error
	return values, err
}
func (s *MemberService) getMemberTagIDsBatch(ctx context.Context, memberIDs []string) (map[string][]string, error) {
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
func (s *MemberService) ListMemberTagsAdmin(
	ctx context.Context,
	req *connect.Request[managev1.ListMemberTagsAdminRequest],
) (*connect.Response[managev1.ListMemberTagsAdminResponse], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	limit, offset := 50, 0
	if p := req.Msg.Pagination; p != nil {
		if p.Limit > 0 {
			limit = int(p.Limit)
		}
		if p.Offset > 0 {
			offset = int(p.Offset)
		}
	}
	if limit > 500 {
		limit = 500
	}
	query := s.db.WithContext(ctx).Table("user_tag AS t")
	var err error
	query, err = memberTagAdminFilterConfig.ApplyFilters(query, req.Msg.Filters)
	if err != nil {
		return nil, err
	}
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, errs.Internal(err)
	}
	type row struct {
		model.UserTag
		MemberCount int32 `gorm:"column:member_count"`
	}
	var rows []row
	query = query.Select("t.*, COUNT(m.member_id)::int AS member_count").
		Joins("LEFT JOIN user_tag_mapping AS m ON m.tag_id = t.id").
		Group("t.id")
	query, err = memberTagAdminSortConfig.ApplySort(query, req.Msg.Sorts)
	if err != nil {
		return nil, err
	}
	if len(req.Msg.Sorts) != 0 {
		query = query.Order("t.id ASC")
	}
	if err := query.Limit(limit).Offset(offset).Scan(&rows).Error; err != nil {
		return nil, errs.Internal(err)
	}
	tags := make([]*managev1.MemberTag, len(rows))
	for i, row := range rows {
		tags[i] = &managev1.MemberTag{Id: row.ID, Name: row.Name, CreatedAt: timestamppb.New(row.CreatedAt), MemberCount: row.MemberCount}
	}
	return connect.NewResponse(&managev1.ListMemberTagsAdminResponse{Tags: tags, Pagination: &commonv1.PaginationResponse{Total: int32(min(total, math.MaxInt32)), Limit: int32(limit), Offset: int32(offset), HasMore: int64(offset+len(rows)) < total}}), nil
}
func (s *MemberService) CreateMemberTag(
	ctx context.Context,
	req *connect.Request[managev1.CreateMemberTagRequest],
) (*connect.Response[managev1.MemberTag], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Msg.Name)
	if name == "" {
		return nil, errs.Required("name")
	}
	tag := model.UserTag{Name: name}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&tag).Error; err != nil {
			return errs.Internal(err)
		}
		return domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMemberTagCreated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagCreatedAuditRecord(metadata, tag.ID, tag.Name)
			},
		)
	}); err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.MemberTag{Id: tag.ID, Name: tag.Name, CreatedAt: timestamppb.New(tag.CreatedAt)}), nil
}
func (s *MemberService) DeleteMemberTag(
	ctx context.Context,
	req *connect.Request[managev1.DeleteMemberTagRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var tag model.UserTag
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&tag, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFound("member tag", req.Msg.Id)
			}
			return errs.Internal(err)
		}

		var referenced bool
		if err := tx.Raw(`
			SELECT EXISTS (
				SELECT 1
				FROM audience_segment_user_tag
				WHERE user_tag_id = ?
			)
		`, tag.ID).Scan(&referenced).Error; err != nil {
			return errs.Internal(err)
		}
		if referenced {
			return errs.FailedPrecondition("member tag is referenced by an audience segment")
		}
		if err := tx.Where("tag_id = ?", req.Msg.Id).Delete(&model.UserTagMapping{}).Error; err != nil {
			return errs.Internal(err)
		}
		if err := tx.Delete(&tag).Error; err != nil {
			return errs.Internal(err)
		}
		return domainaudit.AppendOptionalRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMemberTagDeleted,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagDeletedAuditRecord(metadata, tag.ID, tag.Name)
			},
		)
	})
	if err != nil {
		return nil, errs.Wrap(err)
	}
	return connect.NewResponse(&managev1.DeleteResponse{Success: true}), nil
}

func (s *MemberService) replaceMemberTags(ctx context.Context, memberID string, rawTagIDs []string) ([]string, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return nil, errs.InvalidArgument("member_id", "must be a canonical UUID")
	}
	tagIDs := make([]string, 0, len(rawTagIDs))
	seen := make(map[string]struct{}, len(rawTagIDs))
	for _, tagID := range rawTagIDs {
		if _, err := uuidutil.ParseCanonical(tagID, "tag_id"); err != nil {
			return nil, errs.InvalidArgument("tag_ids", "must contain canonical UUIDs")
		}
		if _, exists := seen[tagID]; exists {
			continue
		}
		seen[tagID] = struct{}{}
		tagIDs = append(tagIDs, tagID)
	}
	sort.Strings(tagIDs)
	target, err := authorizationtarget.RequireLinkedMember(ctx, s.db, memberID, false)
	if err != nil {
		return nil, err
	}

	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, target.IdentityID); err != nil {
			return errs.Internal(err)
		}
		current, err := authorizationtarget.LinkedMemberForMember(tx.WithContext(ctx), memberID, false)
		if err != nil {
			if errors.Is(err, authorizationtarget.ErrIneligible) {
				return errs.NotFound("member", memberID)
			}
			return err
		}
		if current.IdentityID != target.IdentityID {
			return errs.NotFound("member", memberID)
		}

		if len(tagIDs) != 0 {
			var count int64
			if err := tx.Model(&model.UserTag{}).Where("id IN ?", tagIDs).Count(&count).Error; err != nil {
				return errs.Internal(err)
			}
			if count != int64(len(tagIDs)) {
				return errs.InvalidArgument("tag_ids", "contains an unknown member tag")
			}
		}
		var currentTagIDs []string
		if err := tx.Model(&model.UserTagMapping{}).
			Where("member_id = ?", memberID).
			Order("tag_id ASC").
			Pluck("tag_id", &currentTagIDs).Error; err != nil {
			return errs.Internal(err)
		}
		if slices.Equal(currentTagIDs, tagIDs) {
			return nil
		}
		if err := tx.Where("member_id = ?", memberID).Delete(&model.UserTagMapping{}).Error; err != nil {
			return errs.Internal(err)
		}
		for _, tagID := range tagIDs {
			if err := tx.Create(&model.UserTagMapping{MemberID: memberID, TagID: tagID}).Error; err != nil {
				return errs.Internal(err)
			}
		}
		return domainaudit.AppendRequest(
			ctx,
			tx,
			s.auditWriter,
			sharedtelemetry.AuditMemberUpdated,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewMemberTagsUpdatedAuditRecord(metadata, memberID, tagIDs)
			},
		)
	})
	if err != nil {
		return nil, err
	}
	return tagIDs, nil
}

func (s *MemberService) SetMemberTags(
	ctx context.Context,
	req *connect.Request[managev1.SetMemberTagsRequest],
) (*connect.Response[managev1.AdminMember], error) {
	if _, err := s.requireAdminMember(ctx); err != nil {
		return nil, err
	}
	if _, err := s.replaceMemberTags(ctx, req.Msg.MemberId, req.Msg.TagIds); err != nil {
		return nil, err
	}
	return s.GetMember(ctx, connect.NewRequest(&managev1.GetMemberRequest{MemberId: req.Msg.MemberId}))
}
