//go:build integration

package member

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFinalizeTombstoneRollsBackMemberStateWhenAccountAuditFails(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	memberID, identityID := seedBilateralDeletionPair(t, db, true, now.Add(-24*time.Hour))
	email := deletionFixtureEmail(memberID)
	seedMemberPersonalAccessToken(t, db, memberID, "AQEBAQEBAQEBAQEBAQEBAQ", "01", now)

	require.NoError(t, db.Exec(`DELETE FROM kratos.identities WHERE id = ?::uuid`, identityID).Error)
	require.NoError(t, db.Exec(`DELETE FROM account_identity WHERE id = ?::uuid`, identityID).Error)

	auditFailure := errors.New("account audit failed")
	err := (DeletionLifecycle{}).FinalizeTombstone(
		t.Context(),
		db,
		DeletionSnapshot{
			MemberID:             memberID,
			IdentityID:           identityID,
			PrimaryEmailSnapshot: email,
		},
		now,
		func(context.Context, *gorm.DB, string) error { return auditFailure },
	)
	require.ErrorIs(t, err, auditFailure)

	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Nil(t, member.DeletedAt)
	require.NotEmpty(t, member.Bio)
	require.Equal(t, int64(1), memberPersonalAccessTokenCount(t, db, memberID))
}

func TestFinalizeTombstoneDeletesMemberPersonalAccessTokensAndReplayIsNoOp(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	memberID, identityID := seedBilateralDeletionPair(t, db, true, now.Add(-24*time.Hour))
	email := deletionFixtureEmail(memberID)
	otherMemberID, _ := seedBilateralDeletionPair(t, db, true, now.Add(-24*time.Hour))
	seedMemberPersonalAccessToken(t, db, memberID, "AQEBAQEBAQEBAQEBAQEBAQ", "01", now)
	seedMemberPersonalAccessToken(t, db, otherMemberID, "AwMDAwMDAwMDAwMDAwMDAw", "03", now)

	require.NoError(t, db.Exec(`DELETE FROM kratos.identities WHERE id = ?::uuid`, identityID).Error)
	require.NoError(t, db.Exec(`DELETE FROM account_identity WHERE id = ?::uuid`, identityID).Error)

	auditCalls := 0
	request := DeletionSnapshot{
		MemberID:             memberID,
		IdentityID:           identityID,
		PrimaryEmailSnapshot: email,
	}
	require.NoError(t, (DeletionLifecycle{}).FinalizeTombstone(
		t.Context(),
		db,
		request,
		now,
		func(context.Context, *gorm.DB, string) error {
			auditCalls++
			return nil
		},
	))
	require.Equal(t, 1, auditCalls)
	require.Zero(t, memberPersonalAccessTokenCount(t, db, memberID))
	require.Equal(t, int64(1), memberPersonalAccessTokenCount(t, db, otherMemberID))

	request.AlreadyTombstoned = true
	replayAuditCalls := 0
	require.NoError(t, (DeletionLifecycle{}).FinalizeTombstone(
		t.Context(),
		db,
		request,
		now.Add(time.Minute),
		func(context.Context, *gorm.DB, string) error {
			replayAuditCalls++
			return errors.New("replay must not append an account audit")
		},
	))
	require.Zero(t, replayAuditCalls)
	require.Zero(t, memberPersonalAccessTokenCount(t, db, memberID))
	require.Equal(t, int64(1), memberPersonalAccessTokenCount(t, db, otherMemberID))
}

