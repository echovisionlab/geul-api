package campaign

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/emailcontent"
)

func loadCampaignAIDocumentRoot(
	ctx context.Context,
	tx Store,
	campaignID string,
	lock emailcontent.RowLock,
) (Campaign, uuid.UUID, error) {

	root, err := tx.CampaignAIDocumentRoot(ctx, campaignID, lock)
	if errors.Is(err, ErrCampaignNotFound) {
		return Campaign{}, uuid.Nil, domainerr.ResourceNotFound("campaign", campaignID)
	}
	if err != nil {
		return Campaign{}, uuid.Nil, err
	}

	if root.ContentDocumentID == nil {
		return Campaign{}, uuid.Nil, domainerr.Precondition("Campaign content document is not initialized")
	}
	documentID, err := uuid.Parse(strings.TrimSpace(*root.ContentDocumentID))
	if err != nil || documentID == uuid.Nil {
		return Campaign{}, uuid.Nil, fmt.Errorf("Campaign content document ID %q is invalid", *root.ContentDocumentID)
	}
	return root, documentID, nil
}
