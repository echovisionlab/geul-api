package campaign

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/dbtime"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ScheduleCampaignCommand schedules a draft Campaign. RecipientScope must
// equal the Campaign's stored scope, so a stale editor cannot schedule a
// different audience.
type ScheduleCampaignCommand struct {
	ID             string
	ScheduledAt    *time.Time
	RecipientScope managev1.CampaignRecipientScope
}

// CampaignLifecycle is the lifecycle state a schedule, cancel or send left.
type CampaignLifecycle struct {
	ID             string
	Status         model.CampaignStatus
	ScheduledAt    *time.Time
	RecipientCount int64
	UpdatedAt      time.Time
	record         mutation.Audit
}

// lockCampaignForDelivery locks the Campaign's layout and then the Campaign,
// and rejects a layout assignment that moved in between.
func (s *Service) lockCampaignForDelivery(ctx context.Context, tx *gorm.DB, campaignID string, campaign *model.Campaign, lock func(context.Context, *gorm.DB, string, *model.Campaign) error) error {
	observedLayoutID, _, err := observeAndLockCampaignLayouts(ctx, tx, s.emailAuthoring, campaignID, nil)
	if err != nil {
		return err
	}
	if err := lock(ctx, tx, campaignID, campaign); err != nil {
		return err
	}
	if strings.TrimSpace(ptrStringValue(campaign.LayoutID)) != observedLayoutID {
		return domainerr.Precondition("campaign layout assignment changed; retry")
	}
	return nil
}

// runCampaignLifecycle runs a lifecycle change of one Campaign under access,
// after lock locked it.
func (s *Service) runCampaignLifecycle(
	ctx context.Context,
	access mutation.Access,
	lock func(ctx context.Context, tx *gorm.DB) error,
	change func(ctx context.Context, tx *gorm.DB) (CampaignLifecycle, error),
) (CampaignLifecycle, error) {
	return mutation.Run(ctx, s.mutations, mutation.Spec[CampaignLifecycle]{
		Lock:   lock,
		Access: access,
		Change: change,
		Audit:  func(lifecycle CampaignLifecycle) mutation.Audit { return lifecycle.record },
	})
}

// ScheduleCampaign seals the Campaign's delivery run for the scheduled time.
func (s *Service) ScheduleCampaign(ctx context.Context, command ScheduleCampaignCommand) (CampaignLifecycle, error) {
	access, err := adminAccess(policyv1.Campaign.Manage(command.ID))
	if err != nil {
		return CampaignLifecycle{}, err
	}
	var campaign model.Campaign
	return s.runCampaignLifecycle(ctx, access, func(ctx context.Context, tx *gorm.DB) error {
		return s.lockCampaignForDelivery(ctx, tx, command.ID, &campaign, lockCampaign)
	}, func(ctx context.Context, tx *gorm.DB) (CampaignLifecycle, error) {
		scheduledAt, err := s.requireSchedulable(ctx, tx, campaign, command)
		if err != nil {
			return CampaignLifecycle{}, err
		}
		campaign.Status = string(model.CampaignStatusScheduled)
		campaign.ScheduledAt = &scheduledAt
		if err := tx.WithContext(ctx).Save(&campaign).Error; err != nil {
			return CampaignLifecycle{}, err
		}
		if _, err := s.createDeliveryRun(ctx, tx, campaign, scheduledAt, 0); err != nil {
			return CampaignLifecycle{}, err
		}
		return CampaignLifecycle{
			ID: campaign.ID, Status: model.CampaignStatusScheduled, ScheduledAt: campaign.ScheduledAt, UpdatedAt: campaign.UpdatedAt,
			record: mutation.RequestAudit(sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewCampaignScheduleLifecycleAuditRecord(metadata, campaign.ID, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStateScheduled, scheduledAt)
			}),
		}, nil
	})
}

