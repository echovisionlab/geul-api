package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authorizationtarget"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
)

const userDeletionAdminFenceKey = int64(0x4745554c41444d49) // GEULADMI

var ErrLastActiveAdminDeletion = errors.New("cannot delete the last active admin")
var ErrScheduledDeletionIdentityActive = errors.New("scheduled deletion identity is still active")

type UserDeletionDispatchPublisher interface {
	PublishUserDeleteIdentity(context.Context, *managev1.UserDeleteIdentityCommand) error
	PublishUserDeleteAvatar(context.Context, *managev1.UserDeleteAvatarCommand) error
}

// UserDeletionIdentityDispatchPublisher is the narrow transactional seam
// used by the retention scheduler. It intentionally does not require avatar
// or email capabilities owned by the normal account-deletion flow.
type UserDeletionIdentityDispatchPublisher interface {
	PublishUserDeleteIdentityWithExecutor(context.Context, eventpkg.DBTX, *managev1.UserDeleteIdentityCommand) error
}

type UserDeletionFanoutPublisher interface {
	EmailCommandPublisher
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

// ValidateLastActiveAdminDeletionWithAuthorization performs the final-admin
// decision while the caller's transaction advisory lock is held. The target
// role and remaining admin set both come from fully-consistent SpiceDB; the
// database only validates active identity/member lifecycle state.
func ValidateLastActiveAdminDeletionWithAuthorization(
	ctx context.Context,
	tx *gorm.DB,
	memberID, identityID string,
	spicedb *auth.SpiceDBClient,
) error {
	if spicedb == nil {
		return fmt.Errorf("SpiceDB global admin authority is required")
	}
	if tx == nil {
		return fmt.Errorf("last-admin deletion validation requires a transaction")
	}
	if err := validateDeletionIDPair(memberID, identityID); err != nil {
		return err
	}
	if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", userDeletionAdminFenceKey).Error; err != nil {
		return err
	}
	targetActor, err := policyv1.NewAccountIdentityActor(identityID)
	if err != nil {
		return err
	}
	adminCan, err := policyv1.Platform.IsAdmin()
	if err != nil {
		return err
	}
	isAdmin, err := spicedb.CheckActorCan(ctx, targetActor, adminCan)
	if err != nil {
		return fmt.Errorf("check target global admin role: %w", err)
	}
	if !isAdmin {
		return nil
	}
	admins, err := spicedb.LookupGlobalSubjects(ctx, policyv1.Platform.LookupAdminSubjects())
	if err != nil {
		return fmt.Errorf("lookup global admins: %w", err)
	}
	identityIDs := make([]string, 0, len(admins))
	for _, subject := range admins {
		if subject.ID.String() != identityID {
			identityIDs = append(identityIDs, subject.ID.String())
		}
	}
	if len(identityIDs) == 0 {
		return ErrLastActiveAdminDeletion
	}
	var remaining int64
	if err := tx.Raw(`
		SELECT COUNT(*)
		FROM member
		JOIN kratos.identities AS identity ON identity.id = member.account_identity_id
		WHERE member.account_identity_id IN ?
		  AND member.deleted_at IS NULL
		  AND member.onboarded = TRUE
		  AND identity.state = 'active'
		  AND NOT COALESCE((identity.metadata_admin->>'banned')::boolean, false)
		  AND NOT EXISTS (
		      SELECT 1 FROM user_deletion_request AS request
		      WHERE request.member_id = member.id
		        AND request.identity_id = identity.id
		        AND request.lifecycle_state IN ?
		  )
	`, identityIDs, accountLifecycleDeletionPendingStates).Scan(&remaining).Error; err != nil {
		return err
	}
	if remaining == 0 {
		return ErrLastActiveAdminDeletion
	}
	return nil
}

// immediateDeletion is the outcome of scheduling an administrator's
// immediate deletion: the request to dispatch, and the state it replaced
// when it is new.
type immediateDeletion struct {
	request   model.UserDeletionRequest
	scheduled bool
	previous  string
}

// scheduleImmediateDeletion schedules another Member's deletion for now and
// dispatches it. A deletion already scheduled is dispatched as it stands.
func (s *Service) scheduleImmediateDeletion(ctx context.Context, memberID, identityID string) error {
	request, err := s.immediateDeletionRequest(ctx, memberID, identityID)
	if err != nil {
		return err
	}
	result, err := mutation.Run(ctx, s.mutations, mutation.Spec[immediateDeletion]{
		Subjects: []string{identityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, tx *gorm.DB) (immediateDeletion, error) {
			return s.storeImmediateDeletion(ctx, tx, request)
		},
		Audit: func(result immediateDeletion) mutation.Audit {
			if !result.scheduled {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditAccountUpdated, func(metadata sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewAccountDeletionScheduledAuditRecord(metadata, memberID, sharedtelemetry.AuditState(result.previous))
			})
		},
	})
	if err != nil {
		return err
	}
	if err := ensureScheduledIdentityInactive(ctx, s.db, s.identity, result.request.ID, identityID); err != nil {
		return fmt.Errorf("converge immediate deletion identity: %w", err)
	}
	return DispatchScheduledUserDeletion(ctx, s.db, s.publisher, s.spicedb, s.memberDeletion, result.request.ID, time.Now().UTC())
}