func TestDeletionLifecycleRejectsNonBilateralIdentityWitness(t *testing.T) {
	db := newServiceIntegrationDB(t)
	now := time.Now().UTC()
	lifecycle := DeletionLifecycle{}

	t.Run("tombstone", func(t *testing.T) {
		memberID, identityID := seedNonBilateralDeletionPair(t, db, true, now)
		_, err := lifecycle.PrepareTombstone(
			t.Context(),
			db,
			memberID,
			identityID,
			deletionFixtureEmail(memberID),
		)
		require.ErrorContains(t, err, "link is not bilateral")
	})

	t.Run("unonboarded", func(t *testing.T) {
		memberID, identityID := seedNonBilateralDeletionPair(t, db, false, now.Add(-8*24*time.Hour))
		_, err := lifecycle.PrepareUnonboardedHardDelete(t.Context(), db, memberID, identityID)
		require.ErrorContains(t, err, "link is not bilateral")

		candidates, _, err := lifecycle.ListExpiredUnonboarded(
			t.Context(),
			db,
			now.Add(-7*24*time.Hour),
			100,
			nil,
		)
		require.NoError(t, err)
		for _, candidate := range candidates {
			require.NotEqual(t, memberID, candidate.MemberID)
		}

		eligible, err := lifecycle.RecheckUnonboarded(
			t.Context(),
			db,
			memberID,
			identityID,
			nil,
		)
		require.NoError(t, err)
		require.False(t, eligible)

		resolvedIdentityID, found, err := lifecycle.UnonboardedIdentity(t.Context(), db, memberID)
		require.NoError(t, err)
		require.False(t, found)
		require.Empty(t, resolvedIdentityID)
	})
}

func seedNonBilateralDeletionPair(
	t *testing.T,
	db *gorm.DB,
	onboarded bool,
	createdAt time.Time,
) (string, string) {
	t.Helper()
	memberID := uuid.NewString()
	identityID := uuid.NewString()
	otherMemberID := uuid.NewString()
	email := deletionFixtureEmail(memberID)

	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (
			id, schema_id, traits, metadata_public, metadata_admin, state, external_id, created_at, updated_at
		) VALUES (
			?::uuid, 'user', jsonb_build_object('email', ?::text), '{}'::jsonb, '{}'::jsonb,
			'active', ?::text, ?, ?
		)
	`, identityID, email, otherMemberID, createdAt, createdAt).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id, created_at) VALUES (?::uuid, ?)`,
		identityID,
		createdAt,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID:                memberID,
		AccountIdentityID: &identityID,
		Nickname:          "Non-bilateral " + memberID,
		Onboarded:         onboarded,
		PrimaryEmail:      &email,
		AvailableEmails:   []string{email},
		SocialLinks:       map[string]string{},
		CreatedAt:         createdAt,
		UpdatedAt:         createdAt,
	}).Error)
	return memberID, identityID
}

func seedBilateralDeletionPair(
	t *testing.T,
	db *gorm.DB,
	onboarded bool,
	createdAt time.Time,
) (string, string) {
	t.Helper()
	memberID := uuid.NewString()
	identityID := uuid.NewString()
	email := deletionFixtureEmail(memberID)
	bio := "retained until tombstone commits"

	require.NoError(t, db.Exec(`
		INSERT INTO kratos.identities (
			id, schema_id, traits, metadata_public, metadata_admin, state, external_id, created_at, updated_at
		) VALUES (
			?::uuid, 'user', jsonb_build_object('email', ?::text), '{}'::jsonb, '{}'::jsonb,
			'active', ?::text, ?, ?
		)
	`, identityID, email, memberID, createdAt, createdAt).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id, created_at) VALUES (?::uuid, ?)`,
		identityID,
		createdAt,
	).Error)
	require.NoError(t, db.Create(&model.Member{
		ID:                memberID,
		AccountIdentityID: &identityID,
		Nickname:          "Bilateral " + memberID,
		Bio:               &bio,
		Onboarded:         onboarded,
		PrimaryEmail:      &email,
		AvailableEmails:   []string{email},
		SocialLinks:       map[string]string{},
		CreatedAt:         createdAt,
		UpdatedAt:         createdAt,
	}).Error)
	return memberID, identityID
}

func deletionFixtureEmail(memberID string) string {
	return "deletion-" + memberID + "@example.test"
}

func seedMemberPersonalAccessToken(
	t *testing.T,
	db *gorm.DB,
	memberID, selector, verifierByte string,
	now time.Time,
) {
	t.Helper()
	require.NoError(t, db.Exec(`
		INSERT INTO member_personal_access_token (
			selector, member_id, secret_hash, created_at, updated_at
		) VALUES (
			?, ?::uuid, decode(repeat(?, 32), 'hex'), ?, ?
		)
	`, selector, memberID, verifierByte, now, now).Error)
}

func memberPersonalAccessTokenCount(t *testing.T, db *gorm.DB, memberID string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(
		`SELECT COUNT(*) FROM member_personal_access_token WHERE member_id = ?::uuid`,
		memberID,
	).Scan(&count).Error)
	return count
}
