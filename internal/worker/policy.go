package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/account"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/email"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"gorm.io/gorm"
)

func (h *Handlers) handleActivateTerms(ctx context.Context) error {
	return h.handleActivateLegal(ctx, legalActivationSpec{
		model: &model.Terms{}, resource: "terms",
		scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
		referenceType:   legaldomain.EmailDeliveryReferenceTypeTerms,
		effectiveEvent:  email.EventTermsEffective.String(),
		logMessage:      "Activated pending terms",
	})
}

func (h *Handlers) handleActivatePrivacy(ctx context.Context) error {
	return h.handleActivateLegal(ctx, legalActivationSpec{
		model: &model.Privacy{}, resource: "privacy",
		scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
		referenceType:   legaldomain.EmailDeliveryReferenceTypePrivacy,
		effectiveEvent:  email.EventPrivacyEffective.String(),
		logMessage:      "Activated pending privacy policies",
	})
}

type legalActivationSpec struct {
	model           structured.Value
	resource        string
	scheduledStatus string
	referenceType   string
	effectiveEvent  string
	logMessage      string
}

type scheduledLegalDocument struct {
	ID string `gorm:"column:id"`
}

func (h *Handlers) handleActivateLegal(ctx context.Context, spec legalActivationSpec) error {
	var pending []scheduledLegalDocument
	if err := h.db.WithContext(ctx).
		Model(spec.model).
		Select("id").
		Where("status = ?", spec.scheduledStatus).
		Order("effective_from ASC, version ASC, id ASC").
		Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if len(pending) > 1 {
		return fmt.Errorf("multiple scheduled %s versions violate the activation invariant", spec.resource)
	}

	now := time.Now().UTC()
	documentID := pending[0].ID
	legalRuntime := legaladapter.NewRuntime()
	noticeRuntime := legaladapter.NewNoticeRuntime(nil)
	activated := false
	var effectiveRun *model.CampaignDeliveryRun
	if err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		activated, effectiveRun, err = legaldomain.ActivateAuditedLegalNoticeDocumentWithEffectiveRunWithDB(
			ctx,
			tx,
			h.auditWriter,
			legalRuntime,
			noticeRuntime,
			spec.referenceType,
			documentID,
			legaldomain.LegalNoticeActivationScheduledDue,
			spec.effectiveEvent,
			map[string]string{spec.resource + "_url": fmt.Sprintf("%s/%s", h.config.SiteOrigin, spec.resource)},
			now,
		)
		if err != nil || !activated {
			return err
		}
		_, err = legaladapter.RequestSaved(
			ctx,
			tx,
			h.ogPlanner,
			spec.resource,
			documentID,
			"",
			true,
			spec.resource+"_scheduled_activated",
		)
		return err
	}); err != nil {
		return err
	}

	activatedCount := 0
	if activated {
		activatedCount = 1
		if h.publisher != nil && h.spicedbClient != nil && h.auditWriter != nil {
			noticeRuntime = legaladapter.NewNoticeRuntime(h.newCampaignDeliveryDispatcher())
		}
		legaldomain.DispatchCommittedLegalEffectiveNoticeAfterActivation(
			ctx,
			h.db,
			noticeRuntime,
			spec.referenceType,
			documentID,
			now,
			effectiveRun,
		)
	}
	slog.Info(spec.logMessage, "count", activatedCount)
	return nil
}

func (h *Handlers) handleUnbanExpired(ctx context.Context) error {
	return h.handleUnbanExpiredWith(ctx, func(
		mutationCtx context.Context,
		userID string,
		now time.Time,
	) (bool, error) {
		return account.ClearExpiredTimedBan(mutationCtx, h.db, h.kratosClient, userID, now, h.auditWriter)
	})
}

func (h *Handlers) handleUnbanExpiredWith(
	ctx context.Context,
	clearExpiredTimedBan func(context.Context, string, time.Time) (bool, error),
) error {
	// Iterate through all Kratos identities to find expired bans.
	// User data (including ban status) is stored in Kratos, not in a local DB table.
	now := time.Now().UTC()
	var unbannedCount int

	page := 0
	perPage := 100
	for {
		identities, _, err := h.kratosClient.ListIdentities(ctx, page, perPage)
		if err != nil {
			return fmt.Errorf("failed to list identities: %w", err)
		}

		for _, identity := range identities {
			if identity.MetadataAdmin == nil {
				continue
			}
			banned, ok := identity.MetadataAdmin["banned"].(bool)
			if !ok || !banned {
				continue
			}
			expiresStr, ok := identity.MetadataAdmin["ban_expires"].(string)
			if !ok || expiresStr == "" {
				continue // permanent ban, no expiry
			}
			expires, err := time.Parse(time.RFC3339, expiresStr)
			if err != nil {
				slog.Warn("invalid ban_expires format", "user_id", identity.ID, "value", expiresStr)
				continue
			}
			if expires.After(now) {
				continue // not yet expired
			}

			cleared, err := clearExpiredTimedBan(ctx, identity.ID, now)
			if err != nil {
				slog.Warn("failed to clear expired ban", "user_id", identity.ID, "error", err)
				continue
			}
			if cleared {
				unbannedCount++
			}
		}

		if len(identities) < perPage {
			break
		}
		page++
	}

	if unbannedCount > 0 {
		slog.Info("Unbanned expired users", "count", unbannedCount)
	}
	return nil
}
