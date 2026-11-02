package member

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/uuidutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// The public asset statuses a deleted avatar may already have.
const (
	publicAssetStatusDeletePending = "delete_pending"
	publicAssetStatusDeleted       = "deleted"
)

// DeletionLifecycle is the Member implementation of Account's narrow
// deletion consumer boundary. It owns Member resource authorization, profile
// state, local references, and avatar asset lifecycle. Each method runs on a
// DeletionStore bound to Account's transaction or fenced connection.
type DeletionLifecycle struct{}

// DeletionAudit is supplied by the Account owner and is invoked only for an
// actual terminal Member transition, inside the transaction that owns it.
type DeletionAudit func(ctx context.Context, memberID string) error

// DeletionAuthorizer is the SpiceDB port that revokes and proves gone the
// Member resource relationships of a hard-delete target.
type DeletionAuthorizer interface {
	SnapshotResourceRelationshipDescriptors(ctx context.Context, plan policyv1.RelationshipSnapshotPlan) ([]auth.RelationshipSnapshot, auth.ZedToken, error)
	ApplyRelationships(ctx context.Context, mutations ...policyv1.RelationshipMutation) (auth.ZedToken, error)
	VerifyResourceRelationshipsDeleted(ctx context.Context, plan policyv1.RelationshipSnapshotPlan, token auth.ZedToken) error
}

// DeletionSnapshot is Member's verified state before Account terminates the
// matching Kratos identity.
type DeletionSnapshot struct {
	MemberID             string
	IdentityID           string
	PrimaryEmailSnapshot string
	AlreadyTombstoned    bool
	IdentityLinked       bool
}

// UnonboardedDeletionTarget is the Member-owned hard-delete target. A zero
// target means the command is stale or already complete.
type UnonboardedDeletionTarget struct {
	MemberID       string
	IdentityID     string
	IdentityLinked bool
}

// DeletionNotification is the Member profile projection captured in an
// Account-owned deletion request.
type DeletionNotification struct {
	Nickname     string
	PrimaryEmail string
	Locale       *string
}

func memberEmailsEqual(left, right string) bool {
	return emailutil.NormalizeAddressForDelivery(left) == emailutil.NormalizeAddressForDelivery(right)
}

func validateDeletionPair(memberID, identityID string) error {
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

func (DeletionLifecycle) PrepareTombstone(ctx context.Context, store DeletionStore, memberID, identityID, primaryEmailSnapshot string) (DeletionSnapshot, error) {
	result := DeletionSnapshot{MemberID: memberID, IdentityID: identityID, PrimaryEmailSnapshot: emailutil.NormalizeAddressForDelivery(primaryEmailSnapshot)}
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return result, err
	}
	if result.PrimaryEmailSnapshot == "" {
		return result, fmt.Errorf("user deletion identity command requires Member primary email snapshot")
	}
	member, err := store.DeletionMember(ctx, memberID)
	if err != nil {
		return result, fmt.Errorf("load Member email projection before identity deletion: %w", err)
	}
	result.AlreadyTombstoned = member.DeletedAt != nil
	result.IdentityLinked = member.AccountIdentityID != nil
	if result.AlreadyTombstoned && result.IdentityLinked {
		return result, fmt.Errorf("deleted Member retains an identity reverse pointer")
	}
	if !result.AlreadyTombstoned && result.IdentityLinked && strings.TrimSpace(*member.AccountIdentityID) != identityID {
		return result, fmt.Errorf("member reverse pointer does not match deletion identity")
	}
	if !result.AlreadyTombstoned && result.IdentityLinked {
		linked, identityExists, err := store.DeletionPairWitness(ctx, memberID, identityID)
		if err != nil {
			return result, err
		}
		if identityExists && !linked {
			return result, fmt.Errorf("member and identity deletion link is not bilateral")
		}
	}
	if result.AlreadyTombstoned {
		return result, nil
	}
	if member.PrimaryEmail == nil || !memberEmailsEqual(*member.PrimaryEmail, result.PrimaryEmailSnapshot) || !slices.Contains(member.AvailableEmails, result.PrimaryEmailSnapshot) {
		return result, fmt.Errorf("deletion email snapshot does not match the Member email projection")
	}
	return result, nil
}

