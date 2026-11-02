//go:build integration

package accountpostgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/account"
	"github.com/echovisionlab/geul-api/internal/account/accounttest"
	accountpostgres "github.com/echovisionlab/geul-api/internal/account/postgres"
	accountadapter "github.com/echovisionlab/geul-api/internal/adapters/account"
	"github.com/echovisionlab/geul-api/internal/authz/authztest"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/mutation"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"

	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// These tests check the production adapters behind the Account Store on
// PostgreSQL alone: the deletion commands enqueued through the transaction,
// the Member retention SQL the scheduler and the worker reach through the
// Member deletion port, and the schema of the request rows.

type workerDB struct {
	db    *gorm.DB
	sqlDB *sql.DB
	units account.Units
}

func openWorkerDB(t *testing.T) workerDB {
	t.Helper()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	sqlDB, err := db.DB()
	require.NoError(t, err)
	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	units, err := accountpostgres.NewUnits(db, accountpostgres.Ports{
		Members: accountadapter.MemberDeletion{}, MemberEmails: accountadapter.MemberEmailProjection{},
		Newsletter: accountadapter.NewsletterSubscription{}, Publisher: publisher,
	})
	require.NoError(t, err)
	return workerDB{db: db, sqlDB: sqlDB, units: units}
}

func (w workerDB) commands(t *testing.T) []*managev1.UserDeleteIdentityCommand {
	t.Helper()
	messages, err := pgtest.ReadPGMQ(t.Context(), w.sqlDB, eventpkg.QueueUserDeleteIdentity, time.Minute, 10)
	require.NoError(t, err)
	commands := make([]*managev1.UserDeleteIdentityCommand, len(messages))
	for i, message := range messages {
		payload, err := message.Envelope.Payload()
		require.NoError(t, err)
		commands[i] = &managev1.UserDeleteIdentityCommand{}
		require.NoError(t, proto.Unmarshal(payload, commands[i]))
	}
	return commands
}

func TestAccountEmailChangeRequestMatchesCurrentMigrationSchemaIntegration(t *testing.T) {
	t.Parallel()
	db := pgtest.SetupAppPostgres(t, pgtest.AppPostgresOptions{BootstrapKratosStub: true, ApplyAppSchemaSQL: true}).DB
	var columns []string
	require.NoError(t, db.Raw(`
		SELECT column_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'account_email_change_request'
		ORDER BY ordinal_position
	`).Scan(&columns).Error)
	require.Equal(t, []string{"id", "identity_id", "previous_email_address", "requested_email_address", "created_at", "member_id"}, columns)
}

