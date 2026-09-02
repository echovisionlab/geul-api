package public

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/member"
	"github.com/echovisionlab/geul-api/internal/model"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const publicNewsletterTokenTestSecret = "public-newsletter-token-test-secret-at-least-32-bytes"

func TestNewsletterUnsubscribeIsTokenOnlyInvalidSafeAndIdempotent(t *testing.T) {
	db := newPublicNewsletterTestDB(t)
	identityID := uuid.NewString()
	require.NoError(t, db.Create(&model.NewsletterSubscription{
		IdentityID:   identityID,
		SubscribedAt: time.Now().UTC(),
	}).Error)
	service := NewNewsletterService(db, publicNewsletterTokenTestSecret)

	_, err := service.Unsubscribe(
		context.Background(),
		connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: "invalid"}),
	)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	token := crypto.GenerateSignedToken(
		member.NewsletterUnsubscribeTokenID(identityID),
		crypto.PurposeUnsubscribe,
		time.Time{},
		publicNewsletterTokenTestSecret,
	)
	request := connect.NewRequest(&openv1.UnsubscribeNewsletterRequest{Token: token})
	for range 2 {
		response, err := service.Unsubscribe(context.Background(), request)
		require.NoError(t, err)
		require.True(t, response.Msg.GetSuccess())
	}

	var count int64
	require.NoError(t, db.Model(&model.NewsletterSubscription{}).
		Where("identity_id = ?", identityID).
		Count(&count).Error)
	require.Zero(t, count)
}

func newPublicNewsletterTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE newsletter_subscription (
			identity_id TEXT PRIMARY KEY,
			subscribed_at DATETIME NOT NULL
		)
	`).Error)
	return db
}
