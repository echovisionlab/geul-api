package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/uuidutil"
)

// revokeOtherSessions keeps the authenticated session usable while revoking
// every other active session for the same Identity. A blank currentSessionID
// is reserved for callers that intentionally revoke the whole Identity.
func (s *Service) revokeOtherSessions(ctx context.Context, identityID, currentSessionID string) ([]string, error) {
	identityID = strings.TrimSpace(identityID)
	currentSessionID = strings.TrimSpace(currentSessionID)
	if identityID == "" {
		return nil, fmt.Errorf("session revocation requires database, revoker, and identity")
	}
	if currentSessionID == "" {
		return nil, s.identity.DeleteIdentitySessions(ctx, identityID)
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return nil, err
	}
	if _, err := uuidutil.ParseCanonical(currentSessionID, "session_id"); err != nil {
		return nil, err
	}

	var sessionIDs []string
	if err := s.read(ctx, func(store Store) error {
		var err error
		sessionIDs, err = store.OtherLiveSessionIDs(ctx, identityID, currentSessionID)
		return err
	}); err != nil {
		return nil, err
	}
	for _, sessionID := range sessionIDs {
		if err := s.identity.DeleteSession(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	return sessionIDs, nil
}
