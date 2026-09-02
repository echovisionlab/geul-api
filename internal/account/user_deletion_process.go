package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func ProcessUserDeleteIdentityAudited(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityDeleter,
	spicedb *auth.SpiceDBClient,
	members MemberDeletionLifecycle,
	publisher UserDeletionFanoutPublisher,
	auditWriter domainaudit.Appender,
	command *managev1.UserDeleteIdentityCommand,
) error {
	if auditWriter == nil {
		return fmt.Errorf("user deletion audit writer is required")
	}
	return processUserDeleteIdentity(ctx, db, identity, spicedb, members, publisher, auditWriter, command)
}

func processUserDeleteIdentity(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityDeleter,
	spicedb *auth.SpiceDBClient,
	members MemberDeletionLifecycle,
	publisher UserDeletionFanoutPublisher,
	auditWriter domainaudit.Appender,
	command *managev1.UserDeleteIdentityCommand,
) error {
	if db == nil || identity == nil || spicedb == nil || members == nil || publisher == nil || command == nil {
		return fmt.Errorf("user deletion identity dependencies are required")
	}
	identityID := strings.TrimSpace(command.GetIdentityId())
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	switch command.GetMode() {
	case managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE:
		return processUnonboardedHardDelete(ctx, db, identity, spicedb, members, auditWriter, command)
	case managev1.UserDeleteIdentityMode_TOMBSTONE:
		// Continue through the retained Member tombstone lifecycle below.
	default:
		return fmt.Errorf("user deletion identity command requires an explicit deletion mode")
	}
	if strings.TrimSpace(command.GetNotificationName()) == "" {
		return fmt.Errorf("user deletion identity command requires member name snapshot")
	}
	return identitystate.WithMutation(ctx, db, identityID, func(
		mutationCtx context.Context,
		connection *gorm.DB,
	) error {
		request, err := members.PrepareTombstone(
			mutationCtx, connection, command.GetMemberId(), command.GetIdentityId(), command.GetNotificationEmail(),
		)
		if err != nil {
			return err
		}
		if err := deleteOrConfirmUserIdentity(mutationCtx, connection, identity, request); err != nil {
			return err
		}
		if err := deleteAccountIdentityAuthorizationAndAnchor(mutationCtx, connection, spicedb, request.IdentityID); err != nil {
			return err
		}
		if err := members.FinalizeTombstone(
			mutationCtx,
			connection,
			request,
			time.Now().UTC(),
			accountDeletedAudit(auditWriter),
		); err != nil {
			return fmt.Errorf("finalize member tombstone: %w", err)
		}
		// Avatar cleanup was durably enqueued with the scheduled lifecycle
		// transaction. This consumer owns only identity cleanup and completion mail;
		// it must not enqueue a second, non-transactional avatar command.
		return PublishUserDeletionCompletionEmail(mutationCtx, connection, members, publisher, command)
	})
}

func accountDeletedAudit(writer domainaudit.Appender) MemberDeletionAudit {
	if writer == nil {
		return nil
	}
	return func(ctx context.Context, tx *gorm.DB, memberID string) error {
		return domainaudit.AppendSystem(
			ctx,
			tx,
			writer,
			sharedtelemetry.ServiceBackend,
			sharedtelemetry.AuditAccountDeleted,
			func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletedAuditRecord(metadata, memberID)
			},
		)
	}
}

func deleteAccountIdentityAuthorizationAndAnchor(
	ctx context.Context,
	db *gorm.DB,
	spicedb *auth.SpiceDBClient,
	identityID string,
) error {
	parsed, err := uuidutil.ParseCanonical(identityID, "account_identity_id")
	if err != nil {
		return err
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(parsed.String()))
	if err != nil {
		return err
	}
	if _, err := spicedb.DeleteAllAccountIdentityRelationships(ctx, subject); err != nil {
		return fmt.Errorf("delete account identity SpiceDB relationships: %w", err)
	}
	// The anchor is application-owned and independent of Kratos. Deleting it
	// after SpiceDB proves subject absence makes the account UUID
	// un-reusable while preserving Member attribution/tombstone history.
	if err := db.WithContext(ctx).Exec(
		`DELETE FROM public.account_identity WHERE id = ?::uuid`, parsed.String(),
	).Error; err != nil {
		return fmt.Errorf("delete account identity anchor: %w", err)
	}
	return nil
}

func deletedUserCompletionEmail(command *managev1.UserDeleteIdentityCommand) *managev1.SendEmailEvent {
	if command == nil {
		return nil
	}
	recipient := normalizeAccountEmail(command.GetNotificationEmail())
	name := strings.TrimSpace(command.GetNotificationName())
	if recipient == "" || name == "" {
		return nil
	}
	memberID := command.GetMemberId()
	job := &managev1.SendEmailEvent{
		Recipient:        recipient,
		TemplateType:     emailutil.EventAccountDeletionComplete.String(),
		TemplateData:     map[string]string{"name": name},
		RecipientContext: emailutil.SystemDirectEmailContext("account_deletion_complete"),
		ReferenceId:      &memberID,
	}
	if locale := localization.NormalizeSupportedLocale(command.GetNotificationLocale()); locale != nil {
		job.Locale = locale
	}
	return job
}

// PublishUserDeletionCompletionEmail verifies the retained tombstone and
// queues the completion notice from the original durable command snapshot.
// It is called only after fully-consistent SpiceDB cleanup and anchor removal.
func PublishUserDeletionCompletionEmail(
	ctx context.Context,
	db *gorm.DB,
	members MemberDeletionLifecycle,
	publisher EmailCommandPublisher,
	command *managev1.UserDeleteIdentityCommand,
) error {
	if db == nil || members == nil || publisher == nil || command == nil {
		return fmt.Errorf("user deletion completion email dependencies are required")
	}
	memberID := strings.TrimSpace(command.GetMemberId())
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	eligible, err := members.CompletionEligible(ctx, db, memberID)
	if err != nil {
		return fmt.Errorf("load deleted Member completion projection: %w", err)
	}
	if !eligible {
		return nil
	}
	return emailutil.PublishCommand(ctx, publisher, deletedUserCompletionEmail(command), "account-deletion-complete:"+memberID)
}

func ProcessUserDeleteAvatar(
	ctx context.Context, db *gorm.DB, members MemberDeletionLifecycle, memberID, avatarAssetID string,
) error {
	if members == nil {
		return fmt.Errorf("member deletion lifecycle is required")
	}
	return members.CleanupAvatar(ctx, db, memberID, avatarAssetID)
}

func validateDeletionIDPair(memberID, identityID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	if memberID == identityID {
		return fmt.Errorf("member_id and identity_id must be distinct")
	}
	return nil
}
