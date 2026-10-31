package audience

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/sliceutil"
	"github.com/echovisionlab/geul-api/internal/structured"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func applySegmentConfig(segment *model.AudienceSegment, config model.AudienceSegmentConfig) error {
	if segment == nil {
		return fmt.Errorf("audience segment is required")
	}
	if config.CreatedAfter != nil && config.CreatedBefore != nil && config.CreatedAfter.After(*config.CreatedBefore) {
		return fmt.Errorf("created_after must not be later than created_before")
	}
	segment.CreatedAfter = config.CreatedAfter
	segment.CreatedBefore = config.CreatedBefore
	segment.Config = config
	return nil
}

// LoadSegmentConfigs loads the normalized relation-backed configuration for
// each non-nil segment.
func LoadSegmentConfigs(ctx context.Context, db *gorm.DB, segments []*model.AudienceSegment) error {
	byID := make(map[string]*model.AudienceSegment, len(segments))
	ids := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment == nil {
			continue
		}
		segment.Config = model.AudienceSegmentConfig{
			CreatedAfter:  segment.CreatedAfter,
			CreatedBefore: segment.CreatedBefore,
		}
		byID[segment.ID] = segment
		ids = append(ids, segment.ID)
	}
	if len(ids) == 0 {
		return nil
	}

	var userTags []model.AudienceSegmentUserTag
	if err := db.WithContext(ctx).Where("audience_segment_id IN ?", ids).Find(&userTags).Error; err != nil {
		return err
	}
	for _, association := range userTags {
		if segment := byID[association.AudienceSegmentID]; segment != nil {
			segment.Config.MemberTagIDs = append(segment.Config.MemberTagIDs, association.UserTagID)
		}
	}

	var userRoles []model.AudienceSegmentUserRole
	if err := db.WithContext(ctx).Where("audience_segment_id IN ?", ids).Find(&userRoles).Error; err != nil {
		return err
	}
	for _, association := range userRoles {
		if segment := byID[association.AudienceSegmentID]; segment != nil {
			segment.Config.AccountRoles = append(segment.Config.AccountRoles, association.Role)
		}
	}

	var excludedMembers []model.AudienceSegmentExcludedMember
	if err := db.WithContext(ctx).Where("audience_segment_id IN ?", ids).Find(&excludedMembers).Error; err != nil {
		return err
	}
	for _, association := range excludedMembers {
		if segment := byID[association.AudienceSegmentID]; segment != nil {
			segment.Config.ExcludeMemberIDs = append(segment.Config.ExcludeMemberIDs, association.MemberID)
		}
	}
	for _, segment := range segments {
		if segment != nil {
			sortSegmentConfig(&segment.Config)
		}
	}
	return nil
}

// LoadSegmentConfig loads the normalized relation-backed configuration for a
// segment.
func LoadSegmentConfig(ctx context.Context, db *gorm.DB, segment *model.AudienceSegment) error {
	return LoadSegmentConfigs(ctx, db, []*model.AudienceSegment{segment})
}

