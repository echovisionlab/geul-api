package account

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// DeletionAuthorizer removes the SpiceDB relationships of a deleted
// account Identity.
type DeletionAuthorizer interface {
	DeleteAllAccountIdentityRelationships(ctx context.Context, subject auth.AccountIdentitySubject) (auth.ZedToken, error)
}

// UserDeletion is the worker side of an account deletion: it deletes the
// Kratos Identity, its SpiceDB relationships and anchor, settles the Member,
// and sends the completion email. Every step holds the Identity fence, and
// each database step commits on its own so a replay resumes after the last
// one that committed.
type UserDeletion struct {
	Units      Units
	Identities auth.IdentityDeleter
	SpiceDB    DeletionAuthorizer
	Members    MemberAuthorizations
	Emails     EmailPublisher
	Audit      domainaudit.Appender
}

func (d UserDeletion) validate() error {
	if d.Audit == nil {
		return fmt.Errorf("user deletion audit writer is required")
	}
	if d.Units == nil || d.Identities == nil || d.SpiceDB == nil || d.Members == nil || d.Emails == nil || d.Audit == nil {
		return fmt.Errorf("user deletion identity dependencies are required")
	}
	return nil
}

// ProcessIdentity runs one durable deletion command.
func (d UserDeletion) ProcessIdentity(ctx context.Context, command DeleteIdentityCommand) error {
	if err := d.validate(); err != nil {
		return err
	}
	identityID := strings.TrimSpace(command.IdentityID)
	if _, err := uuidutil.ParseCanonical(identityID, "identity_id"); err != nil {
		return err
	}
	switch command.Mode {
	case DeleteIdentityUnonboardedHardDelete:
		return d.processUnonboardedHardDelete(ctx, command)
	case DeleteIdentityTombstone:
		// Continue through the retained Member tombstone lifecycle below.
	default:
		return fmt.Errorf("user deletion identity command requires an explicit deletion mode")
	}
	if strings.TrimSpace(ptrStringValue(command.NotificationName)) == "" {
		return fmt.Errorf("user deletion identity command requires member name snapshot")
	}
	return d.Units.Fenced(ctx, identityID, func(mutationCtx context.Context, steps Steps) error {
		var request MemberDeletionSnapshot
		if err := steps.Step(mutationCtx, func(store Store) error {
			var err error
			request, err = store.PrepareMemberTombstone(mutationCtx, command.MemberID, command.IdentityID, ptrStringValue(command.NotificationEmail))
			return err
		}); err != nil {
			return err
		}
		if err := d.deleteOrConfirmUserIdentity(mutationCtx, steps, request); err != nil {
			return err
		}
		if err := d.deleteAccountIdentityAuthorizationAndAnchor(mutationCtx, steps, request.IdentityID); err != nil {
			return err
		}
		if err := steps.Step(mutationCtx, func(store Store) error {
			return store.FinalizeMemberTombstone(mutationCtx, request, time.Now().UTC(), accountDeletedAudit(store, d.Audit))
		}); err != nil {
			return fmt.Errorf("finalize member tombstone: %w", err)
		}
		// Avatar cleanup was durably enqueued with the scheduled lifecycle
		// transaction. This consumer owns only identity cleanup and completion mail;
		// it must not enqueue a second, non-transactional avatar command.
		return d.publishCompletionEmail(mutationCtx, steps, command)
	})
}

// accountDeletedAudit appends the terminal account.deleted record through
// the store of the Member step's transaction.
func accountDeletedAudit(store Store, writer domainaudit.Appender) MemberDeletionAudit {
	return func(ctx context.Context, memberID string) error {
		return store.AppendSystemAudit(ctx, writer, sharedtelemetry.AuditAccountDeleted, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewAccountDeletedAuditRecord(metadata, memberID)
		})
	}
}

func (d UserDeletion) deleteAccountIdentityAuthorizationAndAnchor(ctx context.Context, steps Steps, identityID string) error {
	parsed, err := uuidutil.ParseCanonical(identityID, "account_identity_id")
	if err != nil {
		return err
	}
	subject, err := auth.NewAccountIdentitySubject(auth.IdentityID(parsed.String()))
	if err != nil {
		return err
	}
	if _, err := d.SpiceDB.DeleteAllAccountIdentityRelationships(ctx, subject); err != nil {
		return fmt.Errorf("delete account identity SpiceDB relationships: %w", err)
	}
	// The anchor is application-owned and independent of Kratos. Deleting it
	// after SpiceDB proves subject absence makes the account UUID
	// un-reusable while preserving Member attribution/tombstone history.
	if err := steps.Step(ctx, func(store Store) error {
		return store.DeleteAccountIdentityAnchor(ctx, parsed.String())
	}); err != nil {
		return fmt.Errorf("delete account identity anchor: %w", err)
	}
	return nil
}

func deletedUserCompletionEmail(command DeleteIdentityCommand) *Email {
	recipient := normalizeAccountEmail(ptrStringValue(command.NotificationEmail))
	name := strings.TrimSpace(ptrStringValue(command.NotificationName))
	if recipient == "" || name == "" {
		return nil
	}
	memberID := command.MemberID
	job := &Email{
		Recipient:    recipient,
		TemplateType: emailutil.EventAccountDeletionComplete.String(),
		TemplateData: map[string]string{"name": name},
		SystemReason: "account_deletion_complete",
		ReferenceID:  &memberID,
		MessageID:    "account-deletion-complete:" + memberID,
	}
	if locale := localization.NormalizeSupportedLocale(ptrStringValue(command.NotificationLocale)); locale != nil {
		job.Locale = locale
	}
	return job
}

// publishCompletionEmail verifies the retained tombstone and queues the
// completion notice from the original durable command snapshot. It is
// called only after fully-consistent SpiceDB cleanup and anchor removal.
func (d UserDeletion) publishCompletionEmail(ctx context.Context, steps Steps, command DeleteIdentityCommand) error {
	memberID := strings.TrimSpace(command.MemberID)
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	var eligible bool
	if err := steps.Step(ctx, func(store Store) error {
		var err error
		eligible, err = store.MemberCompletionEligible(ctx, memberID)
		return err
	}); err != nil {
		return fmt.Errorf("load deleted Member completion projection: %w", err)
	}
	if !eligible {
		return nil
	}
	job := deletedUserCompletionEmail(command)
	if job == nil {
		return fmt.Errorf("email command is required")
	}
	return d.Emails.PublishEmail(ctx, *job)
}

// ProcessAvatar releases a deleted Member's avatar binding and schedules
// the asset's deletion.
func (d UserDeletion) ProcessAvatar(ctx context.Context, memberID, avatarAssetID string) error {
	if d.Members == nil {
		return fmt.Errorf("member deletion lifecycle is required")
	}
	return d.Members.CleanupAvatar(ctx, memberID, avatarAssetID)
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
