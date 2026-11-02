package accountpostgres

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

// EmailCommandPublisher is the transport the Account email commands go out
// on.
type EmailCommandPublisher = email.CommandPublisher

// EmailCommands publishes Account email commands as SendEmail events
// (email.PublishCommand).
type EmailCommands struct {
	Publisher EmailCommandPublisher
}

var _ account.EmailPublisher = EmailCommands{}

// PublishEmail implements account.EmailPublisher.
func (c EmailCommands) PublishEmail(ctx context.Context, job account.Email) error {
	event := &managev1.SendEmailEvent{
		Recipient:    job.Recipient,
		TemplateType: job.TemplateType,
		TemplateData: job.TemplateData,
		ReferenceId:  job.ReferenceID,
		Locale:       job.Locale,
	}
	if job.AccountIdentityID != "" {
		event.RecipientContext = email.AccountSelectedPrimaryEmailContext(job.AccountIdentityID)
	} else {
		event.RecipientContext = email.SystemDirectEmailContext(job.SystemReason)
	}
	return email.PublishCommand(ctx, c.Publisher, event, job.MessageID)
}

// memberAuthorizations is the Member side of a deletion outside an Account
// transaction, over the pool and the SpiceDB client.
type memberAuthorizations struct {
	db      *gorm.DB
	members MemberDeletion
	spicedb *auth.SpiceDBClient
}

// MemberAuthorizations returns account.MemberAuthorizations over members.
func MemberAuthorizations(db *gorm.DB, members MemberDeletion, spicedb *auth.SpiceDBClient) account.MemberAuthorizations {
	return memberAuthorizations{db: db, members: members, spicedb: spicedb}
}

func (m memberAuthorizations) RevokeUnonboardedAuthorization(ctx context.Context, target account.MemberUnonboardedDeletionTarget) error {
	return m.members.RevokeUnonboardedAuthorization(ctx, m.spicedb, target)
}

func (m memberAuthorizations) CleanupAvatar(ctx context.Context, memberID, avatarAssetID string) error {
	return m.members.CleanupAvatar(ctx, m.db, memberID, avatarAssetID)
}

// The worker entry points below bind the Account flows the worker runs to
// its database, publisher and Member deletion port.

// DispatchScheduledUserDeletion runs account.DispatchScheduledUserDeletion.
func DispatchScheduledUserDeletion(
	ctx context.Context,
	db *gorm.DB,
	publisher UserDeletionDispatchPublisher,
	spicedb *auth.SpiceDBClient,
	members MemberDeletion,
	requestID string,
	now time.Time,
) error {
	if db == nil || publisher == nil || spicedb == nil || members == nil {
		return fmt.Errorf("scheduled user deletion dependencies are required")
	}
	transactional, ok := publisher.(transactionalUserDeletionDispatchPublisher)
	if !ok {
		return fmt.Errorf("scheduled user deletion publisher does not support transactional dispatch")
	}
	return account.DispatchScheduledUserDeletion(ctx, newUnits(db, Ports{Members: members, Publisher: transactional}), spicedb, requestID, now)
}

// EnqueueExpiredUnonboardedMembers runs account.EnqueueExpiredUnonboardedMembers.
func EnqueueExpiredUnonboardedMembers(
	ctx context.Context,
	db *gorm.DB,
	publisher UserDeletionIdentityDispatchPublisher,
	members MemberDeletion,
	now time.Time,
	pageSize int,
) (int, error) {
	if db == nil || publisher == nil || members == nil {
		return 0, fmt.Errorf("expired unonboarded cleanup requires transactional user deletion publisher")
	}
	return account.EnqueueExpiredUnonboardedMembers(ctx, newUnits(db, Ports{Members: members, Publisher: publisher}), now, pageSize)
}

// ClearExpiredTimedBan runs account.ClearExpiredTimedBan.
func ClearExpiredTimedBan(
	ctx context.Context,
	db *gorm.DB,
	identityManager auth.IdentityManager,
	identityID string,
	now time.Time,
	auditWriter domainaudit.Appender,
) (bool, error) {
	return account.ClearExpiredTimedBan(ctx, newUnits(db, Ports{}), identityManager, identityID, now, auditWriter)
}

// deletion binds the worker side of an account deletion.
func deletion(
	db *gorm.DB,
	identity auth.IdentityDeleter,
	spicedb *auth.SpiceDBClient,
	members MemberDeletion,
	publisher EmailCommandPublisher,
	auditWriter domainaudit.Appender,
) account.UserDeletion {
	return account.UserDeletion{
		Units: newUnits(db, Ports{Members: members}), Identities: identity, SpiceDB: spicedb,
		Members: MemberAuthorizations(db, members, spicedb), Emails: EmailCommands{Publisher: publisher}, Audit: auditWriter,
	}
}

// ProcessUserDeleteIdentityAudited runs one durable deletion command.
func ProcessUserDeleteIdentityAudited(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityDeleter,
	spicedb *auth.SpiceDBClient,
	members MemberDeletion,
	publisher EmailCommandPublisher,
	auditWriter domainaudit.Appender,
	command *managev1.UserDeleteIdentityCommand,
) error {
	if db == nil || identity == nil || spicedb == nil || members == nil || publisher == nil || command == nil {
		return fmt.Errorf("user deletion identity dependencies are required")
	}
	return deletion(db, identity, spicedb, members, publisher, auditWriter).ProcessIdentity(ctx, account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityMode(command.GetMode()), MemberID: command.GetMemberId(), IdentityID: command.GetIdentityId(),
		NotificationEmail: command.NotificationEmail, NotificationName: command.NotificationName,
		NotificationLocale: command.NotificationLocale, AvatarAssetID: command.AvatarAssetId,
	})
}

// ProcessUserDeleteAvatar cleans up a deleted Member's avatar.
func ProcessUserDeleteAvatar(ctx context.Context, db *gorm.DB, members MemberDeletion, memberID, avatarAssetID string) error {
	if members == nil {
		return fmt.Errorf("member deletion lifecycle is required")
	}
	return MemberAuthorizations(db, members, nil).CleanupAvatar(ctx, memberID, avatarAssetID)
}