// requireSchedulable checks the requested time and scope against the locked
// draft and its current content and audience, and returns the scheduled
// time at storage precision.
func (s *Service) requireSchedulable(ctx context.Context, tx *gorm.DB, campaign model.Campaign, command ScheduleCampaignCommand) (time.Time, error) {
	if command.ScheduledAt == nil {
		return time.Time{}, domainerr.Required("scheduled_at")
	}
	scheduledAt := command.ScheduledAt.UTC().Truncate(dbtime.Precision)
	if scheduledAt.Before(time.Now()) {
		return time.Time{}, domainerr.InvalidField("scheduled_at", "must be in the future")
	}
	if !campaignStatusAllowsSchedule(campaign.Status) {
		return time.Time{}, domainerr.Precondition(msgCampaignOnlyScheduleDraft)
	}
	if err := requireCurrentCampaignRenderableContent(ctx, tx, campaign.ID); err != nil {
		return time.Time{}, err
	}
	recipientScope, err := campaignRecipientScopeFromProto(command.RecipientScope)
	if err != nil {
		return time.Time{}, domainerr.InvalidField("recipient_scope", err.Error())
	}
	if campaign.RecipientScope != recipientScope {
		return time.Time{}, domainerr.Precondition("campaign recipient scope changed; reload before scheduling")
	}
	_, err = deriveCampaignDeliveryTarget(ctx, tx, campaign, s.audienceTargets)
	return scheduledAt, err
}

// CancelCampaignCommand returns a scheduled Campaign to draft.
type CancelCampaignCommand struct {
	ID string
}

// CancelCampaign returns the scheduled Campaign to draft and cancels its
// active delivery runs.
func (s *Service) CancelCampaign(ctx context.Context, command CancelCampaignCommand) (CampaignLifecycle, error) {
	access, err := adminAccess(policyv1.Campaign.Manage(command.ID))
	if err != nil {
		return CampaignLifecycle{}, err
	}
	if command.ID == "" {
		return CampaignLifecycle{}, domainerr.Required("id")
	}
	var campaign model.Campaign
	return s.runCampaignLifecycle(ctx, access, func(ctx context.Context, tx *gorm.DB) error {
		return lockCampaign(ctx, tx, command.ID, &campaign)
	}, func(ctx context.Context, tx *gorm.DB) (CampaignLifecycle, error) {
		if model.CampaignStatus(campaign.Status) != model.CampaignStatusScheduled {
			return CampaignLifecycle{}, domainerr.Precondition(msgCampaignOnlyCancelScheduled)
		}
		campaign.Status = string(model.CampaignStatusDraft)
		campaign.ScheduledAt = nil
		if err := tx.WithContext(ctx).Save(&campaign).Error; err != nil {
			return CampaignLifecycle{}, err
		}
		if err := s.emailDelivery.CancelActiveRuns(ctx, tx, campaign.ID, time.Now().UTC()); err != nil {
			return CampaignLifecycle{}, err
		}
		return CampaignLifecycle{
			ID: campaign.ID, Status: model.CampaignStatusDraft, UpdatedAt: campaign.UpdatedAt,
			record: campaignStatusAudit(campaign.ID, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft),
		}, nil
	})
}

func campaignStatusAudit(campaignID string, previous, next sharedtelemetry.AuditState) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewCampaignStatusLifecycleAuditRecord(metadata, campaignID, previous, next)
	})
}

// SendCampaignNowCommand starts a draft Campaign's delivery now. As for a
// schedule, RecipientScope must equal the stored scope.
type SendCampaignNowCommand struct {
	ID             string
	RecipientScope managev1.CampaignRecipientScope
}

