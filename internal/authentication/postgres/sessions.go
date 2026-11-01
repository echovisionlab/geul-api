package authenticationpostgres

import (
	"context"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authentication"
	"github.com/echovisionlab/geul-api/internal/identityread"
)

// NewSessions returns the Kratos session reads over db, one statement each
// outside any transaction.
func NewSessions(db *gorm.DB) authentication.Sessions {
	return sessions{db: db}
}

type sessions struct {
	db *gorm.DB
}

func (s sessions) Principal(ctx context.Context, sessionID string) (*auth.UserInfo, error) {
	return auth.ResolveAuthenticatedPrincipalBySessionID(ctx, s.db, sessionID)
}

func (s sessions) FirstSession(ctx context.Context, sessionID string) (bool, error) {
	return identityread.FirstSession(ctx, s.db, sessionID)
}
