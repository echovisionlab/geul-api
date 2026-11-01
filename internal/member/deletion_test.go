package member_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/member/membertest"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
)

// deletionFixture is an onboarded Member, with its email projection and two
// personal access tokens, whose Identity Account is about to delete.
func deletionFixture(t *testing.T) (*membertest.Store, *mutationtest.UnitOfWork[member.Store], string, string) {
	t.Helper()
	store, units := membertest.New()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	value := store.SeedLinked(memberID, identityID, "Departing")
	email := "departing@example.test"
	value.PrimaryEmail, value.AvailableEmails = &email, []string{email}
	bio := "private"
	value.Bio = &bio
	store.SeedMember(value)
	store.SeedPersonalAccessTokens(memberID, 2)
	return store, units, memberID, identityID
}

func finalizeTombstone(t *testing.T, units *mutationtest.UnitOfWork[member.Store], snapshot member.DeletionSnapshot, audit member.DeletionAudit) error {
	t.Helper()
	return mutationtest.Transact(t, mutationtest.Harness[member.Store]{Units: units}, func(ctx context.Context, store member.Store) error {
		return member.DeletionLifecycle{}.FinalizeTombstone(ctx, store, snapshot, time.Now().UTC(), audit)
	})
}

func TestFinalizeTombstoneDeletesPersonalAccessTokensAndReplayIsNoOp(t *testing.T) {
	t.Parallel()
	store, units, memberID, identityID := deletionFixture(t)
	snapshot, err := member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, identityID, " Departing@Example.test ")
	require.NoError(t, err)
	require.Equal(t, member.DeletionSnapshot{MemberID: memberID, IdentityID: identityID, PrimaryEmailSnapshot: "departing@example.test", IdentityLinked: true}, snapshot)

	require.ErrorContains(t, finalizeTombstone(t, units, snapshot, nil), "exact Kratos identity still exists after deletion")
	store.DeleteIdentity(identityID)
	var audited []string
	audit := func(_ context.Context, memberID string) error {
		audited = append(audited, memberID)
		return nil
	}
	require.NoError(t, finalizeTombstone(t, units, snapshot, audit))
	stored, _ := store.Member(memberID)
	require.NotNil(t, stored.DeletedAt)
	require.Nil(t, stored.Bio)
	require.Equal(t, "Departing", stored.Nickname)
	require.Equal(t, []string{"departing@example.test"}, stored.AvailableEmails)
	require.Zero(t, store.PersonalAccessTokens(memberID))
	require.Equal(t, []string{memberID}, audited)

	replay, err := member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, identityID, "departing@example.test")
	require.NoError(t, err)
	require.True(t, replay.AlreadyTombstoned)
	require.NoError(t, finalizeTombstone(t, units, replay, audit))
	require.Equal(t, []string{memberID}, audited)
	eligible, err := member.DeletionLifecycle{}.CompletionEligible(t.Context(), store, memberID)
	require.NoError(t, err)
	require.True(t, eligible)
}

func TestFinalizeTombstoneRollsBackMemberStateWhenAccountAuditFails(t *testing.T) {
	t.Parallel()
	store, units, memberID, identityID := deletionFixture(t)
	snapshot, err := member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, identityID, "departing@example.test")
	require.NoError(t, err)
	store.DeleteIdentity(identityID)
	auditDown := errors.New("account audit unavailable")

	err = finalizeTombstone(t, units, snapshot, func(context.Context, string) error { return auditDown })

	require.ErrorIs(t, err, auditDown)
	stored, _ := store.Member(memberID)
	require.Nil(t, stored.DeletedAt)
	require.NotNil(t, stored.Bio)
	require.Equal(t, 2, store.PersonalAccessTokens(memberID))
}

func TestDeletionLifecycleRejectsAMismatchedOrNonBilateralLink(t *testing.T) {
	t.Parallel()
	store, _, memberID, identityID := deletionFixture(t)
	_, err := member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, identityID, "other@example.test")
	require.ErrorContains(t, err, "deletion email snapshot does not match the Member email projection")
	_, err = member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, uuid.NewString(), "departing@example.test")
	require.ErrorContains(t, err, "member reverse pointer does not match deletion identity")
	_, err = member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, memberID, "departing@example.test")
	require.ErrorContains(t, err, "member_id and identity_id must be distinct")

	store.SeedIdentity(identityID, membertest.Identity{ExternalID: uuid.NewString()})
	_, err = member.DeletionLifecycle{}.PrepareTombstone(t.Context(), store, memberID, identityID, "departing@example.test")
	require.ErrorContains(t, err, "member and identity deletion link is not bilateral")
}

func TestDeletionNotificationSnapshotNormalizesTheRecipient(t *testing.T) {
	t.Parallel()
	store, _, memberID, identityID := deletionFixture(t)
	value, _ := store.Member(memberID)
	email, locale := " Departing@Example.test ", " ko "
	value.PrimaryEmail, value.PreferredLocale = &email, &locale
	store.SeedMember(value)

	snapshot, err := member.DeletionLifecycle{}.NotificationSnapshot(t.Context(), store, memberID, identityID)
	require.NoError(t, err)
	trimmed := "ko"
	require.Equal(t, member.DeletionNotification{Nickname: "Departing", PrimaryEmail: "departing@example.test", Locale: &trimmed}, snapshot)

	_, err = member.DeletionLifecycle{}.NotificationSnapshot(t.Context(), store, memberID, uuid.NewString())
	require.ErrorIs(t, err, member.ErrMemberNotFound)
}

