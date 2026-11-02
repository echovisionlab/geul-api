package accountpostgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentsignal"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// MemberDeletionAudit is Account's callback for the terminal account.deleted
// audit, invoked by Member inside the transaction of its terminal step.
type MemberDeletionAudit func(ctx context.Context, tx *gorm.DB, memberID string) error

// MemberDeletion is the Member side of an account deletion, run in the
// transaction or on the connection Account passes (accountadapter).
type MemberDeletion interface {
	PrepareTombstone(ctx context.Context, db *gorm.DB, memberID, identityID, email string) (account.MemberDeletionSnapshot, error)
	FinalizeTombstone(ctx context.Context, tx *gorm.DB, snapshot account.MemberDeletionSnapshot, deletedAt time.Time, audit MemberDeletionAudit) error
	PrepareUnonboardedHardDelete(ctx context.Context, db *gorm.DB, memberID, identityID string) (account.MemberUnonboardedDeletionTarget, error)
	RevokeUnonboardedAuthorization(ctx context.Context, spicedb *auth.SpiceDBClient, target account.MemberUnonboardedDeletionTarget) error
	FinalizeUnonboardedHardDelete(ctx context.Context, tx *gorm.DB, target account.MemberUnonboardedDeletionTarget, audit MemberDeletionAudit) error
	CleanupAvatar(ctx context.Context, db *gorm.DB, memberID, avatarAssetID string) error
	NotificationSnapshot(ctx context.Context, db *gorm.DB, memberID, identityID string) (account.MemberDeletionNotification, error)
	CompletionEligible(ctx context.Context, db *gorm.DB, memberID string) (bool, error)
	AvatarAssetID(ctx context.Context, db *gorm.DB, memberID string) (string, error)
	ListExpiredUnonboarded(ctx context.Context, db *gorm.DB, cutoff time.Time, limit int, after *account.MemberUnonboardedCursor) ([]account.MemberUnonboardedCandidate, *account.MemberUnonboardedCursor, error)
	RecheckUnonboarded(ctx context.Context, tx *gorm.DB, memberID, identityID string, cutoff *time.Time) (bool, error)
	UnonboardedIdentity(ctx context.Context, db *gorm.DB, memberID string) (string, bool, error)
}

// MemberEmails is the Member email projection Account keeps in step with
// the proven Identity candidates. Both answer gorm.ErrRecordNotFound for a
// pair that is not an exact live link.
type MemberEmails interface {
	PrimaryEmail(ctx context.Context, db *gorm.DB, memberID, identityID string) (string, error)
	SyncEmailProjection(ctx context.Context, tx *gorm.DB, memberID, identityID, primaryEmail string, availableEmails []string) error
}

// Newsletter is the Member-owned newsletter subscription of an Identity.
type Newsletter interface {
	Mutate(ctx context.Context, tx *gorm.DB, identityID string, subscribed bool) (bool, error)
	State(ctx context.Context, db *gorm.DB, identityID string) (account.NewsletterState, error)
}

// UserDeletionIdentityDispatchPublisher is the narrow transactional seam
// used by the retention scheduler. It intentionally does not require avatar
// or email capabilities owned by the normal account-deletion flow.
type UserDeletionIdentityDispatchPublisher interface {
	PublishUserDeleteIdentityWithExecutor(context.Context, eventpkg.DBTX, *managev1.UserDeleteIdentityCommand) error
}

// UserDeletionDispatchPublisher publishes the deletion commands outside a
// transaction.
type UserDeletionDispatchPublisher interface {
	PublishUserDeleteIdentity(context.Context, *managev1.UserDeleteIdentityCommand) error
	PublishUserDeleteAvatar(context.Context, *managev1.UserDeleteAvatarCommand) error
}

// transactionalUserDeletionDispatchPublisher is implemented by the real PGMQ
// publisher. Both deletion commands must be inserted through the owning
// lifecycle transaction before the request row is removed; otherwise a broker
// failure can leave the identity command accepted while the avatar cleanup is
// lost.
type transactionalUserDeletionDispatchPublisher interface {
	UserDeletionIdentityDispatchPublisher
	PublishUserDeleteAvatarWithExecutor(context.Context, eventpkg.DBTX, *managev1.UserDeleteAvatarCommand) error
}

// Ports are the other domains an Account transaction reaches. NewUnits
// requires every field.
type Ports struct {
	Members      MemberDeletion
	MemberEmails MemberEmails
	Newsletter   Newsletter
	// Publisher enqueues the durable deletion commands in the transaction.
	Publisher UserDeletionIdentityDispatchPublisher
}

