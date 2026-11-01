package account

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/localization"
	"github.com/echovisionlab/geul-api/internal/mutation"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

var ErrLastActiveAdminDeletion = errors.New("cannot delete the last active admin")
var ErrScheduledDeletionIdentityActive = errors.New("scheduled deletion identity is still active")

// validateLastActiveAdminDeletion performs the final-admin decision while
// the caller's transaction holds the last-administrator fence, which it
// takes first. The target role and remaining admin set both come from
// fully-consistent SpiceDB; the database only validates active
// identity/member lifecycle state.
func validateLastActiveAdminDeletion(
	ctx context.Context,
	store Store,
	spicedb GlobalRoles,
	memberID, identityID string,
) error {
	if err := validateDeletionIDPair(memberID, identityID); err != nil {
		return err
	}
	if err := store.LockAdminFence(ctx); err != nil {
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
	remaining, err := store.ActiveAdminCount(ctx, identityIDs)
	if err != nil {
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
	request   DeletionRequest
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
	result, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[immediateDeletion, Store]{
		Subjects: []string{identityID},
		Access:   freshAdmin(),
		Change: func(ctx context.Context, store Store) (immediateDeletion, error) {
			return s.storeImmediateDeletion(ctx, store, request)
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
	if err := s.ensureScheduledIdentityInactive(ctx, result.request.ID, identityID); err != nil {
		return fmt.Errorf("converge immediate deletion identity: %w", err)
	}
	return DispatchScheduledUserDeletion(ctx, s.units, s.spicedb, result.request.ID, time.Now().UTC())
}

// immediateDeletionRequest builds the scheduled deletion request of an
// active pair, with the verified notification snapshot the worker needs
// after the Identity is gone.
func (s *Service) immediateDeletionRequest(ctx context.Context, memberID, identityID string) (DeletionRequest, error) {
	if err := validateDeletionIDPair(memberID, identityID); err != nil {
		return DeletionRequest{}, err
	}
	if err := s.read(ctx, func(store Store) error { return store.ValidateActivePair(ctx, memberID, identityID) }); err != nil {
		return DeletionRequest{}, fmt.Errorf("validate active member identity pair: %w", err)
	}
	if _, err := newAccountEmailService(s.identity, s.transact).
		SyncMemberEmailProjection(ctx, identityID, nil, nil); err != nil {
		return DeletionRequest{}, fmt.Errorf("sync deletion Member email projection: %w", err)
	}
	accountEmail, _, err := ResolveMemberPrimaryEmailForIdentity(ctx, s.read, s.identity, identityID)
	if err != nil {
		return DeletionRequest{}, fmt.Errorf("resolve deletion email snapshot: %w", err)
	}
	if accountEmail == nil {
		return DeletionRequest{}, errors.New("verified Member primary email is required for permanent deletion")
	}
	var member MemberDeletionNotification
	if err := s.read(ctx, func(store Store) error {
		var err error
		member, err = store.MemberDeletionNotification(ctx, memberID, identityID)
		return err
	}); err != nil {
		return DeletionRequest{}, err
	}
	normalizedEmail := normalizeAccountEmail(accountEmail.Email)
	if normalizedEmail == "" {
		return DeletionRequest{}, errors.New("member primary email is required for permanent deletion")
	}
	if member.PrimaryEmail == "" || !accountEmailsEqual(member.PrimaryEmail, normalizedEmail) {
		return DeletionRequest{}, errors.New("member primary email is not synchronized")
	}
	var locale *string
	if member.Locale != nil {
		locale = localization.NormalizeSupportedLocale(*member.Locale)
	}
	now := time.Now().UTC()
	return DeletionRequest{
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
func (s *Service) storeImmediateDeletion(ctx context.Context, store Store, request DeletionRequest) (immediateDeletion, error) {
	current, err := store.LinkedMemberForMember(ctx, request.MemberID, true)
	if err != nil {
		return immediateDeletion{}, err
	}
	if current.IdentityID != request.IdentityID {
		return immediateDeletion{}, ErrIneligible
	}
	if err := validateLastActiveAdminDeletion(ctx, store, s.spicedb, request.MemberID, request.IdentityID); err != nil {
		return immediateDeletion{}, err
	}
	result := immediateDeletion{request: request, scheduled: true, previous: "none"}
	existing, found, err := store.LockPairDeletionRequest(ctx, request.MemberID, request.IdentityID)
	switch {
	case err != nil:
		return immediateDeletion{}, err
	case !found:
	case existing.LifecycleState == accountLifecycleStateScheduled || existing.LifecycleState == accountLifecycleStateRecoveryConfirmationPending:
		return immediateDeletion{request: existing}, nil
	default:
		result.previous = existing.LifecycleState
	}
	if err := store.DeletePairDeletionRequests(ctx, request.MemberID, request.IdentityID); err != nil {
		return immediateDeletion{}, err
	}
	return result, store.InsertDeletionRequest(ctx, result.request)
}
