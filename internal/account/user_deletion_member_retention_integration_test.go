//go:build integration

package account

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

type databaseIdentityDeleter struct{ db *gorm.DB }

func (d databaseIdentityDeleter) DeleteIdentity(ctx context.Context, identityID string) error {
	return d.db.WithContext(ctx).Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", identityID).Error
}

type recordingDeletionFanout struct {
	avatars []*managev1.UserDeleteAvatarCommand
	emails  []*managev1.SendEmailEvent
}

func (p *recordingDeletionFanout) PublishUserDeleteAvatar(_ context.Context, command *managev1.UserDeleteAvatarCommand) error {
	p.avatars = append(p.avatars, command)
	return nil
}

func (p *recordingDeletionFanout) PublishSendEmail(_ context.Context, command *managev1.SendEmailEvent) error {
	p.emails = append(p.emails, command)
	return nil
}

func TestProcessUserDeleteIdentityPreservesNameAndMemberEmails(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	primaryEmail := "primary@example.test"
	secondaryEmail := "secondary@example.test"
	nickname := "Preserved name"
	seedDeletionIdentity(t, db, identityID, memberID, primaryEmail)
	require.NoError(t, db.Exec(`
		INSERT INTO member (
			id, account_identity_id, nickname, onboarded, primary_email, available_emails,
			bio, website, social_links, preferred_locale
		) VALUES (
			?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text, ?::text],
			'private bio', 'https://example.test', '{"github":"member"}'::jsonb, 'ko'
		)
	`, memberID, identityID, nickname, primaryEmail, primaryEmail, secondaryEmail).Error)
	require.NoError(t, db.Create(&model.NewsletterSubscription{
		IdentityID: identityID, SubscribedAt: time.Now().UTC(),
	}).Error)
	consent := model.UserCookieConsent{
		ID: uuid.NewString(), MemberID: memberID, Essential: true, Analytics: true,
		ConsentVersion: 2, Source: "settings", RecordedAt: time.Now().UTC(),
	}
	require.NoError(t, db.Create(&consent).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member_personal_access_token (
			selector, member_id, secret_hash, created_at, updated_at
		) VALUES (
			'AQEBAQEBAQEBAQEBAQEBAQ', ?::uuid,
			decode(repeat('01', 32), 'hex'), CURRENT_TIMESTAMP, CURRENT_TIMESTAMP
		)
	`, memberID).Error)

	publisher := &recordingDeletionFanout{}
	command := &managev1.UserDeleteIdentityCommand{
		Mode:              managev1.UserDeleteIdentityMode_TOMBSTONE,
		MemberId:          memberID,
		IdentityId:        identityID,
		NotificationEmail: &primaryEmail,
		NotificationName:  &nickname,
	}
	require.NoError(t, processUserDeleteIdentity(
		t.Context(), db, databaseIdentityDeleter{db: db}, integrationSpiceDB(t),
		memberDeletionIntegrationAdapter{}, publisher, apitelemetry.NewDurableWriter(db), command,
	))

	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Nil(t, member.AccountIdentityID)
	require.NotNil(t, member.DeletedAt)
	require.Equal(t, nickname, member.Nickname)
	require.True(t, member.Onboarded)
	require.Nil(t, member.Bio)
	require.Nil(t, member.Website)
	require.Empty(t, member.SocialLinks)
	require.Nil(t, member.PreferredLocale)
	require.Equal(t, primaryEmail, *member.PrimaryEmail)
	require.Equal(t, []string{primaryEmail, secondaryEmail}, []string(member.AvailableEmails))

	var subscriptionCount int64
	require.NoError(t, db.Model(&model.NewsletterSubscription{}).
		Where("identity_id = ?::uuid", identityID).Count(&subscriptionCount).Error)
	require.Zero(t, subscriptionCount)
	var personalAccessTokenCount int64
	require.NoError(t, db.Table("member_personal_access_token").
		Where("member_id = ?::uuid", memberID).Count(&personalAccessTokenCount).Error)
	require.Zero(t, personalAccessTokenCount)
	var retainedConsent model.UserCookieConsent
	require.NoError(t, db.Where("id = ?::uuid", consent.ID).Take(&retainedConsent).Error)
	require.Equal(t, memberID, retainedConsent.MemberID)
	require.True(t, retainedConsent.Analytics)
	require.Equal(t, int32(2), retainedConsent.ConsentVersion)
	require.Empty(t, publisher.avatars)
	require.Len(t, publisher.emails, 1)
}

func TestProcessUserDeleteIdentityReplaySucceedsAfterEmailRetentionScrub(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	primaryEmail := "retained@example.test"
	nickname := "Retained Member"
	seedDeletionIdentity(t, db, identityID, memberID, primaryEmail)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text])
	`, memberID, identityID, nickname, primaryEmail, primaryEmail).Error)

	command := &managev1.UserDeleteIdentityCommand{
		Mode:              managev1.UserDeleteIdentityMode_TOMBSTONE,
		MemberId:          memberID,
		IdentityId:        identityID,
		NotificationEmail: &primaryEmail,
		NotificationName:  &nickname,
	}
	require.NoError(t, processUserDeleteIdentity(
		t.Context(), db, databaseIdentityDeleter{db: db}, integrationSpiceDB(t),
		memberDeletionIntegrationAdapter{}, &recordingDeletionFanout{}, apitelemetry.NewDurableWriter(db), command,
	))
	require.NoError(t, db.Exec(`
		UPDATE member
		SET primary_email = NULL, available_emails = '{}'::text[]
		WHERE id = ?::uuid
	`, memberID).Error)

	replayPublisher := &recordingDeletionFanout{}
	require.NoError(t, processUserDeleteIdentity(
		t.Context(), db, databaseIdentityDeleter{db: db}, integrationSpiceDB(t),
		memberDeletionIntegrationAdapter{}, replayPublisher, apitelemetry.NewDurableWriter(db), command,
	))
	require.Empty(t, replayPublisher.avatars)
	require.Len(t, replayPublisher.emails, 1)
}

