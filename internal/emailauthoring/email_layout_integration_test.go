//go:build integration

package emailauthoring

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestEmailLayoutServiceListUsesCanonicalSourceBatchAndFailsClosedIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	service := NewEmailLayoutService(
		db, "https://cdn.example.com", "https://example.com", spiceDB,
		WithEmailLayoutCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
		WithEmailLayoutContentBlockStore(store),
	)
	created, err := service.CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Key:          "canonical_list_layout",
		Name:         "Canonical list layout",
		HtmlContent:  "<html><body>{{content}}</body></html>",
		SourceLocale: "en",
	}))
	require.NoError(t, err)

	listed, err := service.ListEmailLayoutsAdmin(
		ctx,
		connect.NewRequest(&managev1.ListEmailLayoutsAdminRequest{}),
	)
	require.NoError(t, err)
	require.Condition(t, func() bool {
		for _, layout := range listed.Msg.Layouts {
			if layout.Id == created.Msg.Id && layout.HtmlContent == created.Msg.HtmlContent {
				return true
			}
		}
		return false
	})

	require.NoError(t, db.Exec(
		"DELETE FROM email_layout_translation WHERE entity_id = ?",
		created.Msg.Id,
	).Error)
	listed, err = service.ListEmailLayoutsAdmin(
		ctx,
		connect.NewRequest(&managev1.ListEmailLayoutsAdminRequest{}),
	)
	require.Nil(t, listed)
	require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
}

func TestEmailLayoutPreviewFallsBackToCanonicalSourceWhenTargetIsMissingIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	service := NewEmailLayoutService(
		db, "https://cdn.example.com", "https://example.com", spiceDB,
		WithEmailLayoutCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
		WithEmailLayoutContentBlockStore(store),
	)
	created, err := service.CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Key:          "preview_resolver_failure",
		Name:         "Preview resolver failure",
		HtmlContent:  "<html><body>{{content}}</body></html>",
		SourceLocale: "en",
	}))
	require.NoError(t, err)

	locale := "ko"
	preview, err := service.PreviewEmailLayout(ctx, connect.NewRequest(&managev1.PreviewEmailLayoutRequest{
		Id:     created.Msg.Id,
		Locale: &locale,
	}))
	require.NoError(t, err)
	require.Contains(t, preview.Msg.Html, "<html><body>")
}

func TestEmailLayoutMutationIsFrozenOnlyWhileDeliveryRunIsActiveIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	service := NewEmailLayoutService(
		db, "https://cdn.example.com", "https://example.com", spiceDB,
		WithEmailLayoutCampaignDeliveryReferences(integrationCampaignDeliveryReferences{}),
		WithEmailLayoutContentBlockStore(store),
	)
	created, err := service.CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Key:          "frozen_layout_" + testutil.IntegrationUUID(),
		Name:         "Frozen delivery layout",
		HtmlContent:  "<html><body>{{content}}</body></html>",
		SourceLocale: "en",
	}))
	require.NoError(t, err)

	now := time.Now().UTC()
	campaignID := testutil.IntegrationUUID()
	campaignDocumentID := testutil.IntegrationUUID()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'email')`,
		campaignDocumentID,
	).Error)
	require.NoError(t, db.Create(&model.Campaign{
		ID: campaignID, ContentDocumentID: &campaignDocumentID,
		Name: "Layout freeze campaign", Subject: "Layout freeze",
		Status: "CAMPAIGN_STATUS_SCHEDULED", TargetMode: model.CampaignTargetModeAll,
		LayoutID: &created.Msg.Id, RecipientScope: "ALL_MATCHING_USERS",
		ScheduledAt: &now, CreatedAt: now, UpdatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&model.CampaignDeliveryRun{
		ID: testutil.IntegrationUUID(), RunKind: "campaign", CampaignID: &campaignID,
		Status: "scheduled", ScheduledAt: now, DefinitionSealed: true,
		RenderSnapshot: model.JSONFields{
			"subject": "Layout freeze", "content_html": "<p>Layout freeze</p>",
			"source_locale": "en", "translations": []any{map[string]any{
				"locale": "en", "subject": "Layout freeze", "content_html": "<p>Layout freeze</p>",
			}},
		}, SnapshotSchemaVersion: 1,
		SourceCampaignUpdatedAt: &now,
		SourceLayoutID:          &created.Msg.Id, SourceLayoutUpdatedAt: &now,
		TargetQueryVersion: 2, TargetMode: "all_users", TargetRecipientScope: "ALL_MATCHING_USERS",
		CreatedAt: now, UpdatedAt: now,
	}).Error)

	renamed := "Frozen delivery layout rename"
	_, err = service.UpdateEmailLayout(ctx, connect.NewRequest(&managev1.UpdateEmailLayoutRequest{
		Id: created.Msg.Id, Name: &renamed,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Where("campaign_id = ?", campaignID).
		Updates(map[string]any{"status": "cancelled", "updated_at": time.Now().UTC()}).Error)
	updated, err := service.UpdateEmailLayout(ctx, connect.NewRequest(&managev1.UpdateEmailLayoutRequest{
		Id: created.Msg.Id, Name: &renamed,
	}))
	require.NoError(t, err)
	require.Equal(t, renamed, updated.Msg.Name)
	require.Equal(t, created.Msg.HtmlContent, updated.Msg.HtmlContent)
}
