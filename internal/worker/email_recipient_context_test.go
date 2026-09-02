package worker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func TestEnforceEmailRecipientContextBypassAllowlist(t *testing.T) {
	ctx := context.Background()
	handlers := &Handlers{}

	t.Run("allows explicit system direct reasons", func(t *testing.T) {
		for _, reason := range []string{
			"account_deletion_complete",
			"account_deletion_scheduled",
			"account_recovery_confirm",
			"account_recovery_complete",
			"primary_email_changed",
		} {
			blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
				Recipient:        "ops@example.com",
				TemplateType:     reason,
				RecipientContext: email.SystemDirectEmailContext(reason),
			})

			require.NoError(t, err)
			require.False(t, blocked)
		}
	})

	t.Run("blocks deletion cancellation system direct bypass", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "account@example.com",
			TemplateType:     email.EventAccountDeletionCancelled.String(),
			RecipientContext: email.SystemDirectEmailContext("account_deletion_cancelled"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks unknown system direct reason", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "ops@example.com",
			TemplateType:     "system",
			RecipientContext: email.SystemDirectEmailContext("unknown_reason"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks system direct template mismatch", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "ops@example.com",
			TemplateType:     email.EventVerificationCode.String(),
			RecipientContext: email.SystemDirectEmailContext("account_recovery_confirm"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("allows explicit test actor", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "test@example.com",
			TemplateType:     "template:template-1",
			RecipientContext: email.TestEmailContext("admin-1"),
		})

		require.NoError(t, err)
		require.False(t, blocked)

		handlers.kratosClient = fakeWorkerIdentityManager{}
		blocked, err = handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:    "canonical@example.com",
			TemplateType: email.EventPasskeyAdded.String(),
			RecipientContext: email.AccountSelectedPrimaryEmailContext(
				"identity-security",
			),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks missing test actor", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "test@example.com",
			TemplateType:     "template:template-1",
			RecipientContext: email.TestEmailContext(""),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("requires context", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:    "missing@example.com",
			TemplateType: "missing",
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks context that does not match template class", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "subscriber@example.com",
			TemplateType:     "campaign:campaign-1",
			RecipientContext: email.AccountVerificationContext("identity-1", "subscriber@example.com"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})
}

func TestEnforceEmailRecipientContextAccountCurrentEmail(t *testing.T) {
	ctx := context.Background()

	t.Run("allows current verified account email", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-1", "user@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-1",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "User@Example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: true},
				},
				Credentials: map[string]auth.Credential{
					"code": {Type: "code", Identifiers: []string{"user@example.com"}},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "user@example.com",
			TemplateType:     email.EventWelcome.String(),
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-1"),
		})

		require.NoError(t, err)
		require.False(t, blocked)
	})

	t.Run("blocks a passkey notice whose exact mutation was not committed", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-security-stale", "canonical@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-security-stale",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "canonical@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "canonical@example.com", Verified: true},
				},
				Credentials: map[string]auth.Credential{
					"code": {Type: "code", Identifiers: []string{"canonical@example.com"}},
					"passkey": {
						Type: "passkey",
						Config: structured.Fields{"credentials": structured.Values{
							structured.Fields{"id": "passkey-1"},
						}},
					},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "canonical@example.com",
			TemplateType:     email.EventPasskeyAdded.String(),
			TemplateData:     map[string]string{"_credential_count": "2"},
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-security-stale"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("requires exact social credential context", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-social", "canonical@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-social",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "canonical@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "canonical@example.com", Verified: true},
				},
				Credentials: map[string]auth.Credential{"oidc": {
					Type: "oidc",
					Config: structured.Fields{"providers": structured.Values{
						structured.Fields{"provider": "google", "subject": "subject-1"},
					}},
				}},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "canonical@example.com",
			TemplateType:     email.EventSocialLoginAdded.String(),
			TemplateData:     map[string]string{"provider": "google", "_provider_subject": "different-subject"},
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-social"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks a legacy noncanonical Member primary projection", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-selected", "delivery@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-selected",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "canonical@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "canonical@example.com", Verified: true},
					{Via: "email", Value: "delivery@example.com", Verified: true},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "delivery@example.com",
			TemplateType:     email.EventWelcome.String(),
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-selected"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("security notice uses the canonical Member primary projection", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-security", "canonical@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-security",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "canonical@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "canonical@example.com", Verified: true},
					{Via: "email", Value: "delivery@example.com", Verified: true},
				},
				Credentials: map[string]auth.Credential{
					"code": {Type: "code", Identifiers: []string{"canonical@example.com"}},
					"passkey": {
						Type: "passkey",
						Config: structured.Fields{"credentials": structured.Values{
							structured.Fields{"id": "passkey-1"},
						}},
					},
				},
			}},
		}

		job := &managev1.SendEmailEvent{
			Recipient:    "canonical@example.com",
			TemplateType: email.EventPasskeyAdded.String(),
			TemplateData: map[string]string{"_credential_count": "1"},
			RecipientContext: email.AccountSelectedPrimaryEmailContext(
				"identity-security",
			),
		}
		blocked, err := handlers.enforceEmailRecipientContext(ctx, job)

		require.NoError(t, err)
		require.False(t, blocked)
		require.NotContains(t, job.TemplateData, "_credential_count")
		require.NoError(t, emailauthoring.ValidateAutomaticEmailTemplateData(job.TemplateType, job.TemplateData))
	})

	t.Run("blocks stale account email", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-2", "new@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-2",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "new@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "new@example.com", Verified: true},
					{Via: "email", Value: "old@example.com", Verified: true},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "old@example.com",
			TemplateType:     email.EventWelcome.String(),
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-2"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks unverified account email", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-3", "user@example.com")
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-3",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "user@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: false},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "user@example.com",
			TemplateType:     email.EventWelcome.String(),
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-3"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("blocks account mail before Member onboarding", func(t *testing.T) {
		db, memberID := activeMemberEmailTestDB(t, "identity-unonboarded", "user@example.com")
		require.NoError(t, db.Exec("UPDATE member SET onboarded = FALSE WHERE id = ?", memberID).Error)
		handlers := &Handlers{
			db: db,
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:         "identity-unonboarded",
				ExternalID: memberID,
				Traits:     structured.Fields{"email": "user@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: true},
				},
			}},
		}
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "user@example.com",
			TemplateType:     email.EventWelcome.String(),
			RecipientContext: email.AccountSelectedPrimaryEmailContext("identity-unonboarded"),
		})
		require.NoError(t, err)
		require.True(t, blocked)
	})
}

