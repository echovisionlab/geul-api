package campaign

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/domainerr"
)

func sealCampaignDeliveryRun(
	ctx context.Context,
	tx Store,
	definition CampaignDeliveryRunDefinition,
) (CampaignDeliveryRunRef, error) {
	campaignID := strings.TrimSpace(definition.CampaignID)
	if campaignID == "" {
		return CampaignDeliveryRunRef{}, domainerr.Required("campaign_id")
	}
	if definition.SnapshotSchemaVersion != CampaignDeliverySnapshotSchemaVersion {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", "unsupported snapshot schema version")
	}
	if err := ValidateCampaignDeliverySnapshot(definition.Snapshot); err != nil {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	if err := ValidateCampaignDeliveryTarget(definition.Target); err != nil {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", err.Error())
	}
	renderSnapshot, err := campaignDeliverySnapshotJSONFields(definition.Snapshot)
	if err != nil {
		return CampaignDeliveryRunRef{}, domainerr.New(domainerr.Invalid, "", err.Error())
	}

	sourceCampaignUpdatedAt := definition.SourceCampaignUpdatedAt.UTC()
	run := DeliveryRun{
		RunKind:                 EmailDeliveryRunKindCampaign,
		CampaignID:              &campaignID,
		Status:                  CampaignDeliveryRunStatusScheduled,
		ScheduledAt:             definition.ScheduledAt.UTC(),
		TemplateData:            map[string]any{},
		RenderSnapshot:          renderSnapshot,
		SnapshotSchemaVersion:   definition.SnapshotSchemaVersion,
		SourceLayoutID:          definition.SourceLayoutID,
		AudienceSegmentID:       definition.AudienceSegmentID,
		SourceCampaignUpdatedAt: &sourceCampaignUpdatedAt,
		SourceLayoutUpdatedAt:   definition.SourceLayoutUpdatedAt,
		TargetQueryVersion:      definition.Target.QueryVersion,
		TargetMode:              definition.Target.Mode,
		TargetRecipientScope:    definition.Target.RecipientScope,
		TargetCreatedAfter:      definition.Target.CreatedAfter,
		TargetCreatedBefore:     definition.Target.CreatedBefore,
	}
	if err := tx.InsertSealedRun(
		ctx,
		&run,
		&definition.Target,
	); err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	return CampaignDeliveryRunRef{ID: run.ID}, nil
}
func startCampaignDeliveryRun(
	ctx context.Context,
	tx Store,
	runID string,
	targetCount int64,
	startedAt time.Time,
) error {
	if targetCount <= 0 {
		return domainerr.New(domainerr.Invalid, "", "target count must be positive")
	}

	run, err := tx.LockRun(ctx, strings.TrimSpace(runID))
	if err != nil {
		return err
	}

	if run.RunKind != EmailDeliveryRunKindCampaign || run.CampaignID == nil || !run.DefinitionSealed {
		return domainerr.Precondition("Campaign delivery run definition is invalid")
	}
	run.TargetCount = int(targetCount)
	return enqueueStartedCampaignDeliveryRun(ctx, tx, &run, startedAt.UTC())
}
func campaignDeliverySnapshotJSONFields(
	snapshot CampaignDeliverySnapshot,
) (map[string]any, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("encode Campaign delivery snapshot: %w", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, fmt.Errorf("decode Campaign delivery snapshot: %w", err)
	}
	return fields, nil
}
