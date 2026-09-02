package campaign

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

const (
	CampaignDeliveryTargetQueryVersion    int16 = 2
	CampaignDeliverySnapshotSchemaVersion int16 = 1

	CampaignDeliveryTargetModeAllUsers      = "all_users"
	CampaignDeliveryTargetModeUserTags      = "user_tags"
	CampaignDeliveryTargetModeUsersByFilter = "users_by_filter"
)

type CampaignDeliverySnapshotTranslation struct {
	Locale      string `json:"locale"`
	Subject     string `json:"subject"`
	ContentHTML string `json:"content_html"`
}

type CampaignDeliverySnapshotLayout struct {
	Locale      string `json:"locale"`
	HTMLContent string `json:"html_content"`
}

// CampaignDeliverySnapshot is the immutable Campaign-owned render fact.
// Email Authoring supplies a layout snapshot through a port before this value
// is sealed; scheduled delivery never reloads mutable authoring rows.
type CampaignDeliverySnapshot struct {
	Subject            string                                `json:"subject"`
	ContentHTML        string                                `json:"content_html"`
	SourceLocale       string                                `json:"source_locale"`
	Translations       []CampaignDeliverySnapshotTranslation `json:"translations"`
	LayoutSourceLocale *string                               `json:"layout_source_locale,omitempty"`
	LayoutTranslations *[]CampaignDeliverySnapshotLayout     `json:"layout_translations,omitempty"`
}

func ValidateCampaignDeliverySnapshot(snapshot CampaignDeliverySnapshot) error {
	if strings.TrimSpace(snapshot.SourceLocale) == "" {
		return fmt.Errorf("render snapshot source_locale is required")
	}
	sourceCount := 0
	for index, row := range snapshot.Translations {
		if strings.TrimSpace(row.Locale) == "" {
			return fmt.Errorf("render snapshot translation %d locale is required", index)
		}
		if row.Locale == snapshot.SourceLocale {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		return fmt.Errorf("render snapshot requires exactly one source translation")
	}
	if (snapshot.LayoutSourceLocale == nil) != (snapshot.LayoutTranslations == nil) {
		return fmt.Errorf("render snapshot layout source locale and translations must appear together")
	}
	if snapshot.LayoutSourceLocale == nil {
		return nil
	}
	if strings.TrimSpace(*snapshot.LayoutSourceLocale) == "" {
		return fmt.Errorf("render snapshot layout_source_locale is required")
	}
	layoutSourceCount := 0
	for index, row := range *snapshot.LayoutTranslations {
		if strings.TrimSpace(row.Locale) == "" {
			return fmt.Errorf("render snapshot layout translation %d locale is required", index)
		}
		if row.Locale == *snapshot.LayoutSourceLocale {
			layoutSourceCount++
		}
	}
	if layoutSourceCount != 1 {
		return fmt.Errorf("render snapshot requires exactly one layout source translation")
	}
	return nil
}

type CampaignDeliveryTarget struct {
	QueryVersion      int16
	Mode              string
	RecipientScope    string
	CreatedAfter      *time.Time
	CreatedBefore     *time.Time
	MemberTagIDs      []string
	AccountRoles      []string
	ExcludedMemberIDs []string
}

func deriveCampaignDeliveryTarget(
	ctx context.Context,
	tx *gorm.DB,
	campaign model.Campaign,
	audienceTargets CampaignAudienceTargetPort,
) (CampaignDeliveryTarget, error) {
	target := CampaignDeliveryTarget{
		QueryVersion:   CampaignDeliveryTargetQueryVersion,
		Mode:           CampaignDeliveryTargetModeAllUsers,
		RecipientScope: campaign.RecipientScope,
	}
	if err := validateCampaignRecipientScope(target.RecipientScope); err != nil {
		return CampaignDeliveryTarget{}, errs.FailedPrecondition(err.Error())
	}
	if err := validateCampaignTargetDefinition(campaign); err != nil {
		return CampaignDeliveryTarget{}, errs.FailedPrecondition(err.Error())
	}
	if campaign.TargetMode == model.CampaignTargetModeAll {
		return target, nil
	}
	segmentID := strings.TrimSpace(ptrStringValue(campaign.SegmentID))
	if audienceTargets == nil {
		return CampaignDeliveryTarget{}, errs.DependencyUnavailable("Campaign audience target")
	}
	segment, err := audienceTargets.LockTarget(ctx, tx, segmentID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return CampaignDeliveryTarget{}, errs.FailedPrecondition(errs.MsgCampaignSegmentNotFound)
		}
		return CampaignDeliveryTarget{}, err
	}
	if segment.Archived {
		return CampaignDeliveryTarget{}, errs.FailedPrecondition("campaign audience segment is archived")
	}
	if !segment.Valid {
		return CampaignDeliveryTarget{}, errs.FailedPrecondition("campaign audience segment is invalid")
	}
	target.CreatedAfter = segment.CreatedAfter
	target.CreatedBefore = segment.CreatedBefore
	target.MemberTagIDs = append([]string(nil), segment.MemberTagIDs...)
	target.AccountRoles = append([]string(nil), segment.AccountRoles...)
	target.ExcludedMemberIDs = append([]string(nil), segment.ExcludedMemberIDs...)
	switch strings.TrimSpace(segment.SegmentType) {
	case managev1.SegmentType_SEGMENT_TYPE_ALL_MEMBERS.String():
		target.Mode = CampaignDeliveryTargetModeAllUsers
	case managev1.SegmentType_SEGMENT_TYPE_MEMBER_TAGS.String():
		target.Mode = CampaignDeliveryTargetModeUserTags
	case managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER.String():
		target.Mode = CampaignDeliveryTargetModeUsersByFilter
	default:
		return CampaignDeliveryTarget{}, errs.FailedPrecondition("campaign audience segment type is unsupported")
	}
	return target, nil
}

