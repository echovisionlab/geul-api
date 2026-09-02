package account

import (
	"context"
	"fmt"
	"strings"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	"gorm.io/gorm"
)

// revokeOtherSessions keeps the authenticated session usable while revoking
// every other active session for the same Identity. A blank currentSessionID
// is reserved for callers that intentionally revoke the whole Identity.
func revokeOtherSessions(
	ctx context.Context,
	db *gorm.DB,
	revoker interface {
		auth.SessionRevoker
		auth.IdentitySessionRevoker
	},
	identityID string,
	currentSessionID string,
) ([]string, error) {
	identityID = strings.TrimSpace(identityID)
	currentSessionID = strings.TrimSpace(currentSessionID)
	if db == nil || revoker == nil || identityID == "" {
		return nil, fmt.Errorf("session revocation requires database, revoker, and identity")
	}
	if currentSessionID == "" {
		return nil, revoker.DeleteIdentitySessions(ctx, identityID)
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return nil, err
	}
	if _, err := uuidutil.ParseCanonical(currentSessionID, "session_id"); err != nil {
		return nil, err
	}

	var sessionIDs []string
	if err := db.WithContext(ctx).Raw(`
		SELECT id::text
		FROM kratos.sessions
		WHERE identity_id = ?::uuid
		  AND id <> ?::uuid
		  AND active = TRUE
		  AND expires_at > NOW()
		ORDER BY id
	`, identityID, currentSessionID).Scan(&sessionIDs).Error; err != nil {
		return nil, err
	}
	for _, sessionID := range sessionIDs {
		if err := revoker.DeleteSession(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	return sessionIDs, nil
}