func (p Ports) validate() error {
	var missing []string
	for name, absent := range map[string]bool{
		"Members": p.Members == nil, "MemberEmails": p.MemberEmails == nil, "Newsletter": p.Newsletter == nil, "Publisher": p.Publisher == nil,
	} {
		if absent {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		return errors.New("account store ports are required: " + strings.Join(missing, ", "))
	}
	return nil
}

func (s store) MemberPrimaryEmail(ctx context.Context, memberID, identityID string) (string, error) {
	email, err := s.ports.MemberEmails.PrimaryEmail(ctx, s.tx, memberID, identityID)
	return email, linkNotFound(err)
}

func (s store) SyncMemberEmails(ctx context.Context, memberID, identityID, primaryEmail string, availableEmails []string) error {
	return linkNotFound(s.ports.MemberEmails.SyncEmailProjection(ctx, s.tx, memberID, identityID, primaryEmail, availableEmails))
}

func (s store) MutateNewsletter(ctx context.Context, identityID string, subscribed bool) (bool, error) {
	return s.ports.Newsletter.Mutate(ctx, s.tx, identityID, subscribed)
}

func (s store) NewsletterState(ctx context.Context, identityID string) (account.NewsletterState, error) {
	return s.ports.Newsletter.State(ctx, s.tx, identityID)
}

func (s store) PrepareMemberTombstone(ctx context.Context, memberID, identityID, primaryEmail string) (account.MemberDeletionSnapshot, error) {
	return s.ports.Members.PrepareTombstone(ctx, s.tx, memberID, identityID, primaryEmail)
}

// deletionAudit hands Member the domain's audit callback, which appends
// through the store of the same transaction.
func deletionAudit(audit account.MemberDeletionAudit) MemberDeletionAudit {
	if audit == nil {
		return nil
	}
	return func(ctx context.Context, _ *gorm.DB, memberID string) error { return audit(ctx, memberID) }
}

func (s store) FinalizeMemberTombstone(ctx context.Context, snapshot account.MemberDeletionSnapshot, deletedAt time.Time, audit account.MemberDeletionAudit) error {
	return s.ports.Members.FinalizeTombstone(ctx, s.tx, snapshot, deletedAt, deletionAudit(audit))
}

func (s store) PrepareUnonboardedHardDelete(ctx context.Context, memberID, identityID string) (account.MemberUnonboardedDeletionTarget, error) {
	return s.ports.Members.PrepareUnonboardedHardDelete(ctx, s.tx, memberID, identityID)
}

func (s store) FinalizeUnonboardedHardDelete(ctx context.Context, target account.MemberUnonboardedDeletionTarget, audit account.MemberDeletionAudit) error {
	return s.ports.Members.FinalizeUnonboardedHardDelete(ctx, s.tx, target, deletionAudit(audit))
}

func (s store) MemberDeletionNotification(ctx context.Context, memberID, identityID string) (account.MemberDeletionNotification, error) {
	notification, err := s.ports.Members.NotificationSnapshot(ctx, s.tx, memberID, identityID)
	return notification, linkNotFound(err)
}

func (s store) MemberCompletionEligible(ctx context.Context, memberID string) (bool, error) {
	return s.ports.Members.CompletionEligible(ctx, s.tx, memberID)
}

func (s store) MemberAvatarAssetID(ctx context.Context, memberID string) (string, error) {
	return s.ports.Members.AvatarAssetID(ctx, s.tx, memberID)
}

func (s store) ExpiredUnonboardedMembers(ctx context.Context, cutoff time.Time, limit int, after *account.MemberUnonboardedCursor) ([]account.MemberUnonboardedCandidate, *account.MemberUnonboardedCursor, error) {
	return s.ports.Members.ListExpiredUnonboarded(ctx, s.tx, cutoff, limit, after)
}

func (s store) RecheckUnonboardedMember(ctx context.Context, memberID, identityID string, cutoff *time.Time) (bool, error) {
	return s.ports.Members.RecheckUnonboarded(ctx, s.tx, memberID, identityID, cutoff)
}

func (s store) UnonboardedMemberIdentity(ctx context.Context, memberID string) (string, bool, error) {
	return s.ports.Members.UnonboardedIdentity(ctx, s.tx, memberID)
}

func deleteIdentityCommand(command account.DeleteIdentityCommand) *managev1.UserDeleteIdentityCommand {
	return &managev1.UserDeleteIdentityCommand{
		Mode: managev1.UserDeleteIdentityMode(command.Mode), MemberId: command.MemberID, IdentityId: command.IdentityID,
		NotificationEmail: command.NotificationEmail, NotificationName: command.NotificationName,
		NotificationLocale: command.NotificationLocale, AvatarAssetId: command.AvatarAssetID,
	}
}

func (s store) EnqueueUserDeleteIdentity(ctx context.Context, command account.DeleteIdentityCommand) error {
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	return s.ports.Publisher.PublishUserDeleteIdentityWithExecutor(ctx, executor, deleteIdentityCommand(command))
}

func (s store) EnqueueUserDeleteAvatar(ctx context.Context, command account.DeleteAvatarCommand) error {
	transactional, ok := s.ports.Publisher.(transactionalUserDeletionDispatchPublisher)
	if !ok {
		return fmt.Errorf("scheduled user deletion publisher does not support transactional dispatch")
	}
	executor, err := contentsignal.TransactionExecutor(s.tx)
	if err != nil {
		return err
	}
	return transactional.PublishUserDeleteAvatarWithExecutor(ctx, executor, &managev1.UserDeleteAvatarCommand{
		MemberId: command.MemberID, AvatarAssetId: command.AvatarAssetID,
	})
}