// FinalizeTombstone tombstones the Member once its Identity is gone, in the
// caller's transaction, which also holds the Account audit appendAudit
// writes.
func (DeletionLifecycle) FinalizeTombstone(ctx context.Context, store DeletionStore, request DeletionSnapshot, deletedAt time.Time, appendAudit DeletionAudit) error {
	identityExists, err := store.IdentityExists(ctx, request.IdentityID)
	if err != nil {
		return err
	}
	if identityExists {
		return fmt.Errorf("exact Kratos identity still exists after deletion")
	}
	member, err := store.LockMember(ctx, request.MemberID)
	if err != nil {
		return err
	}
	if member.AccountIdentityID != nil {
		return fmt.Errorf("member reverse pointer was not cleared by exact identity deletion")
	}
	if err := store.DeletePersonalAccessTokens(ctx, request.MemberID); err != nil {
		return fmt.Errorf("delete Member personal access tokens: %w", err)
	}
	if member.DeletedAt == nil {
		if err := store.TombstoneMember(ctx, member.ID, deletedAt); err != nil {
			return err
		}
		member.DeletedAt = &deletedAt
	}
	if member.DeletedAt == nil {
		return fmt.Errorf("member tombstone was not established")
	}
	if !request.AlreadyTombstoned && (member.PrimaryEmail == nil || !memberEmailsEqual(*member.PrimaryEmail, request.PrimaryEmailSnapshot) || !slices.Contains(member.AvailableEmails, request.PrimaryEmailSnapshot)) {
		return fmt.Errorf("member email projection changed during identity deletion")
	}
	if !request.AlreadyTombstoned && appendAudit != nil {
		return appendAudit(ctx, request.MemberID)
	}
	return nil
}

func (DeletionLifecycle) NotificationSnapshot(ctx context.Context, store DeletionStore, memberID, identityID string) (DeletionNotification, error) {
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return DeletionNotification{}, err
	}
	member, err := store.NotificationMember(ctx, memberID, identityID)
	if err != nil {
		return DeletionNotification{}, err
	}
	result := DeletionNotification{Nickname: strings.TrimSpace(member.Nickname)}
	if member.PrimaryEmail != nil {
		result.PrimaryEmail = emailutil.NormalizeAddressForDelivery(*member.PrimaryEmail)
	}
	if member.PreferredLocale != nil {
		locale := strings.TrimSpace(*member.PreferredLocale)
		if locale != "" {
			result.Locale = &locale
		}
	}
	return result, nil
}

func (DeletionLifecycle) CompletionEligible(ctx context.Context, store DeletionStore, memberID string) (bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return false, err
	}
	return store.CompletionEligible(ctx, memberID)
}

func (DeletionLifecycle) CleanupAvatar(ctx context.Context, store DeletionStore, memberID, avatarAssetID string) error {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return err
	}
	if err := store.ReleaseAvatarBindings(ctx, memberID); err != nil {
		return fmt.Errorf("release avatar binding: %w", err)
	}
	avatarAssetID = strings.TrimSpace(avatarAssetID)
	if avatarAssetID == "" {
		return nil
	}
	if _, err := uuidutil.ParseCanonical(avatarAssetID, "avatar_asset_id"); err != nil {
		return err
	}
	status, found, err := store.PublicAssetStatus(ctx, avatarAssetID)
	if err != nil || !found {
		return err
	}
	if status == publicAssetStatusDeletePending || status == publicAssetStatusDeleted {
		return nil
	}
	if err := store.RequestPublicAssetDeletion(ctx, avatarAssetID); err != nil {
		if mutation.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func (DeletionLifecycle) AvatarAssetID(ctx context.Context, store DeletionStore, memberID string) (string, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return "", err
	}
	return store.AvatarAssetID(ctx, memberID)
}

func (DeletionLifecycle) PrepareUnonboardedHardDelete(
	ctx context.Context,
	store DeletionStore,
	memberID,
	identityID string,
) (UnonboardedDeletionTarget, error) {
	target := UnonboardedDeletionTarget{MemberID: memberID, IdentityID: identityID}
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return target, err
	}
	member, err := store.DeletionMember(ctx, memberID)
	if errors.Is(err, ErrMemberNotFound) {
		return UnonboardedDeletionTarget{}, nil
	}
	if err != nil {
		return target, err
	}
	if member.Onboarded || member.DeletedAt != nil {
		return UnonboardedDeletionTarget{}, nil
	}
	target.IdentityLinked = member.AccountIdentityID != nil
	if target.IdentityLinked && strings.TrimSpace(*member.AccountIdentityID) != identityID {
		return target, fmt.Errorf("unonboarded hard deletion identity does not match Member link")
	}
	if target.IdentityLinked {
		linked, identityExists, err := store.DeletionPairWitness(ctx, memberID, identityID)
		if err != nil {
			return target, err
		}
		if identityExists && !linked {
			return target, fmt.Errorf("unonboarded Member and identity link is not bilateral")
		}
	}
	return target, nil
}

