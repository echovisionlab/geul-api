package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// UpdateCampaignConfigurationCommand changes a draft Campaign's recipient
// targeting and layout; a nil field keeps its value. TargetMode and
// SegmentID are one patch unit: an absent segment clears it for ALL and
// fails validation for SEGMENT, and a segment needs a target mode.
type UpdateCampaignConfigurationCommand struct {
	ID             string
	TargetMode     *managev1.CampaignTargetMode
	SegmentID      *string
	RecipientScope *managev1.CampaignRecipientScope
	// LayoutID set to an empty value clears the layout.
	LayoutID *string
}

// CampaignConfiguration is the configuration an update left.
type CampaignConfiguration struct {
	ID             string
	Changed        bool
	TargetMode     string
	SegmentID      *string
	LayoutID       *string
	RecipientScope string
	UpdatedAt      time.Time
}

// campaignConfigurationChange is the committed configuration and the
// fields it changed.
type campaignConfigurationChange struct {
	CampaignConfiguration
	fields []string
}

// UpdateCampaignConfiguration owns durable recipient targeting. Collaboration
// documents must never mutate these relations because their internal RPC does
// not carry the authenticated Member actor needed for a trustworthy Audit.
func (s *Service) UpdateCampaignConfiguration(ctx context.Context, command UpdateCampaignConfigurationCommand) (CampaignConfiguration, error) {
	access, err := adminAccess(policyv1.Campaign.Edit(command.ID))
	if err != nil {
		return CampaignConfiguration{}, err
	}
	var campaign model.Campaign
	var observedLayoutID string
	var lockedLayouts map[string]CampaignLayoutReference
	change, err := mutation.Run(ctx, s.mutations, mutation.Spec[campaignConfigurationChange]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			var err error
			observedLayoutID, lockedLayouts, err = observeAndLockCampaignLayouts(ctx, tx, s.emailAuthoring, command.ID, command.LayoutID)
			if err != nil {
				return err
			}
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx *gorm.DB) (campaignConfigurationChange, error) {
			next, err := requestedCampaignConfiguration(campaign, command)
			if err != nil {
				return campaignConfigurationChange{}, err
			}
			if err := s.validateCampaignConfiguration(ctx, tx, campaign, next, command, observedLayoutID, lockedLayouts); err != nil {
				return campaignConfigurationChange{}, err
			}
			return applyCampaignConfiguration(ctx, tx, campaign, next)
		},
		Audit: func(change campaignConfigurationChange) mutation.Audit {
			if len(change.fields) == 0 {
				return mutation.Unchanged()
			}
			return campaignMetadataAudit(change.ID, change.fields)
		},
	})
	return change.CampaignConfiguration, err
}

// requestedCampaignConfiguration overlays the command on the Campaign's
// current configuration.
func requestedCampaignConfiguration(campaign model.Campaign, command UpdateCampaignConfigurationCommand) (CampaignConfiguration, error) {
	next := CampaignConfiguration{
		ID: campaign.ID, TargetMode: campaign.TargetMode, SegmentID: campaign.SegmentID,
		LayoutID: campaign.LayoutID, RecipientScope: campaign.RecipientScope, UpdatedAt: campaign.UpdatedAt,
	}
	touched := false
	if command.TargetMode != nil {
		parsedMode, err := campaignTargetModeFromProto(*command.TargetMode)
		if err != nil {
			return next, domainerr.InvalidField("target_mode", err.Error())
		}
		next.TargetMode = parsedMode
		next.SegmentID = nullableTrimmedString(ptrStringValue(command.SegmentID))
		touched = true
	} else if command.SegmentID != nil {
		return next, domainerr.InvalidField("segment_id", "must be updated together with target_mode")
	}
	if command.RecipientScope != nil {
		parsedScope, err := campaignRecipientScopeFromProto(*command.RecipientScope)
		if err != nil {
			return next, domainerr.InvalidField("recipient_scope", err.Error())
		}
		next.RecipientScope = parsedScope
		touched = true
	}
	if command.LayoutID != nil {
		next.LayoutID = nullableTrimmedString(ptrStringValue(command.LayoutID))
		touched = true
	}
	if !touched {
		return next, domainerr.InvalidField("configuration", "must include at least one configuration field")
	}
	return next, nil
}

