//go:build integration

package public

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestNewsletterTokenUnsubscribeAuditsOnlyCommittedTransitionIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	identityID, memberID := createNewsletterAuditMember(t, db)
	require.NoError(t, db.Create(&model.NewsletterSubscription{IdentityID: identityID}).Error)
	service := NewAuditedNewsletterService(db, publicNewsletterTokenTestSecret, apitelemetry.NewDurableWriter(db))
	token := crypto.GenerateSignedToken(
		member.NewsletterUnsubscribeTokenID(identityID),
		crypto.PurposeUnsubscribe,
		time.Time{},
		publicNewsletterTokenTestSecret,
	)
	request := connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: token})

	response, err := service.Unsubscribe(t.Context(), request)
	require.NoError(t, err)
	require.True(t, response.Msg.GetSuccess())
	response, err = service.Unsubscribe(t.Context(), request)
	require.NoError(t, err)
	require.True(t, response.Msg.GetSuccess())

	var records []struct {
		Action        string
		ActorKind     string
		ActorMemberID string `gorm:"column:actor_member_id"`
		TargetID      string `gorm:"column:target_id"`
		Attributes    []byte `gorm:"column:attributes"`
	}
	require.NoError(t, db.Raw(`
		SELECT action, actor_kind, actor_member_id::text AS actor_member_id, target_id, attributes
		FROM public.domain_audit
		WHERE action = ? AND target_type = 'account' AND target_id = ?
	`, sharedtelemetry.AuditAccountUpdated, memberID).Scan(&records).Error)
	require.Len(t, records, 1)
	require.Equal(t, string(sharedtelemetry.AuditAccountUpdated), records[0].Action)
	require.Equal(t, string(sharedtelemetry.ActorKindMember), records[0].ActorKind)
	require.Equal(t, memberID, records[0].ActorMemberID)
	require.Equal(t, memberID, records[0].TargetID)
	attributes := map[string]any{}
	require.NoError(t, json.Unmarshal(records[0].Attributes, &attributes))
	require.Equal(t, []any{"newsletter_subscription"}, attributes["changed_fields"])
	require.Equal(t, "subscribed", attributes["previous_state"])
	require.Equal(t, "unsubscribed", attributes["new_state"])
	require.Len(t, attributes, 3)

	var subscriptions int64
	require.NoError(t, db.Model(&model.NewsletterSubscription{}).Where("identity_id = ?", identityID).Count(&subscriptions).Error)
	require.Zero(t, subscriptions)
}

func TestNewsletterTokenUnsubscribeAuditFailureRollsBackIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	identityID, _ := createNewsletterAuditMember(t, db)
	require.NoError(t, db.Create(&model.NewsletterSubscription{IdentityID: identityID}).Error)
	service := NewAuditedNewsletterService(db, publicNewsletterTokenTestSecret, failingNewsletterAuditAppender{})
	token := crypto.GenerateSignedToken(
		member.NewsletterUnsubscribeTokenID(identityID),
		crypto.PurposeUnsubscribe,
		time.Time{},
		publicNewsletterTokenTestSecret,
	)

	_, err := service.Unsubscribe(t.Context(), connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: token}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var subscriptions int64
	require.NoError(t, db.Model(&model.NewsletterSubscription{}).Where("identity_id = ?", identityID).Count(&subscriptions).Error)
	require.Equal(t, int64(1), subscriptions)
}

func TestNewsletterTokenUnsubscribeMissingMemberRollsBackIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: "orphan@example.test"})
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	require.NoError(t, db.Create(&model.NewsletterSubscription{IdentityID: identityID}).Error)
	service := NewAuditedNewsletterService(db, publicNewsletterTokenTestSecret, apitelemetry.NewDurableWriter(db))
	token := crypto.GenerateSignedToken(
		member.NewsletterUnsubscribeTokenID(identityID),
		crypto.PurposeUnsubscribe,
		time.Time{},
		publicNewsletterTokenTestSecret,
	)

	_, err := service.Unsubscribe(t.Context(), connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: token}))
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
	var subscriptions int64
	require.NoError(t, db.Model(&model.NewsletterSubscription{}).Where("identity_id = ?", identityID).Count(&subscriptions).Error)
	require.Equal(t, int64(1), subscriptions)
	require.Zero(t, newsletterAccountAuditCount(t, db))
}

func createNewsletterAuditMember(t *testing.T, db *gorm.DB) (string, string) {
	t.Helper()
	identityID := uuid.NewString()
	memberID := uuid.NewString()
	email := memberID + "@example.test"
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: identityID, Email: email, Name: "newsletter-member"})
	require.NoError(t, db.Exec("UPDATE kratos.identities SET external_id = ? WHERE id = ?::uuid", memberID, identityID).Error)
	require.NoError(t, db.Exec("INSERT INTO account_identity (id) VALUES (?::uuid)", identityID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO member (id, account_identity_id, nickname, onboarded, primary_email, available_emails, social_links)
		VALUES (?::uuid, ?::uuid, ?, TRUE, ?, ARRAY[?::text], '{}'::jsonb)
	`, memberID, identityID, "newsletter-member", email, email).Error)
	return identityID, memberID
}

func newsletterAccountAuditCount(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("public.domain_audit").
		Where("action = ? AND target_type = 'account'", sharedtelemetry.AuditAccountUpdated).
		Count(&count).Error)
	return count
}

type failingNewsletterAuditAppender struct{}

func (failingNewsletterAuditAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("append newsletter audit")
}
