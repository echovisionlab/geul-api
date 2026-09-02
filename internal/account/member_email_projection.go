package account

import (
	"context"

	"gorm.io/gorm"
)

// MemberEmailProjection is the Member-owned email projection Account needs
// after it has derived current, usable candidates from Kratos credentials.
// Account owns candidate calculation and command ordering; Member owns the
// exact bilateral-link persistence and projection reads.
type MemberEmailProjection interface {
	PrimaryEmail(context.Context, *gorm.DB, string, string) (string, error)
	SyncEmailProjection(context.Context, *gorm.DB, string, string, string, []string) error
}