func ValidateCampaignDeliveryTarget(target CampaignDeliveryTarget) error {
	if target.QueryVersion != CampaignDeliveryTargetQueryVersion {
		return fmt.Errorf("unsupported email delivery target query version: %d", target.QueryVersion)
	}
	if target.CreatedAfter != nil && target.CreatedBefore != nil && target.CreatedAfter.After(*target.CreatedBefore) {
		return fmt.Errorf("email delivery target created_after exceeds created_before")
	}
	if len(sortedUniqueNonEmptyCampaignStrings(target.ExcludedMemberIDs)) != len(target.ExcludedMemberIDs) {
		return fmt.Errorf("email delivery target excluded member IDs must be unique and non-empty")
	}
	if err := validateCampaignRecipientScope(target.RecipientScope); err != nil {
		return err
	}
	hasUserTags := len(target.MemberTagIDs) > 0
	hasUserFilter := len(target.AccountRoles) > 0 || len(target.ExcludedMemberIDs) > 0 || target.CreatedAfter != nil || target.CreatedBefore != nil
	switch target.Mode {
	case CampaignDeliveryTargetModeAllUsers:
		if hasUserTags || hasUserFilter {
			return fmt.Errorf("all-users email delivery target is structurally invalid")
		}
	case CampaignDeliveryTargetModeUserTags:
		if !hasUserTags || hasUserFilter {
			return fmt.Errorf("user-tags email delivery target is structurally invalid")
		}
	case CampaignDeliveryTargetModeUsersByFilter:
		if hasUserTags {
			return fmt.Errorf("users-by-filter email delivery target is structurally invalid")
		}
	default:
		return fmt.Errorf("unsupported email delivery target mode: %s", target.Mode)
	}
	return nil
}

func loadCampaignDeliveryTarget(
	ctx context.Context,
	db *gorm.DB,
	run model.CampaignDeliveryRun,
) (CampaignDeliveryTarget, error) {
	if !run.DefinitionSealed {
		return CampaignDeliveryTarget{}, fmt.Errorf("email delivery run definition is not sealed")
	}
	if run.SnapshotSchemaVersion != CampaignDeliverySnapshotSchemaVersion {
		return CampaignDeliveryTarget{}, fmt.Errorf(
			"unsupported email delivery snapshot schema version: %d",
			run.SnapshotSchemaVersion,
		)
	}
	target := CampaignDeliveryTarget{
		QueryVersion:   run.TargetQueryVersion,
		Mode:           run.TargetMode,
		RecipientScope: run.TargetRecipientScope,
		CreatedAfter:   run.TargetCreatedAfter,
		CreatedBefore:  run.TargetCreatedBefore,
	}
	if target.QueryVersion != CampaignDeliveryTargetQueryVersion {
		return CampaignDeliveryTarget{}, fmt.Errorf(
			"unsupported email delivery target query version: %d",
			target.QueryVersion,
		)
	}
	if err := db.WithContext(ctx).
		Model(&model.EmailDeliveryRunTargetUserTag{}).
		Where("run_id = ?", run.ID).
		Order("user_tag_id ASC").
		Pluck("user_tag_id", &target.MemberTagIDs).Error; err != nil {
		return CampaignDeliveryTarget{}, err
	}
	if err := db.WithContext(ctx).
		Model(&model.EmailDeliveryRunTargetUserRole{}).
		Where("run_id = ?", run.ID).
		Order("role ASC").
		Pluck("role", &target.AccountRoles).Error; err != nil {
		return CampaignDeliveryTarget{}, err
	}
	if err := db.WithContext(ctx).
		Model(&model.EmailDeliveryRunTargetExcludedMember{}).
		Where("run_id = ?", run.ID).
		Order("member_id ASC").
		Pluck("member_id", &target.ExcludedMemberIDs).Error; err != nil {
		return CampaignDeliveryTarget{}, err
	}
	if err := ValidateCampaignDeliveryTarget(target); err != nil {
		return CampaignDeliveryTarget{}, err
	}
	return target, nil
}

func campaignDeliveryTargetRecipientSelection(
	target CampaignDeliveryTarget,
) (*bulkEmailRecipientSelection, error) {
	if err := ValidateCampaignDeliveryTarget(target); err != nil {
		return nil, err
	}
	selection := &bulkEmailRecipientSelection{
		RequireNewsletterSubscription: target.RecipientScope == campaignRecipientScopeSubscribedUsers,
	}
	switch target.Mode {
	case CampaignDeliveryTargetModeAllUsers:
		selection.Mode = CampaignDeliveryTargetModeAllUsers
	case CampaignDeliveryTargetModeUserTags:
		if len(target.MemberTagIDs) == 0 {
			return nil, fmt.Errorf("user-tags target has no user tags")
		}
		selection.Mode = CampaignDeliveryTargetModeUserTags
		selection.MemberTagIDs = target.MemberTagIDs
	case CampaignDeliveryTargetModeUsersByFilter:
		selection.Mode = CampaignDeliveryTargetModeUsersByFilter
		selection.Filters = &bulkEmailRecipientFilters{
			AccountRoles:      target.AccountRoles,
			CreatedAfter:      target.CreatedAfter,
			CreatedBefore:     target.CreatedBefore,
			ExcludedMemberIDs: target.ExcludedMemberIDs,
		}
	default:
		return nil, fmt.Errorf("unsupported email delivery target mode: %s", target.Mode)
	}
	return selection, nil
}

func sortedUniqueNonEmptyCampaignStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