func TestEnforceEmailRecipientContextNewsletterSubscriptionRechecksCurrentRow(t *testing.T) {
	db, err := gorm.Open(
		sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"),
		&gorm.Config{},
	)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			onboarded BOOLEAN NOT NULL DEFAULT TRUE,
			deleted_at DATETIME
		);
		CREATE TABLE newsletter_subscription (
			identity_id TEXT PRIMARY KEY,
			subscribed_at DATETIME NOT NULL
		)
	`).Error)

	identityID := uuid.NewString()
	memberID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO member (id, account_identity_id, primary_email) VALUES (?, ?, ?)",
		memberID,
		identityID,
		"delivery@example.com",
	).Error)
	require.NoError(t, db.Create(&model.NewsletterSubscription{
		IdentityID:   identityID,
		SubscribedAt: time.Now().UTC(),
	}).Error)
	handlers := &Handlers{
		db: db,
		kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
			ID:         identityID,
			ExternalID: memberID,
			Traits:     structured.Fields{"email": "delivery@example.com"},
			VerifiableAddresses: []auth.VerifiableAddress{
				{Via: "email", Value: "delivery@example.com", Verified: true},
			},
			Credentials: map[string]auth.Credential{
				"oidc": {
					Type: "oidc",
					Config: structured.Fields{
						"providers": structured.Values{structured.Fields{
							"provider": "google", "subject": "google-subject",
							"email": "delivery@example.com", "email_verified": true,
						}},
					},
				},
			},
		}},
	}
	job := &managev1.SendEmailEvent{
		Recipient:        "delivery@example.com",
		TemplateType:     "campaign:campaign-1",
		RecipientContext: email.NewsletterSubscriptionContext(identityID, memberID),
	}

	blocked, err := handlers.enforceEmailRecipientContext(t.Context(), job)
	require.NoError(t, err)
	require.False(t, blocked)

	require.NoError(t, db.Exec("UPDATE member SET onboarded = FALSE WHERE id = ?", memberID).Error)
	blocked, err = handlers.enforceEmailRecipientContext(t.Context(), job)
	require.NoError(t, err)
	require.True(t, blocked)
	require.NoError(t, db.Exec("UPDATE member SET onboarded = TRUE WHERE id = ?", memberID).Error)

	mismatchedIdentityJob := proto.Clone(job).(*managev1.SendEmailEvent)
	mismatchedIdentityJob.RecipientContext = email.NewsletterSubscriptionContext(uuid.NewString(), memberID)
	blocked, err = handlers.enforceEmailRecipientContext(t.Context(), mismatchedIdentityJob)
	require.NoError(t, err)
	require.True(t, blocked)

	require.NoError(t, db.Where("identity_id = ?", identityID).
		Delete(&model.NewsletterSubscription{}).Error)
	blocked, err = handlers.enforceEmailRecipientContext(t.Context(), job)
	require.NoError(t, err)
	require.True(t, blocked)
}

func activeMemberEmailTestDB(t *testing.T, identityID, primaryEmail string) (*gorm.DB, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`
		CREATE TABLE member (
			id TEXT PRIMARY KEY,
			account_identity_id TEXT,
			primary_email TEXT,
			onboarded BOOLEAN NOT NULL DEFAULT TRUE,
			deleted_at DATETIME
		)
	`).Error)
	memberID := uuid.NewString()
	require.NoError(t, db.Exec(
		"INSERT INTO member (id, account_identity_id, primary_email) VALUES (?, ?, ?)",
		memberID, identityID, primaryEmail,
	).Error)
	return db, memberID
}

func TestEnforceEmailRecipientContextVerificationTargets(t *testing.T) {
	ctx := context.Background()
	handlers := &Handlers{
		kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
			ID:    "identity-1",
			State: auth.KratosStateActive,
			Traits: structured.Fields{
				"email":         "new-account@example.com",
				"pending_email": "target-change@example.com",
			},
			VerifiableAddresses: []auth.VerifiableAddress{
				{Via: "email", Value: "new-account@example.com", Verified: false},
				{Via: "email", Value: "target-change@example.com", Verified: false},
			},
		}},
	}

	t.Run("allows account verification target email", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "new-account@example.com",
			TemplateType:     email.EventVerificationCode.String(),
			RecipientContext: email.AccountVerificationContext("identity-1", "new-account@example.com"),
		})

		require.NoError(t, err)
		require.False(t, blocked)
	})

	t.Run("blocks stale account verification target", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "other@example.com",
			TemplateType:     email.EventVerificationCode.String(),
			RecipientContext: email.AccountVerificationContext("identity-1", "new-account@example.com"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	t.Run("allows pending email verification target", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "target-change@example.com",
			TemplateType:     email.EventVerificationCode.String(),
			RecipientContext: email.AccountVerificationContext("identity-1", "target-change@example.com"),
		})

		require.NoError(t, err)
		require.False(t, blocked)
	})

	t.Run("blocks stale pending email verification target", func(t *testing.T) {
		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "other@example.com",
			TemplateType:     email.EventVerificationCode.String(),
			RecipientContext: email.AccountVerificationContext("identity-1", "target-change@example.com"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})
}

func TestEnforceEmailRecipientContextBlocksObsoleteVerificationDelivery(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name        string
		identity    *auth.Identity
		recipient   string
		buildJob    func(string, string) *managev1.SendEmailEvent
		wantBlocked bool
	}{
		{
			name: "allows current unverified account email",
			identity: &auth.Identity{
				ID:                  "identity-1",
				State:               auth.KratosStateActive,
				Traits:              structured.Fields{"email": "current@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: "current@example.com", Verified: false}},
			},
			recipient: "current@example.com",
			buildJob: func(identityID, target string) *managev1.SendEmailEvent {
				return &managev1.SendEmailEvent{
					Recipient:        target,
					TemplateType:     email.EventVerificationCode.String(),
					RecipientContext: email.AccountVerificationContext(identityID, target),
				}
			},
		},
		{
			name: "blocks replaced pending email",
			identity: &auth.Identity{
				ID:    "identity-1",
				State: auth.KratosStateActive,
				Traits: structured.Fields{
					"email":         "current@example.com",
					"pending_email": "newer@example.com",
				},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "obsolete@example.com", Verified: false},
					{Via: "email", Value: "newer@example.com", Verified: false},
				},
			},
			recipient: "obsolete@example.com",
			buildJob: func(identityID, target string) *managev1.SendEmailEvent {
				return &managev1.SendEmailEvent{
					Recipient:        target,
					TemplateType:     email.EventVerificationCode.String(),
					RecipientContext: email.AccountVerificationContext(identityID, target),
				}
			},
			wantBlocked: true,
		},
		{
			name: "blocks already verified address",
			identity: &auth.Identity{
				ID:                  "identity-1",
				State:               auth.KratosStateActive,
				Traits:              structured.Fields{"email": "verified@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: "verified@example.com", Verified: true}},
			},
			recipient: "verified@example.com",
			buildJob: func(identityID, target string) *managev1.SendEmailEvent {
				return &managev1.SendEmailEvent{
					Recipient:        target,
					TemplateType:     email.EventVerificationCode.String(),
					RecipientContext: email.AccountVerificationContext(identityID, target),
				}
			},
			wantBlocked: true,
		},
		{
			name: "blocks inactive identity",
			identity: &auth.Identity{
				ID:                  "identity-1",
				State:               auth.KratosStateInactive,
				Traits:              structured.Fields{"email": "inactive@example.com"},
				VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: "inactive@example.com", Verified: false}},
			},
			recipient: "inactive@example.com",
			buildJob: func(identityID, target string) *managev1.SendEmailEvent {
				return &managev1.SendEmailEvent{
					Recipient:        target,
					TemplateType:     email.EventVerificationCode.String(),
					RecipientContext: email.AccountVerificationContext(identityID, target),
				}
			},
			wantBlocked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handlers := &Handlers{kratosClient: fakeWorkerIdentityManager{identity: tt.identity}}
			job := tt.buildJob(tt.identity.ID, tt.recipient)
			blocked, err := handlers.enforceEmailRecipientContext(ctx, job)

			require.NoError(t, err)
			require.Equal(t, tt.wantBlocked, blocked)
		})
	}
}

func TestEnforceEmailRecipientContextPasswordlessFlows(t *testing.T) {
	ctx := context.Background()
	loginContext := func(identityID, targetEmail string) *managev1.SendEmailEvent_AuthLogin {
		return &managev1.SendEmailEvent_AuthLogin{
			AuthLogin: &managev1.AuthLoginRecipient{
				IdentityId:  identityID,
				TargetEmail: targetEmail,
			},
		}
	}
	registrationContext := func(targetEmail string) *managev1.SendEmailEvent_AuthRegistration {
		return &managev1.SendEmailEvent_AuthRegistration{
			AuthRegistration: &managev1.AuthRegistrationRecipient{
				TargetEmail: targetEmail,
			},
		}
	}

	t.Run("allows unverified email when it is an identity code address", func(t *testing.T) {
		handlers := &Handlers{
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:    "identity-1",
				State: auth.KratosStateActive,
				Credentials: map[string]auth.Credential{
					"code": {Type: "code", Identifiers: []string{"user@example.com"}},
				},
				VerifiableAddresses: []auth.VerifiableAddress{
					{Via: "email", Value: "user@example.com", Verified: false},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "user@example.com",
			TemplateType:     email.EventLoginCode.String(),
			RecipientContext: loginContext("identity-1", "user@example.com"),
		})

		require.NoError(t, err)
		require.False(t, blocked)
	})

	t.Run("blocks login recipient absent from code credential", func(t *testing.T) {
		handlers := &Handlers{
			kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
				ID:    "identity-1",
				State: auth.KratosStateActive,
				Credentials: map[string]auth.Credential{
					"code": {Type: "code", Identifiers: []string{"other@example.com"}},
				},
			}},
		}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "user@example.com",
			TemplateType:     email.EventLoginCode.String(),
			RecipientContext: loginContext("identity-1", "user@example.com"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})

	for _, tc := range []struct {
		name     string
		identity *auth.Identity
	}{
		{
			name: "blocks login for banned identity",
			identity: &auth.Identity{
				ID:            "identity-banned",
				State:         auth.KratosStateActive,
				MetadataAdmin: structured.Fields{"banned": true},
			},
		},
		{
			name: "blocks login for inactive identity",
			identity: &auth.Identity{
				ID:    "identity-inactive",
				State: auth.KratosStateInactive,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.identity.Credentials = map[string]auth.Credential{
				"code": {Type: "code", Identifiers: []string{"user@example.com"}},
			}
			handlers := &Handlers{
				kratosClient: fakeWorkerIdentityManager{identity: tc.identity},
			}

			blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
				Recipient:        "user@example.com",
				TemplateType:     email.EventLoginCode.String(),
				RecipientContext: loginContext(tc.identity.ID, "user@example.com"),
			})

			require.NoError(t, err)
			require.True(t, blocked)
		})
	}

	t.Run("allows authenticated courier registration without account or subscriber lookup", func(t *testing.T) {
		handlers := &Handlers{}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "new@example.com",
			TemplateType:     email.EventRegistrationCode.String(),
			RecipientContext: registrationContext("new@example.com"),
		})

		require.NoError(t, err)
		require.False(t, blocked)
	})

	t.Run("blocks registration target mismatch", func(t *testing.T) {
		handlers := &Handlers{}

		blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
			Recipient:        "other@example.com",
			TemplateType:     email.EventRegistrationCode.String(),
			RecipientContext: registrationContext("new@example.com"),
		})

		require.NoError(t, err)
		require.True(t, blocked)
	})
}

func TestEnforceEmailRecipientContextVerificationTemplateMatrix(t *testing.T) {
	ctx := context.Background()
	targetEmail := "target@example.com"
	handlers := &Handlers{
		kratosClient: fakeWorkerIdentityManager{identity: &auth.Identity{
			ID:    "identity-1",
			State: auth.KratosStateActive,
			Traits: structured.Fields{
				"email":         targetEmail,
				"pending_email": targetEmail,
			},
			VerifiableAddresses: []auth.VerifiableAddress{{Via: "email", Value: targetEmail, Verified: false}},
		}},
	}

	templates := []struct {
		name         string
		templateType string
	}{
		{name: "auth_verification", templateType: email.EventVerificationCode.String()},
		{name: "account_transactional", templateType: email.EventWelcome.String()},
		{name: "legal_notice", templateType: email.EventTermsEffective.String()},
		{name: "campaign", templateType: "campaign:campaign-1"},
		{name: "direct_test_template", templateType: "template:template-1"},
		{name: "unknown", templateType: "custom"},
	}

	for _, templateCase := range templates {
		t.Run(templateCase.name, func(t *testing.T) {
			blocked, err := handlers.enforceEmailRecipientContext(ctx, &managev1.SendEmailEvent{
				Recipient:        targetEmail,
				TemplateType:     templateCase.templateType,
				RecipientContext: email.AccountVerificationContext("identity-1", targetEmail),
			})

			require.NoError(t, err)
			require.Equal(t, templateCase.templateType != email.EventVerificationCode.String(), blocked)
		})
	}
}

type fakeWorkerIdentityManager struct {
	identity *auth.Identity
	err      error
}

func (f fakeWorkerIdentityManager) GetIdentity(context.Context, string) (*auth.Identity, error) {
	return f.identity, f.err
}

func (f fakeWorkerIdentityManager) GetIdentityWithIncludeCredential(context.Context, string, string) (*auth.Identity, error) {
	return f.identity, f.err
}

func (f fakeWorkerIdentityManager) ListIdentities(context.Context, int, int) ([]*auth.Identity, int64, error) {
	return nil, 0, f.err
}

func (f fakeWorkerIdentityManager) UpdateIdentityTraits(context.Context, string, structured.Fields) error {
	return f.err
}

func (f fakeWorkerIdentityManager) UpdateIdentityVerifiableAddresses(context.Context, string, []auth.VerifiableAddress) error {
	return f.err
}

func (f fakeWorkerIdentityManager) UpdateIdentityMetadataAdmin(context.Context, string, structured.Fields) error {
	return f.err
}

func (f fakeWorkerIdentityManager) SetIdentityState(context.Context, string, string) error {
	return f.err
}

func (f fakeWorkerIdentityManager) DeleteIdentitySessions(context.Context, string) error {
	return f.err
}

func (f fakeWorkerIdentityManager) DeleteIdentity(context.Context, string) error { return f.err }

func (f fakeWorkerIdentityManager) GetIdentityEmail(context.Context, string) (string, error) {
	if f.identity == nil {
		return "", f.err
	}
	if email := f.identity.GetTraitString("email"); email != nil {
		return *email, f.err
	}
	return "", f.err
}
