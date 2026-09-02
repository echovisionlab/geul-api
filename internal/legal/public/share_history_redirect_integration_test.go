//go:build integration

package public_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	sharelinkpublic "github.com/echovisionlab/geul-api/internal/sharelink/public"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestExpiredAutomaticLegalPreviewTokenResolvesOnlyExactPublicHistoryIntegration(t *testing.T) {
	db := newPublicIntegrationDB(t)
	store := newPublicLegalContentBlockStore(t)
	now := time.Now().UTC()
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	active := seedPublicPrivacyPolicy(
		t, db, store, 980001, "Active privacy history", "Active privacy history",
		managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(), &past, nil,
	)
	scheduled := seedPublicPrivacyPolicy(
		t, db, store, 980002, "Scheduled privacy", "Scheduled privacy",
		managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), &future, nil,
	)
	draft := seedPublicTermsPolicy(
		t, db, store, 990001, "Cancelled terms", "Cancelled terms",
		managev1.TermsStatus_TERMS_STATUS_DRAFT.String(), nil, nil,
	)

	expiredAt := now.Add(-time.Minute)
	automaticToken := "auto-history-" + uuid.NewString()
	manualToken := "manual-history-" + uuid.NewString()
	passwordHash, err := crypto.NewPasswordHasher(nil).Hash("manual-password")
	require.NoError(t, err)
	activeManualToken := "active-manual-" + uuid.NewString()
	links := []model.ShareLink{
		{
			Token:      automaticToken,
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
			EntityID:   active.ID,
			ExpiresAt:  &expiredAt,
			CreatedAt:  now.Add(-2 * time.Hour),
		},
		{
			Token:        manualToken,
			EntityType:   managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
			EntityID:     active.ID,
			PasswordHash: &passwordHash,
			ExpiresAt:    &expiredAt,
			CreatedAt:    now.Add(-2 * time.Hour),
		},
		{
			Token:        activeManualToken,
			EntityType:   managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
			EntityID:     active.ID,
			PasswordHash: &passwordHash,
			ExpiresAt:    &future,
			CreatedAt:    now.Add(-time.Hour),
		},
		{
			Token:      "expired-scheduled-" + uuid.NewString(),
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY.String(),
			EntityID:   scheduled.ID,
			ExpiresAt:  &expiredAt,
			CreatedAt:  now.Add(-2 * time.Hour),
		},
		{
			Token:      "cancelled-draft-" + uuid.NewString(),
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS.String(),
			EntityID:   draft.ID,
			ExpiresAt:  &expiredAt,
			CreatedAt:  now.Add(-2 * time.Hour),
		},
	}
	for i := range links {
		require.NoError(t, db.Create(&links[i]).Error)
	}
	seedSealedAutomaticLegalUpdateRun(
		t,
		db,
		managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY,
		active.ID,
		automaticToken,
		now,
	)

	service := sharelinkpublic.NewService(db, sharelinkadapter.NewPublicTargetResolver(db))
	publicHistory, err := service.Validate(
		t.Context(),
		connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: links[0].Token}),
	)
	require.NoError(t, err)
	require.True(t, publicHistory.Msg.Valid)
	require.Equal(t, active.ID, publicHistory.Msg.GetEntityId())
	require.Equal(t, managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY, publicHistory.Msg.GetEntityType())
	require.False(t, publicHistory.Msg.PasswordRequired)

	for _, link := range links[1:] {
		if link.Token == activeManualToken {
			continue
		}
		response, err := service.Validate(
			t.Context(),
			connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: link.Token}),
		)
		require.NoError(t, err)
		require.False(t, response.Msg.Valid)
	}
	protected, err := service.Validate(
		t.Context(),
		connect.NewRequest(&openv1.ValidateShareLinkRequest{Token: activeManualToken}),
	)
	require.NoError(t, err)
	require.False(t, protected.Msg.Valid)
	require.True(t, protected.Msg.PasswordRequired)
	password := "manual-password"
	opened, err := service.Validate(
		t.Context(),
		connect.NewRequest(&openv1.ValidateShareLinkRequest{
			Token: activeManualToken, Password: &password,
		}),
	)
	require.NoError(t, err)
	require.True(t, opened.Msg.Valid)
}

func seedSealedAutomaticLegalUpdateRun(
	t *testing.T,
	db *gorm.DB,
	entityType managev1.ShareLinkEntityType,
	entityID string,
	token string,
	now time.Time,
) {
	t.Helper()
	eventKey := ""
	run := model.CampaignDeliveryRun{
		RunKind:               "legal_notice",
		Status:                "sent",
		ScheduledAt:           now.Add(-2 * time.Hour),
		CompletedAt:           timePtr(now.Add(-time.Hour)),
		DefinitionSealed:      true,
		SnapshotSchemaVersion: 1,
		TargetQueryVersion:    2,
		TargetMode:            "all_users",
		TargetRecipientScope:  "ALL_MATCHING_USERS",
		SourceTemplateUpdatedAt: timePtr(
			now.Add(-3 * time.Hour),
		),
		RenderSnapshot: model.JSONFields{
			"subject":       "Legal update",
			"content_html":  "<p>Legal update</p>",
			"source_locale": "en",
			"translations": []structured.Fields{{
				"locale":       "en",
				"subject":      "Legal update",
				"content_html": "<p>Legal update</p>",
			}},
		},
		TemplateData: model.JSONFields{
			"policy_title":   "Legal update",
			"effective_date": now.Format("2006-01-02"),
			"preview_url":    "https://example.test/s/" + token,
		},
	}
	var version int32
	switch entityType {
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_PRIVACY:
		eventKey = "privacy_update"
		require.NoError(t, db.Table("privacy_history").
			Select("version").Where("id = ?", entityID).Scan(&version).Error)
		run.PrivacyID = &entityID
		run.SourcePrivacyVersion = &version
	case managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_TERMS:
		eventKey = "terms_update"
		require.NoError(t, db.Table("terms_history").
			Select("version").Where("id = ?", entityID).Scan(&version).Error)
		run.TermsID = &entityID
		run.SourceTermsVersion = &version
	default:
		t.Fatalf("unsupported legal entity type %s", entityType.String())
	}
	run.TemplateEventKey = &eventKey
	require.NoError(t, db.Create(&run).Error)
}

func timePtr(value time.Time) *time.Time {
	return &value
}
