package public

import (
	"context"
	"time"

	"github.com/echovisionlab/geul-api/internal/crypto"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
)

type legalShareLinkAccess struct {
	status        legaldomain.Status
	publicHistory bool
}

// resolveLegalShareLinkAccess reports which version state a Share Link of
// entityID opens: a published version as public history, or a version
// scheduled for later. An expired, unknown or wrongly protected link opens
// nothing.
func (s *Service) resolveLegalShareLinkAccess(
	ctx context.Context,
	kind publicKind,
	token string,
	password string,
	entityID string,
	now time.Time,
) (legalShareLinkAccess, bool, error) {
	var link ShareLink
	var linked bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		link, linked, err = store.ShareLink(ctx, token, kind.shareLinkEntityType, entityID, now)
		return err
	}); err != nil || !linked {
		return legalShareLinkAccess{}, false, err
	}
	opened, err := shareLinkOpens(link, password)
	if err != nil || !opened {
		return legalShareLinkAccess{}, false, err
	}
	var target legaldomain.Lifecycle
	var found bool
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		target, found, err = store.Lifecycle(ctx, kind.kind, entityID)
		return err
	}); err != nil || !found {
		return legalShareLinkAccess{}, false, err
	}
	if target.Status == kind.active || target.Status == kind.archived {
		return legalShareLinkAccess{status: target.Status, publicHistory: true}, true, nil
	}
	if target.Status != kind.scheduled || target.EffectiveFrom == nil || !target.EffectiveFrom.After(now) {
		return legalShareLinkAccess{}, false, nil
	}
	return legalShareLinkAccess{status: target.Status}, true, nil
}

// shareLinkOpens reports whether link, an unexpired Share Link of the
// version, opens for password: a link without a password always does.
func shareLinkOpens(link ShareLink, password string) (bool, error) {
	if link.ExpiresAt == nil {
		return false, nil
	}
	if link.PasswordHash == nil {
		return true, nil
	}
	if password == "" {
		return false, nil
	}
	return crypto.NewPasswordHasher(nil).Verify(password, *link.PasswordHash)
}
