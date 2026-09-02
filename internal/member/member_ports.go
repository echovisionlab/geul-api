package member

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/email"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
)

// FileDeleter is the Member-owned minimal avatar cleanup dependency.
type FileDeleter interface {
	DeleteFileByID(context.Context, string) error
}

// UserAvatarAssetPromoter is the exact File capability required for Member
// avatar projection; File storage remains outside this domain.
type UserAvatarAssetPromoter interface {
	PromoteUserAvatarAsset(context.Context, string) (*commonv1.AssetRef, error)
}

// EmailCommandPublisher is the Member-owned welcome and newsletter command boundary.
type EmailCommandPublisher = email.CommandPublisher
