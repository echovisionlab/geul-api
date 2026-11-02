package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"

	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// UpdateCampaignConfigurationCommand changes a draft Campaign's recipient
// targeting and layout; a nil field keeps its value. TargetMode and
// SegmentID are one patch unit: an absent segment clears it for ALL and
// fails validation for SEGMENT, and a segment needs a target mode.
type UpdateCampaignConfigurationCommand struct {
	ID             string
	TargetMode     *TargetMode
	SegmentID      *string
	RecipientScope *RecipientScope
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
	var campaign Campaign
	var observedLayoutID string
	var lockedLayouts map[string]CampaignLayoutReference
	change, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[campaignConfigurationChange, Store]{
		Lock: func(ctx context.Context, tx Store) error {
			var err error
			observedLayoutID, lockedLayouts, err = observeAndLockCampaignLayouts(ctx, tx, command.ID, command.LayoutID)
			if err != nil {
				return err
			}
			return lockCampaign(ctx, tx, command.ID, &campaign)
		},
		Access: access,
		Change: func(ctx context.Context, tx Store) (campaignConfigurationChange, error) {
			next, err := requestedCampaignConfiguration(campaign, command)
			if err != nil {
				return campaignConfigurationChange{}, err
			}
			if err := s.validateCampaignConfiguration(ctx, tx, campaign, next, command, observedLayoutID, lockedLayouts); err != nil {
				return campaignConfigurationChange{}, err
			}
			return applyCampaignConfiguration(ctx, tx, campaign, next, s.now())
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
func requestedCampaignConfiguration(campaign Campaign, command UpdateCampaignConfigurationCommand) (CampaignConfiguration, error) {
	next := CampaignConfiguration{
		ID: campaign.ID, TargetMode: campaign.TargetMode, SegmentID: campaign.SegmentID,
		LayoutID: campaign.LayoutID, RecipientScope: campaign.RecipientScope, UpdatedAt: campaign.UpdatedAt,
	}
	touched := false
	if command.TargetMode != nil {
		parsedMode, err := campaignTargetModeValue(*command.TargetMode)
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
		parsedScope, err := campaignRecipientScopeValue(*command.RecipientScope)
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
	tx Store,
	campaign Campaign,
	next CampaignConfiguration,
	command UpdateCampaignConfigurationCommand,
	observedLayoutID string,
	lockedLayouts map[string]CampaignLayoutReference,
) error {
	if err := validateCampaignTargetDefinition(Campaign{TargetMode: next.TargetMode, SegmentID: next.SegmentID}); err != nil {
		return domainerr.InvalidField("target_mode", err.Error())
	}
	if !campaignStatusAllowsEdit(campaign.Status) {
		return domainerr.Precondition(msgCampaignCannotUpdateSent)
	}
	if ptrStringValue(campaign.LayoutID) != observedLayoutID {
		return domainerr.Precondition("campaign layout assignment changed; retry")
	}
	if next.TargetMode == CampaignTargetModeSegment {
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
func applyCampaignConfiguration(ctx context.Context, tx Store, campaign Campaign, next CampaignConfiguration, now time.Time) (campaignConfigurationChange, error) {
	var fields []string
	if campaign.TargetMode != next.TargetMode {
		fields = append(fields, "target_mode")
	}
	if ptrStringValue(campaign.SegmentID) != ptrStringValue(next.SegmentID) {
		fields = append(fields, "segment")
	}
	if ptrStringValue(campaign.LayoutID) != ptrStringValue(next.LayoutID) {
		fields = append(fields, "layout")
	}
	if campaign.RecipientScope != next.RecipientScope {
		fields = append(fields, "recipient_scope")
	}
	if len(fields) == 0 {
		return campaignConfigurationChange{CampaignConfiguration: next}, nil
	}
	next.UpdatedAt = now.UTC().Truncate(dbtime.Precision)
	if err := tx.UpdateCampaign(ctx, campaign, CampaignChange{Configuration: &next, UpdatedAt: next.UpdatedAt}); err != nil {
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
	tx Store,
	campaignID string,
	requestedLayoutID *string,
) (string, map[string]CampaignLayoutReference, error) {

	layoutID, err := tx.CampaignLayoutID(ctx, campaignID)
	if errors.Is(err, ErrCampaignNotFound) {
		return "", nil, errCampaignNotFound()
	}
	if err != nil {
		return "", nil, err
	}

	observedLayoutID := strings.TrimSpace(ptrStringValue(layoutID))
	if observedLayoutID == "" && strings.TrimSpace(ptrStringValue(requestedLayoutID)) == "" {
		return observedLayoutID, map[string]CampaignLayoutReference{}, nil
	}
	lockedLayouts, err := tx.LockLayouts(ctx, observedLayoutID, ptrStringValue(requestedLayoutID))
	return observedLayoutID, lockedLayouts, err
}
