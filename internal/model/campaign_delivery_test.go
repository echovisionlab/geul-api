package model

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCampaignDeliveryRecipientBeforeCreateEnforcesContextAttribution(t *testing.T) {
	identityID := "identity-id"
	memberID := "member-id"

	tests := []struct {
		name           string
		contextType    string
		identityID     *string
		memberID       *string
		wantValidation bool
	}{
		{
			name:        "newsletter subscription",
			contextType: "newsletter_subscription",
			identityID:  &identityID,
			memberID:    &memberID,
		},
		{
			name:        "account current",
			contextType: "account_current",
			identityID:  &identityID,
			memberID:    &memberID,
		},
		{
			name:           "missing context",
			wantValidation: true,
		},
		{
			name:           "unknown context",
			contextType:    "unknown_context",
			identityID:     &identityID,
			memberID:       &memberID,
			wantValidation: true,
		},
		{
			name:           "newsletter subscription without identity",
			contextType:    "newsletter_subscription",
			memberID:       &memberID,
			wantValidation: true,
		},
		{
			name:           "account current without identity",
			contextType:    "account_current",
			memberID:       &memberID,
			wantValidation: true,
		},
		{
			name:           "newsletter subscription without member",
			contextType:    "newsletter_subscription",
			identityID:     &identityID,
			wantValidation: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recipient := CampaignDeliveryRecipient{
				RecipientEmail:       "recipient@example.test",
				IdentityID:           tt.identityID,
				MemberID:             tt.memberID,
				RecipientContextType: tt.contextType,
			}
			err := recipient.BeforeCreate(nil)
			if tt.wantValidation {
				require.ErrorIs(t, err, gorm.ErrInvalidData)
				return
			}
			require.NoError(t, err)
			require.Equal(
				t,
				"recipient@example.test",
				recipient.NormalizedRecipientEmail,
			)
			require.Equal(t, "pending", recipient.Status)
		})
	}
}