func TestUnonboardedHardDeleteRemovesTheMemberAndItsReferences(t *testing.T) {
	t.Parallel()
	store, units := membertest.New()
	memberID, identityID := uuid.NewString(), uuid.NewString()
	value := store.SeedLinked(memberID, identityID, memberID)
	value.Onboarded = false
	value.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store.SeedMember(value)
	tagID := uuid.NewString()
	store.SeedTag(member.Tag{ID: tagID, Name: "tag"})
	store.SeedMemberTags(memberID, tagID)
	store.SeedBootstrap("first_admin", memberID)
	store.SeedBootstrap("other", memberID)
	cutoff := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	candidates, next, err := member.DeletionLifecycle{}.ListExpiredUnonboarded(t.Context(), store, cutoff, 10, nil)
	require.NoError(t, err)
	require.Equal(t, []member.UnonboardedCandidate{{MemberID: memberID, IdentityID: identityID, CreatedAt: value.CreatedAt}}, candidates)
	require.Equal(t, &member.UnonboardedCursor{CreatedAt: value.CreatedAt, MemberID: memberID}, next)
	eligible, err := member.DeletionLifecycle{}.RecheckUnonboarded(t.Context(), store, memberID, identityID, &cutoff)
	require.NoError(t, err)
	require.True(t, eligible)
	early := value.CreatedAt.Add(-time.Hour)
	eligible, err = member.DeletionLifecycle{}.RecheckUnonboarded(t.Context(), store, memberID, identityID, &early)
	require.NoError(t, err)
	require.False(t, eligible)

	target, err := member.DeletionLifecycle{}.PrepareUnonboardedHardDelete(t.Context(), store, memberID, identityID)
	require.NoError(t, err)
	require.Equal(t, member.UnonboardedDeletionTarget{MemberID: memberID, IdentityID: identityID, IdentityLinked: true}, target)
	harness := mutationtest.Harness[member.Store]{Units: units}
	finalize := func(audit member.DeletionAudit) error {
		return mutationtest.Transact(t, harness, func(ctx context.Context, tx member.Store) error {
			return member.DeletionLifecycle{}.FinalizeUnonboardedHardDelete(ctx, tx, target, audit)
		})
	}
	require.ErrorContains(t, finalize(nil), "unonboarded Member identity anchor still exists after cleanup")
	store.DeleteIdentity(identityID)
	var audited []string
	require.NoError(t, finalize(func(_ context.Context, memberID string) error {
		audited = append(audited, memberID)
		return nil
	}))
	_, found := store.Member(memberID)
	require.False(t, found)
	require.Empty(t, store.MemberTags(memberID))
	firstAdmin, kept := store.Bootstrap("first_admin")
	require.True(t, kept)
	require.Empty(t, firstAdmin)
	_, kept = store.Bootstrap("other")
	require.False(t, kept)
	require.Equal(t, []string{memberID}, audited)

	stale, err := member.DeletionLifecycle{}.PrepareUnonboardedHardDelete(t.Context(), store, memberID, identityID)
	require.NoError(t, err)
	require.Equal(t, member.UnonboardedDeletionTarget{}, stale)
	require.NoError(t, finalize(nil))
}

func TestCleanupAvatarReleasesTheBindingAndRequestsAssetDeletionOnce(t *testing.T) {
	t.Parallel()
	store, _ := membertest.New()
	memberID := uuid.NewString()
	asset := &member.Asset{ID: uuid.NewString()}
	store.SeedAvatar(memberID, asset)

	assetID, err := member.DeletionLifecycle{}.AvatarAssetID(t.Context(), store, memberID)
	require.NoError(t, err)
	require.Equal(t, asset.ID, assetID)
	require.NoError(t, member.DeletionLifecycle{}.CleanupAvatar(t.Context(), store, memberID, asset.ID))
	_, bound := store.AvatarBinding(memberID)
	require.False(t, bound)
	require.Equal(t, "delete_pending", store.AssetStatus(asset.ID))
	require.NoError(t, member.DeletionLifecycle{}.CleanupAvatar(t.Context(), store, memberID, asset.ID))
	require.NoError(t, member.DeletionLifecycle{}.CleanupAvatar(t.Context(), store, memberID, uuid.NewString()))
	require.Error(t, member.DeletionLifecycle{}.CleanupAvatar(t.Context(), store, memberID, "not-a-uuid"))
}

func TestEmailProjectionSyncRequiresACanonicalPrimaryAmongAvailable(t *testing.T) {
	t.Parallel()
	store, _, memberID, identityID := deletionFixture(t)

	require.ErrorIs(t, member.SyncEmailProjection(t.Context(), store, memberID, memberID, "a@example.test", []string{"a@example.test"}), member.ErrMemberNotFound)
	require.ErrorContains(t, member.SyncEmailProjection(t.Context(), store, memberID, identityID, "a@example.test", []string{"A@example.test"}), "member available email must be canonicalized")
	require.ErrorContains(t, member.SyncEmailProjection(t.Context(), store, memberID, identityID, "a@example.test", []string{"b@example.test"}), "member primary email must be an available email")
	require.ErrorIs(t, member.SyncEmailProjection(t.Context(), store, memberID, uuid.NewString(), "a@example.test", []string{"a@example.test"}), member.ErrMemberNotFound)

	require.NoError(t, member.SyncEmailProjection(t.Context(), store, memberID, identityID, " A@example.test ", []string{"a@example.test", "b@example.test"}))
	email, err := member.PrimaryEmail(t.Context(), store, memberID, identityID)
	require.NoError(t, err)
	require.Equal(t, "a@example.test", email)
}
