package campaign

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/dbtime"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ScheduleCampaign schedules a campaign to be sent
func (s *CampaignService) ScheduleCampaign(
	ctx context.Context,
	req *connect.Request[managev1.ScheduleCampaignRequest],
) (*connect.Response[managev1.ScheduleCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	var campaign model.Campaign
	var scheduledAt time.Time

	// Use transaction with row-level locking to prevent race conditions
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		observedLayoutID, _, err := observeAndLockCampaignLayouts(
			ctx,
			tx,
			s.emailAuthoring,
			req.Msg.Id,
			nil,
		)
		if err != nil {
			return err
		}
		// Lock the row for update to prevent concurrent modifications
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if strings.TrimSpace(ptrStringValue(campaign.LayoutID)) != observedLayoutID {
			return errs.FailedPrecondition("campaign layout assignment changed; retry")
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}
		if req.Msg.ScheduledAt == nil {
			return errs.Required("scheduled_at")
		}
		scheduledAt = req.Msg.ScheduledAt.AsTime().UTC().Truncate(dbtime.Precision)
		if scheduledAt.Before(time.Now()) {
			return errs.InvalidArgument("scheduled_at", "must be in the future")
		}

		if !campaignStatusAllowsSchedule(campaign.Status) {
			return errs.FailedPrecondition(errs.MsgCampaignOnlyScheduleDraft)
		}

		if err := requireCurrentCampaignRenderableContent(ctx, tx, campaign.ID); err != nil {
			return err
		}
		recipientScope, err := campaignRecipientScopeFromProto(req.Msg.GetRecipientScope())
		if err != nil {
			return errs.InvalidArgument("recipient_scope", err.Error())
		}
		if campaign.RecipientScope != recipientScope {
			return errs.FailedPrecondition("campaign recipient scope changed; reload before scheduling")
		}
		if _, err := deriveCampaignDeliveryTarget(ctx, tx, campaign, s.audienceTargets); err != nil {
			return err
		}

		campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
		campaign.ScheduledAt = &scheduledAt

		if err := tx.Save(&campaign).Error; err != nil {
			return errs.Internal(err)
		}

		_, err = createCampaignDeliveryRun(
			ctx,
			tx,
			campaign,
			scheduledAt,
			0,
			s.audienceTargets,
			s.contentBlocks,
			s.emailAuthoring,
			s.emailDelivery,
		)
		if err != nil {
			return err
		}
		return s.appendCampaignScheduleAudit(ctx, tx, campaign.ID, sharedtelemetry.AuditStateDraft, sharedtelemetry.AuditStateScheduled, scheduledAt)
	})

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.ScheduleCampaignResponse{
		Id: campaign.ID, Changed: true, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED,
		ScheduledAt: timestamppb.New(*campaign.ScheduledAt), RecipientScope: req.Msg.GetRecipientScope(),
		UpdatedAt: timestamppb.New(campaign.UpdatedAt),
	}), nil
}

// CancelCampaign cancels a scheduled campaign
func (s *CampaignService) CancelCampaign(
	ctx context.Context,
	req *connect.Request[managev1.CancelCampaignRequest],
) (*connect.Response[managev1.CancelCampaignResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var campaign model.Campaign

	// Use transaction with row-level locking to prevent race conditions
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Lock the row for update to prevent concurrent modifications
		if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
			First(&campaign, "id = ?", req.Msg.Id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errs.NotFoundMsg("campaign not found")
			}
			return errs.Internal(err)
		}
		if err := identitystate.RequireFreshAdminCan(ctx, tx, s.spiceDB, campaignCan); err != nil {
			return err
		}

		// Can only cancel scheduled campaigns
		if campaign.Status != managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String() {
			return errs.FailedPrecondition(errs.MsgCampaignOnlyCancelScheduled)
		}

		campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String()
		campaign.ScheduledAt = nil

		if err := tx.Save(&campaign).Error; err != nil {
			return errs.Internal(err)
		}

		if s.emailDelivery == nil {
			return errs.DependencyUnavailable("Email Delivery")
		}
		if err := s.emailDelivery.CancelActiveRuns(ctx, tx, campaign.ID, time.Now().UTC()); err != nil {
			return err
		}
		return s.appendCampaignStatusAudit(ctx, tx, campaign.ID, sharedtelemetry.AuditStateScheduled, sharedtelemetry.AuditStateDraft)
	})

	if err != nil {
		return nil, err
	}

	return connect.NewResponse(&managev1.CancelCampaignResponse{
		Id: campaign.ID, Changed: true, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT,
		UpdatedAt: timestamppb.New(campaign.UpdatedAt),
	}), nil
}

// SendCampaignNow immediately sends a campaign
func (s *CampaignService) SendCampaignNow(
	ctx context.Context,
	req *connect.Request[managev1.SendCampaignNowRequest],
) (*connect.Response[managev1.SendCampaignNowResponse], error) {
	campaignCan, err := policyv1.Campaign.Manage(req.Msg.Id)
	if err != nil {
		return nil, errs.InvalidArgument("id", err.Error())
	}

	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var result campaignSendNowResult
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		prepared, err := prepareCampaignSendNowWithDB(
			ctx,
			tx,
			s.spiceDB,
			campaignCan,
			s.audienceTargets,
			s.contentBlocks,
			s.emailAuthoring,
			s.emailDelivery,
			req.Msg,
		)
		result = prepared
		if err != nil {
			return err
		}
		if err := s.appendCampaignDeliveryRunAudit(ctx, tx, prepared.campaign.ID, campaignAuditState(prepared.previousStatus), sharedtelemetry.AuditStateSending, prepared.run.ID); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return connect.NewResponse(&managev1.SendCampaignNowResponse{
		Id: result.campaign.ID, Status: managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING,
		RecipientCount: int32(result.recipientCount), UpdatedAt: timestamppb.New(result.campaign.UpdatedAt),
	}), nil
}