// validateCampaignConfiguration checks the requested configuration against
// the locked draft, its observed layout and the requested segment and
// layout.
func (s *Service) validateCampaignConfiguration(
	ctx context.Context,
	tx *gorm.DB,
	campaign model.Campaign,
	next CampaignConfiguration,
	command UpdateCampaignConfigurationCommand,
	observedLayoutID string,
	lockedLayouts map[string]CampaignLayoutReference,
) error {
	if err := validateCampaignTargetDefinition(model.Campaign{TargetMode: next.TargetMode, SegmentID: next.SegmentID}); err != nil {
		return domainerr.InvalidField("target_mode", err.Error())
	}
	if !campaignStatusAllowsEdit(campaign.Status) {
		return domainerr.Precondition(msgCampaignCannotUpdateSent)
	}
	if ptrStringValue(campaign.LayoutID) != observedLayoutID {
		return domainerr.Precondition("campaign layout assignment changed; retry")
	}
	if next.TargetMode == model.CampaignTargetModeSegment {
		if err := s.requireAssignableSegment(ctx, tx, *next.SegmentID); err != nil {
			return err
		}
	}
	if command.LayoutID != nil && next.LayoutID != nil {
		if _, ok := lockedLayouts[*next.LayoutID]; !ok {
			return domainerr.ResourceNotFound("email_layout", *next.LayoutID)
		}
	}
	return nil
}

// applyCampaignConfiguration writes the fields of next that differ from the
// locked Campaign.
func applyCampaignConfiguration(ctx context.Context, tx *gorm.DB, campaign model.Campaign, next CampaignConfiguration) (campaignConfigurationChange, error) {
	updates := structured.Fields{}
	var fields []string
	if campaign.TargetMode != next.TargetMode {
		fields, updates["target_mode"] = append(fields, "target_mode"), next.TargetMode
	}
	if ptrStringValue(campaign.SegmentID) != ptrStringValue(next.SegmentID) {
		fields, updates["segment_id"] = append(fields, "segment"), next.SegmentID
	}
	if ptrStringValue(campaign.LayoutID) != ptrStringValue(next.LayoutID) {
		fields, updates["layout_id"] = append(fields, "layout"), next.LayoutID
	}
	if campaign.RecipientScope != next.RecipientScope {
		fields, updates["recipient_scope"] = append(fields, "recipient_scope"), next.RecipientScope
	}
	if len(fields) == 0 {
		return campaignConfigurationChange{CampaignConfiguration: next}, nil
	}
	next.UpdatedAt = dbtime.Now()
	updates["updated_at"] = next.UpdatedAt
	if err := tx.WithContext(ctx).Model(&campaign).Updates(updates).Error; err != nil {
		return campaignConfigurationChange{}, err
	}
	next.Changed = true
	return campaignConfigurationChange{CampaignConfiguration: next, fields: fields}, nil
}

// observeAndLockCampaignLayouts reads the Campaign's current layout and
// locks it with the requested one through Email Authoring, before the
// Campaign row, so a concurrent layout deletion waits for the Campaign
// write.
func observeAndLockCampaignLayouts(
	ctx context.Context,
	tx *gorm.DB,
	emailAuthoring CampaignEmailAuthoringPort,
	campaignID string,
	requestedLayoutID *string,
) (string, map[string]CampaignLayoutReference, error) {
	var observed struct {
		LayoutID *string `gorm:"column:layout_id"`
	}
	if err := tx.WithContext(ctx).
		Model(&model.Campaign{}).
		Select("layout_id").
		First(&observed, "id = ?", campaignID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, errCampaignNotFound()
		}
		return "", nil, err
	}
	observedLayoutID := strings.TrimSpace(ptrStringValue(observed.LayoutID))
	if observedLayoutID == "" && strings.TrimSpace(ptrStringValue(requestedLayoutID)) == "" {
		return observedLayoutID, map[string]CampaignLayoutReference{}, nil
	}
	lockedLayouts, err := emailAuthoring.LockLayoutsForCampaign(ctx, tx, observedLayoutID, ptrStringValue(requestedLayoutID))
	return observedLayoutID, lockedLayouts, err
}
