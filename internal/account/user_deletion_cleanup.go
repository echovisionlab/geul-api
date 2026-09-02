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
	"github.com/echovisionlab/geul-api/internal/identitystate"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
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

func scheduleImmediateUserDeletion(
	ctx context.Context,
	db *gorm.DB,
	identity auth.IdentityManager,
	publisher UserDeletionDispatchPublisher,
	spicedb *auth.SpiceDBClient,
	members MemberDeletionLifecycle,
	memberID string,
	identityID string,
	onScheduled func(context.Context, *gorm.DB, string) error,
	memberEmails MemberEmailProjection,
) error {
	if db == nil || identity == nil || publisher == nil || spicedb == nil || members == nil {
		return fmt.Errorf("user deletion dependencies are required")
	}
	if memberEmails == nil {
		return fmt.Errorf("member email projection is required")
	}
	projection := memberEmails
	if err := validateDeletionIDPair(memberID, identityID); err != nil {
		return err
	}
	if err := authorizationtarget.ValidateActivePair(ctx, db, memberID, identityID); err != nil {
		return fmt.Errorf("validate active member identity pair: %w", err)
	}

	if _, err := NewAccountEmailService(db, identity, projection).
		SyncMemberEmailProjection(ctx, identityID, nil, nil); err != nil {
		return fmt.Errorf("sync deletion Member email projection: %w", err)
	}
	accountEmail, _, err := ResolveMemberPrimaryEmailForIdentity(ctx, db, projection, identity, identityID)
	if err != nil {
		return fmt.Errorf("resolve deletion email snapshot: %w", err)
	}
	if accountEmail == nil {
		return fmt.Errorf("verified Member primary email is required for permanent deletion")
	}
	member, err := members.NotificationSnapshot(ctx, db, memberID, identityID)
	if err != nil {
		return err
	}
	nickname := strings.TrimSpace(member.Nickname)
	normalizedEmail := normalizeAccountEmail(accountEmail.Email)
	if normalizedEmail == "" {
		return fmt.Errorf("member primary email is required for permanent deletion")
	}
	if member.PrimaryEmail == "" || !accountEmailsEqual(member.PrimaryEmail, normalizedEmail) {
		return fmt.Errorf("member primary email is not synchronized")
	}
	var locale *string
	if member.Locale != nil {
		if normalized := localization.NormalizeSupportedLocale(*member.Locale); normalized != nil {
			locale = normalized
		}
	}

	now := time.Now().UTC()
	request := model.UserDeletionRequest{
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
		NotificationName:            accountLifecycleStringPointer(nickname),
		NotificationLocale:          locale,
		CreatedAt:                   now,
		UpdatedAt:                   now,
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := identitystate.Lock(tx, identityID); err != nil {
			return err
		}
		current, err := authorizationtarget.LinkedMemberForMember(tx.WithContext(ctx), memberID, true)
		if err != nil {
			if errors.Is(err, authorizationtarget.ErrIneligible) {
				return authorizationtarget.ErrIneligible
			}
			return err
		}
		if current.IdentityID != identityID {
			return authorizationtarget.ErrIneligible
		}
		if err := ValidateLastActiveAdminDeletionWithAuthorization(ctx, tx, memberID, identityID, spicedb); err != nil {
			return err
		}
		previousState := "none"
		var currentRequest model.UserDeletionRequest
		loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("member_id = ?::uuid AND identity_id = ?::uuid", memberID, identityID).
			Take(&currentRequest).Error
		if loadErr == nil {
			if currentRequest.LifecycleState == accountLifecycleStateScheduled ||
				currentRequest.LifecycleState == accountLifecycleStateRecoveryConfirmationPending {
				request = currentRequest
				return nil
			}
			previousState = currentRequest.LifecycleState
		} else if !errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return loadErr
		}
		if err := tx.Where("member_id = ?::uuid AND identity_id = ?::uuid", memberID, identityID).
			Delete(&model.UserDeletionRequest{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&request).Error; err != nil {
			return err
		}
		if onScheduled == nil {
			return nil
		}
		return onScheduled(ctx, tx, previousState)
	}); err != nil {
		return err
	}
	if err := ensureScheduledIdentityInactive(ctx, db, identity, request.ID, identityID); err != nil {
		return fmt.Errorf("converge immediate deletion identity: %w", err)
	}
	return DispatchScheduledUserDeletion(ctx, db, publisher, spicedb, members, request.ID, time.Now().UTC())
}

// DispatchScheduledUserDeletion deletes the lifecycle row only after the
// durable broker confirms the immutable dual-ID command.
