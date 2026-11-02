package worker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/mutation"
)

// legalActivation is what one scheduled legal activation job activates.
type legalActivation struct {
	kind           LegalKind
	effectiveEvent string
	logMessage     string
}

var (
	termsActivation = legalActivation{
		kind:           LegalTerms,
		effectiveEvent: email.EventTermsEffective.String(),
		logMessage:     "Activated pending terms",
	}
	privacyActivation = legalActivation{
		kind:           LegalPrivacy,
		effectiveEvent: email.EventPrivacyEffective.String(),
		logMessage:     "Activated pending privacy policies",
	}
)

// handleActivateLegal activates the kind's one scheduled version when it is
// due. Two scheduled versions break the activation invariant and fail the
// job before anything changes. The activation, its audit, the sealed
// effective notice run and the OG target switch commit together; the notice
// is dispatched after commit, so mail-provider availability is not part of
// the activation.
func (h *Handlers) handleActivateLegal(ctx context.Context, activation legalActivation) error {
	pending, err := h.legal.ScheduledVersions(ctx, activation.kind)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if len(pending) > 1 {
		return fmt.Errorf("multiple scheduled %s versions violate the activation invariant", activation.kind)
	}

	now := time.Now().UTC()
	documentID := pending[0]
	resource := string(activation.kind)
	templateData := map[string]string{resource + "_url": fmt.Sprintf("%s/%s", h.siteOrigin, resource)}
	activated := false
	var notice EffectiveNotice
	if err := h.units.Transaction(ctx, false, func(tx mutation.Tx[Jobs]) error {
		legal := tx.Store().LegalActivation()
		var err error
		notice, activated, err = legal.ActivateDue(ctx, activation.kind, documentID, activation.effectiveEvent, templateData, now)
		if err != nil || !activated {
			return err
		}
		return legal.RequestOG(ctx, activation.kind, documentID, resource+"_scheduled_activated")
	}); err != nil {
		return err
	}

	activatedCount := 0
	if activated {
		activatedCount = 1
		h.legal.DispatchEffectiveNotice(ctx, activation.kind, documentID, now, notice)
	}
	slog.InfoContext(ctx, activation.logMessage, "count", activatedCount)
	return nil
}

// handleUnbanExpired pages through every identity and clears each timed ban
// whose expiry passed. Ban state lives in the Kratos identity, not in a
// table. A failed clear is logged and the scan goes on; the next run retries
// it.
func (h *Handlers) handleUnbanExpired(ctx context.Context, now time.Time) error {
	var unbannedCount int

	page := 0
	perPage := 100
	for {
		identities, _, err := h.identities.ListIdentities(ctx, page, perPage)
		if err != nil {
			return fmt.Errorf("failed to list identities: %w", err)
		}

		for _, identity := range identities {
			if h.clearExpiredTimedBan(ctx, identity, now) {
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

func (h *Handlers) clearExpiredTimedBan(ctx context.Context, identity *auth.Identity, now time.Time) bool {
	if identity.MetadataAdmin == nil {
		return false
	}
	banned, ok := identity.MetadataAdmin["banned"].(bool)
	if !ok || !banned {
		return false
	}
	expiresStr, ok := identity.MetadataAdmin["ban_expires"].(string)
	if !ok || expiresStr == "" {
		return false // permanent ban, no expiry
	}
	expires, err := time.Parse(time.RFC3339, expiresStr)
	if err != nil {
		slog.WarnContext(ctx, "Skipping timed ban with invalid ban_expires", "user_id", identity.ID, "failure_cause", failurecause.Message(err))
		return false
	}
	if expires.After(now) {
		return false // not yet expired
	}
	cleared, err := h.timedBans.ClearExpiredTimedBan(ctx, identity.ID, now)
	if err != nil {
		slog.ErrorContext(ctx, "Expired timed ban clear failed", append(failurecause.Args(err), "user_id", identity.ID)...)
		return false
	}
	return cleared
}