// A deletion command is written through the Account transaction, so a
// rollback after it drops the command with the rest.
func TestDeletionCommandsRollBackWithTheirTransactionIntegration(t *testing.T) {
	t.Parallel()
	w := openWorkerDB(t)
	memberID, identityID, _ := seedPair(t, w.db, false)
	rollback := errors.New("roll the transaction back")
	command := account.DeleteIdentityCommand{Mode: account.DeleteIdentityUnonboardedHardDelete, MemberID: memberID, IdentityID: identityID}

	err := w.units.Transaction(t.Context(), false, func(tx mutation.Tx[account.Store]) error {
		require.NoError(t, tx.Store().EnqueueUserDeleteIdentity(t.Context(), command))
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	require.Empty(t, w.commands(t))

	require.NoError(t, w.units.Transaction(t.Context(), false, func(tx mutation.Tx[account.Store]) error {
		return tx.Store().EnqueueUserDeleteIdentity(t.Context(), command)
	}))
	commands := w.commands(t)
	require.Len(t, commands, 1)
	require.Equal(t, managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE, commands[0].GetMode())
	require.Equal(t, memberID, commands[0].GetMemberId())
	require.Equal(t, identityID, commands[0].GetIdentityId())
}

// The retention cutoff and the onboarding predicate are the Member's SQL;
// the scheduler commits one durable command and changes nothing else.
func TestEnqueueExpiredUnonboardedMembersCommitsOnlyDurableCommandsIntegration(t *testing.T) {
	t.Parallel()
	w := openWorkerDB(t)
	now := time.Now().UTC()
	targetID, targetIdentityID, _ := seedPair(t, w.db, false)
	recentID, _, _ := seedPair(t, w.db, false)
	onboardedID, _, _ := seedPair(t, w.db, true)
	for memberID, created := range map[string]time.Time{
		targetID:    now.Add(-account.UnonboardedMemberRetention - time.Hour),
		recentID:    now.Add(-account.UnonboardedMemberRetention + time.Hour),
		onboardedID: now.Add(-account.UnonboardedMemberRetention - time.Hour),
	} {
		require.NoError(t, w.db.Exec("UPDATE member SET created_at = ?, updated_at = ? WHERE id = ?::uuid", created, now, memberID).Error)
	}

	queued, err := account.EnqueueExpiredUnonboardedMembers(t.Context(), w.units, now, account.UnonboardedMemberCleanupBatchSize)

	require.NoError(t, err)
	require.Equal(t, 1, queued)
	commands := w.commands(t)
	require.Len(t, commands, 1)
	require.Equal(t, targetID, commands[0].GetMemberId())
	require.Equal(t, targetIdentityID, commands[0].GetIdentityId())
	require.Equal(t, managev1.UserDeleteIdentityMode_UNONBOARDED_HARD_DELETE, commands[0].GetMode())
	var identities, members int64
	require.NoError(t, w.db.Raw("SELECT COUNT(*) FROM kratos.identities WHERE id = ?::uuid", targetIdentityID).Scan(&identities).Error)
	require.NoError(t, w.db.Raw("SELECT COUNT(*) FROM member WHERE id = ?::uuid", targetID).Scan(&members).Error)
	require.Equal(t, int64(1), identities)
	require.Equal(t, int64(1), members)
}

type databaseIdentityDeleter struct{ db *gorm.DB }

func (d databaseIdentityDeleter) DeleteIdentity(ctx context.Context, identityID string) error {
	return d.db.WithContext(ctx).Exec("DELETE FROM kratos.identities WHERE id = ?::uuid", identityID).Error
}

func (w workerDB) deletion(emails *accounttest.Emails) account.UserDeletion {
	return account.UserDeletion{
		Units: w.units, Identities: databaseIdentityDeleter{db: w.db}, SpiceDB: accounttest.NewAuthorizer(&authztest.Checker{}),
		Members: &accounttest.Members{}, Emails: emails, Audit: apitelemetry.NewDurableWriter(w.db),
	}
}

// The tombstone keeps the Member's name and email projection and scrubs its
// profile, newsletter subscription and personal access tokens; the cookie
// consent history stays.
func TestProcessUserDeleteIdentityPreservesNameAndMemberEmailsIntegration(t *testing.T) {
	t.Parallel()
	w := openWorkerDB(t)
	memberID, identityID, primaryEmail := seedPair(t, w.db, true)
	secondaryEmail := "secondary@example.test"
	nickname := "Preserved name"
	require.NoError(t, w.db.Exec(`
		UPDATE member SET nickname = ?, available_emails = ARRAY[?::text, ?::text], bio = 'private bio',
			website = 'https://example.test', social_links = '{"github":"member"}'::jsonb, preferred_locale = 'ko'
		WHERE id = ?::uuid
	`, nickname, primaryEmail, secondaryEmail, memberID).Error)
	require.NoError(t, w.db.Create(&model.NewsletterSubscription{IdentityID: identityID, SubscribedAt: time.Now().UTC()}).Error)
	consent := model.UserCookieConsent{
		ID: uuid.NewString(), MemberID: memberID, Essential: true, Analytics: true,
		ConsentVersion: 2, Source: "settings", RecordedAt: time.Now().UTC(),
	}
	require.NoError(t, w.db.Create(&consent).Error)
	require.NoError(t, w.db.Exec(`
		INSERT INTO member_personal_access_token (selector, member_id, secret_hash, created_at, updated_at)
		VALUES ('AQEBAQEBAQEBAQEBAQEBAQ', ?::uuid, decode(repeat('01', 32), 'hex'), CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
	`, memberID).Error)
	emails := &accounttest.Emails{}

	require.NoError(t, w.deletion(emails).ProcessIdentity(t.Context(), account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityTombstone, MemberID: memberID, IdentityID: identityID,
		NotificationEmail: &primaryEmail, NotificationName: &nickname,
	}))

	var member model.Member
	require.NoError(t, w.db.Where("id = ?::uuid", memberID).Take(&member).Error)
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
	var subscriptions, tokens, anchors int64
	require.NoError(t, w.db.Model(&model.NewsletterSubscription{}).Where("identity_id = ?::uuid", identityID).Count(&subscriptions).Error)
	require.NoError(t, w.db.Table("member_personal_access_token").Where("member_id = ?::uuid", memberID).Count(&tokens).Error)
	require.NoError(t, w.db.Raw("SELECT COUNT(*) FROM account_identity WHERE id = ?::uuid", identityID).Scan(&anchors).Error)
	require.Zero(t, subscriptions)
	require.Zero(t, tokens)
	require.Zero(t, anchors)
	var retained model.UserCookieConsent
	require.NoError(t, w.db.Where("id = ?::uuid", consent.ID).Take(&retained).Error)
	require.Equal(t, memberID, retained.MemberID)
	require.True(t, retained.Analytics)
	require.Equal(t, int32(2), retained.ConsentVersion)
	require.Len(t, emails.Sent(), 1)
	var terminalAudits int64
	require.NoError(t, w.db.Raw(`SELECT COUNT(*) FROM public.domain_audit WHERE action = ? AND target_id = ?`, sharedtelemetry.AuditAccountDeleted, memberID).Scan(&terminalAudits).Error)
	require.Equal(t, int64(1), terminalAudits)
}

// A redelivery after the email retention job scrubbed the tombstone's
// projection still converges and resends the completion mail.
func TestProcessUserDeleteIdentityReplaySucceedsAfterEmailRetentionScrubIntegration(t *testing.T) {
	t.Parallel()
	w := openWorkerDB(t)
	memberID, identityID, primaryEmail := seedPair(t, w.db, true)
	nickname := "Retained Member"
	command := account.DeleteIdentityCommand{
		Mode: account.DeleteIdentityTombstone, MemberID: memberID, IdentityID: identityID,
		NotificationEmail: &primaryEmail, NotificationName: &nickname,
	}
	require.NoError(t, w.deletion(&accounttest.Emails{}).ProcessIdentity(t.Context(), command))
	require.NoError(t, w.db.Exec(`UPDATE member SET primary_email = NULL, available_emails = '{}'::text[] WHERE id = ?::uuid`, memberID).Error)

	replay := &accounttest.Emails{}
	require.NoError(t, w.deletion(replay).ProcessIdentity(t.Context(), command))
	require.Len(t, replay.Sent(), 1)
}
