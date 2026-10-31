package member

import (
	"context"

	"github.com/echovisionlab/geul-api/internal/email"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// UserAvatarAssetPromoter is the exact File capability required for Member
// avatar projection; File storage remains outside this domain.
type UserAvatarAssetPromoter interface {
	PromoteUserAvatarAsset(context.Context, string) (*commonv1.AssetRef, error)
}

// EmailCommandPublisher is the Member-owned welcome command boundary. The
// welcome command is enqueued in the onboarding transaction, so a committed
// onboarding always carries its command.
type EmailCommandPublisher interface {
	email.CommandPublisher
	PublishSendEmailWithExecutor(context.Context, eventpkg.DBTX, *managev1.SendEmailEvent) error
}