func replaceSegmentRelations(
	tx *gorm.DB,
	memberReferences MemberReferences,
	segmentID string,
	config model.AudienceSegmentConfig,
) error {
	if len(config.ExcludeMemberIDs) > 0 {
		eligible, err := memberReferences.EligibleIDs(tx.Statement.Context, tx, config.ExcludeMemberIDs)
		if err != nil {
			return err
		}
		if len(eligible) != len(config.ExcludeMemberIDs) {
			return fmt.Errorf("every excluded member must be an onboarded Member with an exact Identity link")
		}
	}
	for _, association := range (structured.Values{
		&model.AudienceSegmentUserTag{},
		&model.AudienceSegmentUserRole{},
		&model.AudienceSegmentExcludedMember{},
	}) {
		if err := tx.Where("audience_segment_id = ?", segmentID).Delete(association).Error; err != nil {
			return err
		}
	}
	if len(config.MemberTagIDs) > 0 {
		rows := make([]model.AudienceSegmentUserTag, 0, len(config.MemberTagIDs))
		for _, tagID := range config.MemberTagIDs {
			rows = append(rows, model.AudienceSegmentUserTag{AudienceSegmentID: segmentID, UserTagID: tagID})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(config.AccountRoles) > 0 {
		rows := make([]model.AudienceSegmentUserRole, 0, len(config.AccountRoles))
		for _, role := range config.AccountRoles {
			rows = append(rows, model.AudienceSegmentUserRole{AudienceSegmentID: segmentID, Role: role})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	if len(config.ExcludeMemberIDs) > 0 {
		rows := make([]model.AudienceSegmentExcludedMember, 0, len(config.ExcludeMemberIDs))
		for _, memberID := range config.ExcludeMemberIDs {
			rows = append(rows, model.AudienceSegmentExcludedMember{AudienceSegmentID: segmentID, MemberID: memberID})
		}
		if err := tx.Create(&rows).Error; err != nil {
			return err
		}
	}
	return nil
}

func sortSegmentConfig(config *model.AudienceSegmentConfig) {
	if config == nil {
		return
	}
	config.MemberTagIDs = sliceutil.SortedUniqueTrimmed(config.MemberTagIDs)
	config.AccountRoles = sliceutil.SortedUniqueTrimmed(config.AccountRoles)
	config.ExcludeMemberIDs = sliceutil.SortedUniqueTrimmed(config.ExcludeMemberIDs)
}

// SegmentConfigInput is a requested segment filter before validation.
type SegmentConfigInput struct {
	MemberTagIDs []string
	// AccountRoles are role IDs (policyv1.Role); an empty ID stands for an
	// API role the domain does not support.
	AccountRoles     []string
	CreatedAfter     *time.Time
	CreatedBefore    *time.Time
	ExcludeMemberIDs []string
	// malformedTime names a time bound whose wire value is not a valid
	// timestamp; validation rejects it after the identifier checks.
	malformedTime string
}

// normalizeSegmentConfig validates a requested filter and returns it with
// canonical, sorted and deduplicated identifiers. A nil input is the empty
// filter.
func normalizeSegmentConfig(input *SegmentConfigInput) (model.AudienceSegmentConfig, error) {
	if input == nil {
		return model.AudienceSegmentConfig{}, nil
	}
	roles, err := normalizeAccountRoles(input.AccountRoles)
	if err != nil {
		return model.AudienceSegmentConfig{}, err
	}
	memberTagIDs, err := canonicalUUIDs(input.MemberTagIDs, "member_tag_ids")
	if err != nil {
		return model.AudienceSegmentConfig{}, err
	}
	excludeMemberIDs, err := canonicalUUIDs(input.ExcludeMemberIDs, "exclude_member_ids")
	if err != nil {
		return model.AudienceSegmentConfig{}, err
	}
	if input.malformedTime != "" {
		return model.AudienceSegmentConfig{}, fmt.Errorf("%s is invalid", input.malformedTime)
	}
	result := model.AudienceSegmentConfig{
		MemberTagIDs: memberTagIDs, AccountRoles: roles,
		CreatedAfter: utcTime(input.CreatedAfter), CreatedBefore: utcTime(input.CreatedBefore),
		ExcludeMemberIDs: excludeMemberIDs,
	}
	sortSegmentConfig(&result)
	return result, nil
}

func normalizeAccountRoles(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, role := range values {
		switch role {
		case policyv1.Role.Admin().ID(), policyv1.Role.Author().ID(), policyv1.Role.User().ID():
		default:
			return nil, fmt.Errorf("account_roles contains an unsupported role")
		}
		if _, exists := seen[role]; exists {
			continue
		}
		seen[role] = struct{}{}
		result = append(result, role)
	}
	sort.Strings(result)
	return result, nil
}

func utcTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	parsed := value.UTC()
	return &parsed
}

func timestampOrNil(value *time.Time) *timestamppb.Timestamp {
	if value == nil {
		return nil
	}
	return timestamppb.New(*value)
}

func canonicalUUIDs(values []string, field string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, rawValue := range values {
		parsed, err := uuid.Parse(strings.TrimSpace(rawValue))
		if err != nil {
			return nil, fmt.Errorf("%s contains an invalid UUID", field)
		}
		value := parsed.String()
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// ValidateSegmentConfigForType validates a normalized segment configuration.
func ValidateSegmentConfigForType(segmentType model.AudienceSegmentType, config model.AudienceSegmentConfig) error {
	switch model.AudienceSegmentType(strings.TrimSpace(string(segmentType))) {
	case model.AudienceSegmentAllMembers:
		if len(config.MemberTagIDs) > 0 ||
			len(config.AccountRoles) > 0 ||
			config.CreatedAfter != nil ||
			config.CreatedBefore != nil ||
			len(config.ExcludeMemberIDs) > 0 {
			return fmt.Errorf("all-members segments do not accept filters")
		}
	case model.AudienceSegmentMemberTags:
		if len(config.MemberTagIDs) == 0 {
			return fmt.Errorf("member-tag segments require member_tag_ids")
		}
		if len(config.AccountRoles) > 0 ||
			config.CreatedAfter != nil ||
			config.CreatedBefore != nil ||
			len(config.ExcludeMemberIDs) > 0 {
			return fmt.Errorf("member-tag segments only accept member_tag_ids")
		}
	case model.AudienceSegmentMembersByFilter:
		if len(config.MemberTagIDs) > 0 {
			return fmt.Errorf("member-filter segments do not accept tag ids")
		}
	default:
		return fmt.Errorf("unknown segment type: %s", segmentType)
	}
	if config.CreatedAfter != nil && config.CreatedBefore != nil && config.CreatedAfter.After(*config.CreatedBefore) {
		return fmt.Errorf("created_after must not be later than created_before")
	}
	return nil
}
