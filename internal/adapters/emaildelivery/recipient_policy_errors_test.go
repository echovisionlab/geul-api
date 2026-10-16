package emaildeliveryadapter

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecipientMemberLookupDistinguishesMissingFromDependencyFailure(t *testing.T) {
	identityID := "11111111-1111-4111-8111-111111111111"
	memberID := "22222222-2222-4222-8222-222222222222"
	lookupErr := errors.New("database unavailable")
	for _, newsletter := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			name := "account"
			if newsletter {
				name = "newsletter"
			}
			if missing {
				name += "/missing"
			} else {
				name += "/unavailable"
			}
			t.Run(name, func(t *testing.T) {
				sqlDB, mock, err := sqlmock.New()
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
				db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				require.NoError(t, err)
				query := mock.ExpectQuery(`SELECT .* FROM "member"`)
				if missing {
					query.WillReturnRows(sqlmock.NewRows([]string{"id"}))
				} else {
					query.WillReturnError(lookupErr)
				}
				identity := &auth.Identity{ID: identityID, ExternalID: memberID, State: auth.KratosStateActive}
				policy := NewRecipientPolicy(db, recipientLoggingIdentityManager{identity: identity})
				job := &managev1.SendEmailEvent{Recipient: "reader@example.test", TemplateType: "primary_email_changed", RecipientContext: email.AccountSelectedPrimaryEmailContext(identityID)}
				if newsletter {
					job.TemplateType = "campaign:newsletter"
					job.RecipientContext = &managev1.SendEmailEvent_NewsletterSubscription{NewsletterSubscription: &managev1.NewsletterSubscriptionRecipient{IdentityId: identityID, MemberId: memberID}}
				}
				decision, err := policy.Authorize(t.Context(), job)
				if missing {
					require.NoError(t, err)
					require.True(t, decision.Blocked)
					require.Equal(t, "member_identity_link_mismatch", decision.Reason)
				} else {
					require.ErrorIs(t, err, lookupErr)
					require.False(t, decision.Blocked)
					require.Empty(t, decision.Reason)
				}
				require.NoError(t, mock.ExpectationsWereMet())
				mock.ExpectClose()
			})
		}
	}
}