func TestProcessUserDeleteIdentityResumesAfterIdentityDeletionBeforeTombstone(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	primaryEmail := "crash-replay@example.test"
	nickname := "Crash Replay"
	seedDeletionIdentity(t, db, identityID, memberID, primaryEmail)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text])
	`, memberID, identityID, nickname, primaryEmail, primaryEmail).Error)
	require.NoError(t, db.Exec(`DELETE FROM kratos.identities WHERE id = ?::uuid`, identityID).Error)

	publisher := &recordingDeletionFanout{}
	require.NoError(t, processUserDeleteIdentity(
		t.Context(),
		db,
		databaseIdentityDeleter{db: db},
		integrationSpiceDB(t),
		memberDeletionIntegrationAdapter{},
		publisher,
		apitelemetry.NewDurableWriter(db),
		&managev1.UserDeleteIdentityCommand{
			Mode:              managev1.UserDeleteIdentityMode_TOMBSTONE,
			MemberId:          memberID,
			IdentityId:        identityID,
			NotificationEmail: &primaryEmail,
			NotificationName:  &nickname,
		},
	))

	var member model.Member
	require.NoError(t, db.Where("id = ?::uuid", memberID).Take(&member).Error)
	require.Nil(t, member.AccountIdentityID)
	require.NotNil(t, member.DeletedAt)
	require.Empty(t, publisher.avatars)
	require.Len(t, publisher.emails, 1)
}

func TestProcessUserDeleteIdentitySkipsCompletionMailWhenNeverOnboarded(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	primaryEmail := "unonboarded-delete@example.test"
	seedDeletionIdentity(t, db, identityID, memberID, primaryEmail)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails)
		VALUES (?::uuid, ?::uuid, ?, FALSE, ?, ARRAY[?::text])
	`, memberID, identityID, memberID, primaryEmail, primaryEmail).Error)

	publisher := &recordingDeletionFanout{}
	require.NoError(t, processUserDeleteIdentity(
		t.Context(),
		db,
		databaseIdentityDeleter{db: db},
		integrationSpiceDB(t),
		memberDeletionIntegrationAdapter{},
		publisher,
		apitelemetry.NewDurableWriter(db),
		&managev1.UserDeleteIdentityCommand{
			Mode:              managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE,
			MemberId:          memberID,
			IdentityId:        identityID,
			NotificationEmail: &primaryEmail,
			NotificationName:  &memberID,
		},
	))
	require.Empty(t, publisher.emails)
}

func seedDeletionIdentity(t *testing.T, db *gorm.DB, identityID, memberID, email string) {
	t.Helper()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID: identityID, Email: email, State: auth.KratosStateInactive,
	})
	require.NoError(t, db.Exec(
		`UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid`,
		memberID,
		identityID,
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO account_identity (id) VALUES (?::uuid)`,
		identityID,
	).Error)
}
