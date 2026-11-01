package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	legalpostgres "github.com/echovisionlab/geul-api/internal/legal/postgres"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"gorm.io/gorm"
)

func (h *Handlers) handleActivateTerms(ctx context.Context) error {
	return h.handleActivateLegal(ctx, legalActivationSpec{
		model: &model.Terms{}, resource: "terms",
		scheduledStatus: model.TermsStatusScheduled,
		referenceType:   legaldomain.EmailDeliveryReferenceTypeTerms,
		effectiveEvent:  email.EventTermsEffective.String(),
		logMessage:      "Activated pending terms",
	})
}

func (h *Handlers) handleActivatePrivacy(ctx context.Context) error {
	return h.handleActivateLegal(ctx, legalActivationSpec{
		model: &model.Privacy{}, resource: "privacy",
		scheduledStatus: model.PrivacyStatusScheduled,
		referenceType:   legaldomain.EmailDeliveryReferenceTypePrivacy,
		effectiveEvent:  email.EventPrivacyEffective.String(),
		logMessage:      "Activated pending privacy policies",
	})
}

type legalActivationSpec struct {
	model           structured.Value
	resource        string
	scheduledStatus model.LegalPolicyStatus
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
	kind := legaldomain.PolicyKind(spec.referenceType)
	activated := false
	var effectiveRun *legaldomain.NoticeRun
	if err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		joined := legalpostgres.Bind(tx, legalpostgres.Ports{
			Routes: legaladapter.NewRuntime(), Notices: legaladapter.NewNoticeRuntime(),
		}, h.auditWriter)
		var err error
		activated, effectiveRun, err = legaldomain.ActivateDuePolicy(
			ctx,
			joined,
			kind,
			documentID,
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
		dispatch := legaladapter.NewNoticeDispatch(h.db, nil)
		if h.publisher != nil && h.spicedbClient != nil {
			dispatch = legaladapter.NewNoticeDispatch(h.db, h.newCampaignDeliveryDispatcher())
		}
		legaldomain.DispatchCommittedEffectiveNotice(ctx, dispatch, kind, documentID, now, effectiveRun)
	}
	slog.InfoContext(ctx, spec.logMessage, "count", activatedCount)
	return nil
}

func (h *Handlers) handleUnbanExpired(ctx context.Context) error {
	return h.handleUnbanExpiredWith(ctx, func(
		mutationCtx context.Context,
		userID string,
		now time.Time,
	) (bool, error) {
		return accountpostgres.ClearExpiredTimedBan(mutationCtx, h.db, h.kratosClient, userID, now, h.auditWriter)
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
				slog.WarnContext(ctx, "Skipping timed ban with invalid ban_expires", "user_id", identity.ID, "failure_cause", failurecause.Message(err))
				continue
			}
			if expires.After(now) {
				continue // not yet expired
			}

			cleared, err := clearExpiredTimedBan(ctx, identity.ID, now)
			if err != nil {
				slog.ErrorContext(ctx, "Expired timed ban clear failed", append(failurecause.Args(err), "user_id", identity.ID)...)
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
		slog.InfoContext(ctx, "Unbanned expired users", "count", unbannedCount)
	}
	return nil
}
