package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"

	"github.com/echovisionlab/geul-api/internal/sliceutil"
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
	tx Store,
	campaign Campaign,
) (CampaignDeliveryTarget, error) {
	target := CampaignDeliveryTarget{
		QueryVersion:   CampaignDeliveryTargetQueryVersion,
		Mode:           CampaignDeliveryTargetModeAllUsers,
		RecipientScope: campaign.RecipientScope,
	}
	if err := validateCampaignRecipientScope(target.RecipientScope); err != nil {
		return CampaignDeliveryTarget{}, domainerr.Precondition(err.Error())
	}
	if err := validateCampaignTargetDefinition(campaign); err != nil {
		return CampaignDeliveryTarget{}, domainerr.Precondition(err.Error())
	}
	if campaign.TargetMode == CampaignTargetModeAll {
		return target, nil
	}
	criteriaID := strings.TrimSpace(ptrStringValue(campaign.CriteriaID))

	criteria, err := tx.LockCriteriaTarget(ctx, criteriaID)
	if err != nil {
		if errors.Is(err, ErrCriteriaNotFound) {
			return CampaignDeliveryTarget{}, domainerr.Precondition(msgCampaignCriteriaNotFound)
		}
		return CampaignDeliveryTarget{}, err
	}
	if criteria.Archived {
		return CampaignDeliveryTarget{}, domainerr.Precondition("campaign Member criteria is archived")
	}
	if !criteria.Valid {
		return CampaignDeliveryTarget{}, domainerr.Precondition("campaign Member criteria is invalid")
	}
	target.CreatedAfter = criteria.CreatedAfter
	target.CreatedBefore = criteria.CreatedBefore
	target.MemberTagIDs = append([]string(nil), criteria.MemberTagIDs...)
	target.AccountRoles = append([]string(nil), criteria.AccountRoles...)
	target.ExcludedMemberIDs = append([]string(nil), criteria.ExcludedMemberIDs...)
	switch MemberCriteriaKind(strings.TrimSpace(string(criteria.Kind))) {
	case MemberCriteriaKindAllMembers:
		target.Mode = CampaignDeliveryTargetModeAllUsers
	case MemberCriteriaKindMemberTags:
		target.Mode = CampaignDeliveryTargetModeUserTags
	case MemberCriteriaKindFilter:
		target.Mode = CampaignDeliveryTargetModeUsersByFilter
	default:
		return CampaignDeliveryTarget{}, domainerr.Precondition("campaign Member criteria type is unsupported")
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
	if len(sliceutil.SortedUniqueTrimmed(target.ExcludedMemberIDs)) != len(target.ExcludedMemberIDs) {
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
	db Store,
	run DeliveryRun,
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

	loaded, err := db.LoadDeliveryTarget(ctx, run)
	if err != nil {
		return CampaignDeliveryTarget{}, err
	}
	target.MemberTagIDs, target.AccountRoles, target.ExcludedMemberIDs = loaded.MemberTagIDs, loaded.AccountRoles, loaded.ExcludedMemberIDs

	if err := ValidateCampaignDeliveryTarget(target); err != nil {
		return CampaignDeliveryTarget{}, err
	}
	return target, nil
}