// RevokeUnonboardedAuthorization deletes the Member resource relationships of
// a hard-delete target and proves them gone, before its rows are removed.
func (DeletionLifecycle) RevokeUnonboardedAuthorization(
	ctx context.Context,
	spicedb DeletionAuthorizer,
	target UnonboardedDeletionTarget,
) error {
	if target.MemberID == "" {
		return nil
	}
	memberSnapshotPlan, err := policyv1.Member.Snapshot(target.MemberID)
	if err != nil {
		return err
	}
	snapshots, readAt, err := spicedb.SnapshotResourceRelationshipDescriptors(ctx, memberSnapshotPlan)
	if err != nil {
		return fmt.Errorf("snapshot unonboarded Member SpiceDB relationships: %w", err)
	}
	deleteRelationships, err := memberAuthorizationSnapshotDeletions(target.MemberID, snapshots)
	if err != nil {
		return err
	}
	deletedAt := readAt
	if len(deleteRelationships) != 0 {
		deletedAt, err = spicedb.ApplyRelationships(ctx, deleteRelationships...)
		if err != nil {
			return fmt.Errorf("delete unonboarded Member SpiceDB relationships: %w", err)
		}
	}
	if err := spicedb.VerifyResourceRelationshipsDeleted(ctx, memberSnapshotPlan, deletedAt); err != nil {
		return fmt.Errorf("prove unonboarded Member SpiceDB relationship deletion: %w", err)
	}
	return nil
}

// FinalizeUnonboardedHardDelete removes the Member rows of a hard-delete
// target whose authorization is revoked, in the caller's transaction, which
// also holds the Account audit appendAudit writes.
func (DeletionLifecycle) FinalizeUnonboardedHardDelete(
	ctx context.Context,
	store DeletionStore,
	target UnonboardedDeletionTarget,
	appendAudit DeletionAudit,
) error {
	if target.MemberID == "" {
		return nil
	}
	current, err := store.LockMember(ctx, target.MemberID)
	if errors.Is(err, ErrMemberNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Onboarded || current.DeletedAt != nil {
		return nil
	}
	if current.AccountIdentityID != nil {
		if strings.TrimSpace(*current.AccountIdentityID) != target.IdentityID {
			return fmt.Errorf("unonboarded hard deletion identity changed during cleanup")
		}
		return fmt.Errorf("unonboarded Member identity anchor still exists after cleanup")
	}
	if err := store.DeleteUnonboardedReferences(ctx, target.MemberID); err != nil {
		return err
	}
	deleted, err := store.DeleteUnonboardedMember(ctx, target.MemberID)
	if err != nil || !deleted {
		return err
	}
	if appendAudit == nil {
		return nil
	}
	return appendAudit(ctx, target.MemberID)
}

func memberAuthorizationSnapshotDeletions(
	memberID string,
	snapshots []auth.RelationshipSnapshot,
) ([]policyv1.RelationshipMutation, error) {
	deletePolicy, err := policyv1.Member.DeletePolicy(memberID)
	if err != nil {
		return nil, err
	}
	deletes := make([]policyv1.RelationshipMutation, 0, len(snapshots))
	for index, snapshot := range snapshots {
		if !snapshot.Matches(deletePolicy) {
			return nil, fmt.Errorf("member relationship snapshot %d is outside the Member contract", index)
		}
		deletes = append(deletes, deletePolicy)
	}
	return deletes, nil
}

// ListExpiredUnonboarded lists one bounded page of the retention candidates
// after the cursor, and the cursor of the next page.
func (DeletionLifecycle) ListExpiredUnonboarded(
	ctx context.Context,
	store DeletionStore,
	cutoff time.Time,
	limit int,
	after *UnonboardedCursor,
) ([]UnonboardedCandidate, *UnonboardedCursor, error) {
	if limit <= 0 {
		return []UnonboardedCandidate{}, nil, nil
	}
	result, err := store.ExpiredUnonboarded(ctx, cutoff.UTC(), limit, after)
	if err != nil {
		return nil, nil, fmt.Errorf("list expired unonboarded Members: %w", err)
	}
	if len(result) == 0 {
		return result, nil, nil
	}
	last := result[len(result)-1]
	return result, &UnonboardedCursor{CreatedAt: last.CreatedAt, MemberID: last.MemberID}, nil
}

// RecheckUnonboarded reads the Member-owned deletion predicate after Account
// holds the identity mutation fence. It creates no claim, intent, or other
// state: false is the stale-command/admin no-op. A nil cutoff is the
// admin-request path; a non-nil cutoff is the seven-day retention path.
func (DeletionLifecycle) RecheckUnonboarded(
	ctx context.Context,
	store DeletionStore,
	memberID,
	identityID string,
	cutoff *time.Time,
) (bool, error) {
	if err := validateDeletionPair(memberID, identityID); err != nil {
		return false, err
	}
	return store.UnonboardedLinked(ctx, memberID, identityID, cutoff)
}

func (DeletionLifecycle) UnonboardedIdentity(ctx context.Context, store DeletionStore, memberID string) (string, bool, error) {
	if _, err := uuidutil.ParseCanonical(memberID, "member_id"); err != nil {
		return "", false, err
	}
	return store.UnonboardedIdentity(ctx, memberID)
}
