package campaign

import (
	"context"
	"errors"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

// RequireTranslationSourceMutable owns the Campaign root lock and the exact
// lifecycle fences that must hold before Translation mutates Campaign source
// state.
func RequireTranslationSourceMutable(
	ctx context.Context,
	joined Joined,
	campaignID string,
) error {
	campaignID = strings.TrimSpace(campaignID)

	campaign, err := joined.Store.LockCampaign(ctx, campaignID)
	if errors.Is(err, ErrCampaignNotFound) {
		return domainerr.ResourceNotFound("campaign", campaignID)
	}
	if err != nil {
		return err
	}

	if !campaignStatusAllowsEdit(campaign.Status) {
		return domainerr.Precondition(msgCampaignCannotUpdateSent)
	}

	activeRunCount, err := joined.Store.ActiveCampaignRunCount(ctx, campaignID)
	if err != nil {
		return err
	}

	if activeRunCount > 0 {
		return domainerr.Precondition(
			"campaign is frozen by an active delivery run",
		)
	}
	return nil
}