// SendCampaignNow seals and starts the draft Campaign's delivery run for its
// current recipients, cancelling any other active run.
func (s *Service) SendCampaignNow(ctx context.Context, command SendCampaignNowCommand) (CampaignLifecycle, error) {
	access, err := adminAccess(policyv1.Campaign.Manage(command.ID))
	if err != nil {
		return CampaignLifecycle{}, err
	}
	if command.ID == "" {
		return CampaignLifecycle{}, domainerr.Required("id")
	}
	var campaign model.Campaign
	return s.runCampaignLifecycle(ctx, access, func(ctx context.Context, tx *gorm.DB) error {
		return s.lockCampaignForDelivery(ctx, tx, command.ID, &campaign, lockCampaignForSendNow)
	}, func(ctx context.Context, tx *gorm.DB) (CampaignLifecycle, error) {
		recipientScope, err := campaignRecipientScopeFromProto(command.RecipientScope)
		if err != nil {
			return CampaignLifecycle{}, domainerr.InvalidField("recipient_scope", err.Error())
		}
		previousStatus := campaign.Status
		if campaign.RecipientScope != recipientScope {
			return CampaignLifecycle{}, domainerr.Precondition("campaign recipient scope changed; reload before sending")
		}
		target, err := deriveCampaignDeliveryTarget(ctx, tx, campaign, s.audienceTargets)
		if err != nil {
			return CampaignLifecycle{}, err
		}
		recipientCount, err := s.emailDelivery.CountRecipients(ctx, tx, target)
		if err != nil {
			return CampaignLifecycle{}, err
		}
		if recipientCount == 0 {
			return CampaignLifecycle{}, domainerr.Precondition(msgCampaignNoRecipients)
		}
		run, err := s.startCampaignSendNow(ctx, tx, &campaign, recipientCount, time.Now())
		if err != nil {
			return CampaignLifecycle{}, err
		}
		return CampaignLifecycle{
			ID: campaign.ID, Status: model.CampaignStatusSending, RecipientCount: recipientCount, UpdatedAt: campaign.UpdatedAt,
			record: mutation.RequestAudit(sharedtelemetry.AuditCampaignUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewCampaignDeliveryRunLifecycleAuditRecord(metadata, campaign.ID, campaignAuditState(previousStatus), sharedtelemetry.AuditStateSending, run.ID)
			}),
		}, nil
	})
}

// lockCampaignForSendNow locks a draft Campaign with renderable source
// content.
func lockCampaignForSendNow(ctx context.Context, tx *gorm.DB, campaignID string, campaign *model.Campaign) error {
	if err := lockCampaign(ctx, tx, campaignID, campaign); err != nil {
		return err
	}
	if !campaignStatusAllowsSendNow(campaign.Status) {
		return domainerr.Precondition(msgCampaignAlreadySent)
	}
	return requireCurrentCampaignRenderableContent(ctx, tx, campaign.ID)
}

func requireCurrentCampaignRenderableContent(ctx context.Context, db *gorm.DB, campaignID string) error {
	var count int64
	if err := db.WithContext(ctx).
		Table("campaign_translation AS translation").
		Joins("JOIN campaign AS source ON source.id = translation.entity_id AND source.source_locale = translation.locale").
		Where("translation.entity_id = ?", campaignID).
		Where("translation.content_html IS NOT NULL AND btrim(translation.content_html) <> ''").
		Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return domainerr.Precondition(msgCampaignNeedsContent)
	}
	return nil
}

// startCampaignSendNow moves the Campaign to sending, cancels its other
// active runs and seals and starts the run for recipientCount recipients.
func (s *Service) startCampaignSendNow(ctx context.Context, tx *gorm.DB, campaign *model.Campaign, recipientCount int64, now time.Time) (CampaignDeliveryRunRef, error) {
	campaign.Status = string(model.CampaignStatusSending)
	campaign.ScheduledAt = nil
	campaign.SentAt = nil
	campaign.SentCount = 0
	if err := tx.WithContext(ctx).Save(campaign).Error; err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	if err := s.emailDelivery.CancelActiveRuns(ctx, tx, campaign.ID, now.UTC()); err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	return s.createDeliveryRun(ctx, tx, *campaign, now, recipientCount)
}

// createDeliveryRun seals the Campaign's delivery run.
func (s *Service) createDeliveryRun(ctx context.Context, tx *gorm.DB, campaign model.Campaign, scheduledAt time.Time, targetCount int64) (CampaignDeliveryRunRef, error) {
	return createCampaignDeliveryRun(ctx, tx, campaign, scheduledAt, targetCount, s.audienceTargets, s.contentBlocks, s.emailAuthoring, s.emailDelivery)
}