// immediateDeletionRequest builds the scheduled deletion request of an
// active pair, with the verified notification snapshot the worker needs
// after the Identity is gone.
func (s *Service) immediateDeletionRequest(ctx context.Context, memberID, identityID string) (model.UserDeletionRequest, error) {
	if err := validateDeletionIDPair(memberID, identityID); err != nil {
		return model.UserDeletionRequest{}, err
	}
	if err := authorizationtarget.ValidateActivePair(ctx, s.db, memberID, identityID); err != nil {
		return model.UserDeletionRequest{}, fmt.Errorf("validate active member identity pair: %w", err)
	}
	if _, err := NewAccountEmailService(s.db, s.identity, s.memberEmails).
		SyncMemberEmailProjection(ctx, identityID, nil, nil); err != nil {
		return model.UserDeletionRequest{}, fmt.Errorf("sync deletion Member email projection: %w", err)
	}
	accountEmail, _, err := ResolveMemberPrimaryEmailForIdentity(ctx, s.db, s.memberEmails, s.identity, identityID)
	if err != nil {
		return model.UserDeletionRequest{}, fmt.Errorf("resolve deletion email snapshot: %w", err)
	}
	if accountEmail == nil {
		return model.UserDeletionRequest{}, errors.New("verified Member primary email is required for permanent deletion")
	}
	member, err := s.memberDeletion.NotificationSnapshot(ctx, s.db, memberID, identityID)
	if err != nil {
		return model.UserDeletionRequest{}, err
	}
	normalizedEmail := normalizeAccountEmail(accountEmail.Email)
	if normalizedEmail == "" {
		return model.UserDeletionRequest{}, errors.New("member primary email is required for permanent deletion")
	}
	if member.PrimaryEmail == "" || !accountEmailsEqual(member.PrimaryEmail, normalizedEmail) {
		return model.UserDeletionRequest{}, errors.New("member primary email is not synchronized")
	}
	var locale *string
	if member.Locale != nil {
		locale = localization.NormalizeSupportedLocale(*member.Locale)
	}
	now := time.Now().UTC()
	return model.UserDeletionRequest{
		ID:                          uuid.NewString(),
		MemberID:                    memberID,
		IdentityID:                  identityID,
		Token:                       crypto.HashToken("admin-immediate:" + uuid.NewString()),
		TokenExpiresAt:              now.Add(-time.Second),
		ConfirmedAt:                 &now,
		ScheduledAt:                 &now,
		LifecycleState:              accountLifecycleStateScheduled,
		NotificationEmail:           &normalizedEmail,
		NotificationEmailVerifiedAt: &now,
		NotificationName:            accountLifecycleStringPointer(strings.TrimSpace(member.Nickname)),
		NotificationLocale:          locale,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}, nil
}

// storeImmediateDeletion rechecks the pair under its Identity fence and the
// last-admin fence, then replaces the pair's request with request unless a
// deletion is already scheduled.
func (s *Service) storeImmediateDeletion(ctx context.Context, tx *gorm.DB, request model.UserDeletionRequest) (immediateDeletion, error) {
	current, err := authorizationtarget.LinkedMemberForMember(tx.WithContext(ctx), request.MemberID, true)
	if err != nil {
		if errors.Is(err, authorizationtarget.ErrIneligible) {
			return immediateDeletion{}, authorizationtarget.ErrIneligible
		}
		return immediateDeletion{}, err
	}
	if current.IdentityID != request.IdentityID {
		return immediateDeletion{}, authorizationtarget.ErrIneligible
	}
	if err := ValidateLastActiveAdminDeletionWithAuthorization(ctx, tx, request.MemberID, request.IdentityID, s.spicedb); err != nil {
		return immediateDeletion{}, err
	}
	const pair = "member_id = ?::uuid AND identity_id = ?::uuid"
	result := immediateDeletion{request: request, scheduled: true, previous: "none"}
	var existing model.UserDeletionRequest
	err = tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Where(pair, request.MemberID, request.IdentityID).Take(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
	case err != nil:
		return immediateDeletion{}, err
	case existing.LifecycleState == accountLifecycleStateScheduled || existing.LifecycleState == accountLifecycleStateRecoveryConfirmationPending:
		return immediateDeletion{request: existing}, nil
	default:
		result.previous = existing.LifecycleState
	}
	if err := tx.Where(pair, request.MemberID, request.IdentityID).Delete(&model.UserDeletionRequest{}).Error; err != nil {
		return immediateDeletion{}, err
	}
	return result, tx.Create(&result.request).Error
}

// DispatchScheduledUserDeletion deletes the lifecycle row only after the
// durable broker confirms the immutable dual-ID command.