type campaignSendNowResult struct {
	campaign       model.Campaign
	recipientCount int64
	run            CampaignDeliveryRunRef
	previousStatus string
}

func prepareCampaignSendNowWithDB(
	ctx context.Context,
	tx *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	campaignCan policyv1.Can,
	audienceTargets CampaignAudienceTargetPort,
	contentBlocks *contentblock.Store,
	emailAuthoring CampaignEmailAuthoringPort,
	emailDelivery CampaignDeliveryPort,
	request *managev1.SendCampaignNowRequest,
) (campaignSendNowResult, error) {
	observedLayoutID, _, err := observeAndLockCampaignLayouts(
		ctx,
		tx,
		emailAuthoring,
		request.Id,
		nil,
	)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	campaign, err := lockCampaignForSendNow(ctx, tx, request.Id)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	if strings.TrimSpace(ptrStringValue(campaign.LayoutID)) != observedLayoutID {
		return campaignSendNowResult{}, errs.FailedPrecondition("campaign layout assignment changed; retry")
	}
	if err := identitystate.RequireFreshAdminCan(ctx, tx, spiceDB, campaignCan); err != nil {
		return campaignSendNowResult{}, err
	}
	recipientScope, err := campaignRecipientScopeFromProto(request.GetRecipientScope())
	if err != nil {
		return campaignSendNowResult{}, errs.InvalidArgument("recipient_scope", err.Error())
	}
	previousStatus := campaign.Status
	if campaign.RecipientScope != recipientScope {
		return campaignSendNowResult{}, errs.FailedPrecondition("campaign recipient scope changed; reload before sending")
	}
	recipientCount, err := countCampaignSendNowRecipients(
		ctx,
		tx,
		audienceTargets,
		emailDelivery,
		campaign,
	)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	if recipientCount == 0 {
		return campaignSendNowResult{}, errs.FailedPrecondition(errs.MsgCampaignNoRecipients)
	}
	run, err := startCampaignSendNowWithDB(
		ctx,
		tx,
		audienceTargets,
		contentBlocks,
		emailAuthoring,
		emailDelivery,
		&campaign,
		recipientCount,
		time.Now(),
	)
	if err != nil {
		return campaignSendNowResult{}, err
	}
	return campaignSendNowResult{campaign: campaign, recipientCount: recipientCount, run: run, previousStatus: previousStatus}, nil
}

func lockCampaignForSendNow(ctx context.Context, tx *gorm.DB, campaignID string) (model.Campaign, error) {
	var campaign model.Campaign
	if err := tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&campaign, "id = ?", campaignID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.Campaign{}, errs.NotFoundMsg("campaign not found")
		}
		return model.Campaign{}, errs.Internal(err)
	}
	if !campaignStatusAllowsSendNow(campaign.Status) {
		return model.Campaign{}, errs.FailedPrecondition(errs.MsgCampaignAlreadySent)
	}
	if err := requireCurrentCampaignRenderableContent(ctx, tx, campaign.ID); err != nil {
		return model.Campaign{}, err
	}
	return campaign, nil
}

func requireCurrentCampaignRenderableContent(ctx context.Context, db *gorm.DB, campaignID string) error {
	var count int64
	if err := db.WithContext(ctx).
		Table("campaign_translation AS translation").
		Joins("JOIN campaign AS source ON source.id = translation.entity_id AND source.source_locale = translation.locale").
		Where("translation.entity_id = ?", campaignID).
		Where("translation.content_html IS NOT NULL AND btrim(translation.content_html) <> ''").
		Count(&count).Error; err != nil {
		return errs.Internal(err)
	}
	if count != 1 {
		return errs.FailedPrecondition(errs.MsgCampaignNeedsContent)
	}
	return nil
}

func countCampaignSendNowRecipients(
	ctx context.Context,
	tx *gorm.DB,
	audienceTargets CampaignAudienceTargetPort,
	emailDelivery CampaignDeliveryPort,
	campaign model.Campaign,
) (int64, error) {
	target, err := deriveCampaignDeliveryTarget(ctx, tx, campaign, audienceTargets)
	if err != nil {
		return 0, err
	}
	if emailDelivery == nil {
		return 0, errs.DependencyUnavailable("Email Delivery")
	}
	return emailDelivery.CountRecipients(ctx, tx, target)
}

func startCampaignSendNowWithDB(
	ctx context.Context,
	tx *gorm.DB,
	audienceTargets CampaignAudienceTargetPort,
	contentBlocks *contentblock.Store,
	emailAuthoring CampaignEmailAuthoringPort,
	emailDelivery CampaignDeliveryPort,
	campaign *model.Campaign,
	recipientCount int64,
	now time.Time,
) (CampaignDeliveryRunRef, error) {
	if emailDelivery == nil {
		return CampaignDeliveryRunRef{}, errs.DependencyUnavailable("Email Delivery")
	}
	campaign.Status = managev1.CampaignStatus_CAMPAIGN_STATUS_SENDING.String()
	campaign.ScheduledAt = nil
	campaign.SentAt = nil
	campaign.SentCount = 0
	if err := tx.Save(campaign).Error; err != nil {
		return CampaignDeliveryRunRef{}, errs.Internal(err)
	}
	if err := emailDelivery.CancelActiveRuns(ctx, tx, campaign.ID, now.UTC()); err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	run, err := createCampaignDeliveryRun(
		ctx,
		tx,
		*campaign,
		now,
		recipientCount,
		audienceTargets,
		contentBlocks,
		emailAuthoring,
		emailDelivery,
	)
	if err != nil {
		return CampaignDeliveryRunRef{}, err
	}
	return run, nil
}
