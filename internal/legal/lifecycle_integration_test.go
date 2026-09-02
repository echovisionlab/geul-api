//go:build integration

package legal_test

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func newPrivacyServiceForLegalIntegrationTest(
	db *gorm.DB,
	baseURL string,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	options ...legaldomain.PrivacyServiceOption,
) *legaldomain.PrivacyService {
	return legaldomain.NewAuditedPrivacyService(
		db, baseURL, apitelemetry.NewDurableWriter(db), spiceDB,
		legalIntegrationDependencies(db, cdnDomain), options...,
	)
}

func newTermsServiceForLegalIntegrationTest(
	db *gorm.DB,
	baseURL string,
	cdnDomain string,
	spiceDB *auth.SpiceDBClient,
	options ...legaldomain.TermsServiceOption,
) *legaldomain.TermsService {
	return legaldomain.NewAuditedTermsService(
		db, baseURL, apitelemetry.NewDurableWriter(db), spiceDB,
		legalIntegrationDependencies(db, cdnDomain), options...,
	)
}

func legalIntegrationDependencies(db *gorm.DB, cdnDomain string) legaldomain.Dependencies {
	return legaldomain.Dependencies{
		OG:     legaladapter.NewOGRuntime(cdnDomain, newOGPlannerForTest(db, cdnDomain)),
		Notice: legaladapter.NewNoticeRuntime(nil),
	}
}

func TestCancellingOnlyScheduledPrivacyClearsCanonicalOgRouteIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventPrivacyUpdate.String(),
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	svc := newPrivacyServiceForLegalIntegrationTest(
		db, "https://example.com", "https://cdn.example.com", spiceDB,
		legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)

	draft, err := svc.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
		Title:    ptrString("Only scheduled privacy"),
		Document: legalPolicyDocumentFixture("en", "Only scheduled privacy"),
	}))
	require.NoError(t, err)
	scheduled, err := svc.SchedulePrivacy(ctx, connect.NewRequest(&managev1.SchedulePrivacyRequest{
		Id:               draft.Msg.Id,
		EffectiveFrom:    timestamppb.New(time.Now().Add(24 * time.Hour).UTC()),
		ExpectedRevision: draft.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED, scheduled.Msg.Status)

	var beforeTargets []model.OgGenerationTarget
	require.NoError(t, db.Where(
		"entity_type = ? AND entity_id = ?",
		"privacy",
		legaladapter.RouteID("privacy"),
	).Find(&beforeTargets).Error)
	require.NotEmpty(t, beforeTargets)
	for _, target := range beforeTargets {
		require.NotNil(t, target.LatestGenerationID)
	}

	cancelled, err := svc.CancelPrivacySchedule(ctx, connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{Id: draft.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT, cancelled.Msg.Status)

	var afterTargets []model.OgGenerationTarget
	require.NoError(t, db.Where(
		"entity_type = ? AND entity_id = ?",
		"privacy",
		legaladapter.RouteID("privacy"),
	).Find(&afterTargets).Error)
	require.Len(t, afterTargets, len(beforeTargets))
	for _, target := range afterTargets {
		require.Nil(t, target.LatestGenerationID)
	}
	var activeGenerations int64
	require.NoError(t, db.Model(&model.OgGeneration{}).
		Where("target_id IN ? AND status IN ?", targetIDs(afterTargets), []string{
			model.OgGenerationStatusQueued,
			model.OgGenerationStatusProcessing,
		}).Count(&activeGenerations).Error)
	require.Zero(t, activeGenerations)
}

func targetIDs(targets []model.OgGenerationTarget) []string {
	ids := make([]string, len(targets))
	for i := range targets {
		ids[i] = targets[i].ID
	}
	return ids
}

func TestPrivacyScheduleActivationKeepsMailLifecycleIndependentIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventPrivacyUpdate.String(),
	)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventPrivacyEffective.String(),
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	svc := newPrivacyServiceForLegalIntegrationTest(
		db, "https://example.com", "", spiceDB,
		legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)

	activeDraft, err := svc.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
		Title:    ptrString("Active Privacy"),
		Document: legalPolicyDocumentFixture("en", "Active Privacy"),
	}))
	require.NoError(t, err)
	active, err := svc.ActivatePrivacyNow(ctx, connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
		Id: activeDraft.Msg.Id, ExpectedRevision: activeDraft.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE, active.Msg.Status)
	require.NotNil(t, active.Msg.EffectiveFrom)
	var terminalEffectiveRun model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"privacy_id = ? AND template_event_key = ? AND status = ?",
		activeDraft.Msg.Id,
		email.EventPrivacyEffective.String(),
		legaldomain.CampaignDeliveryRunStatusScheduled,
	).First(&terminalEffectiveRun).Error)
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Where("id = ?", terminalEffectiveRun.ID).
		Updates(structured.Fields{
			"status":       legaldomain.CampaignDeliveryRunStatusSent,
			"completed_at": time.Now().UTC(),
		}).Error)
	var supersededEffectiveRun *model.CampaignDeliveryRun
	err = db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		supersededEffectiveRun, createErr = legaladapter.NewNoticeRuntime(nil).CreateRun(
			ctx,
			tx,
			legaldomain.EmailDeliveryReferenceTypePrivacy,
			activeDraft.Msg.Id,
			email.EventPrivacyEffective.String(),
			map[string]string{"privacy_url": "https://example.com/privacy"},
			time.Now().UTC(),
		)
		return createErr
	})
	require.NoError(t, err)

	scheduledDraft, err := svc.CreatePrivacyVersion(ctx, connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
		Title:    ptrString("Scheduled Privacy"),
		Document: legalPolicyDocumentFixture("en", "Scheduled Privacy"),
	}))
	require.NoError(t, err)
	effectiveFrom := time.Now().Add(48 * time.Hour).UTC()
	scheduled, err := svc.SchedulePrivacy(ctx, connect.NewRequest(&managev1.SchedulePrivacyRequest{
		Id:               scheduledDraft.Msg.Id,
		EffectiveFrom:    timestamppb.New(effectiveFrom),
		ExpectedRevision: scheduledDraft.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED, scheduled.Msg.Status)
	require.NotNil(t, scheduled.Msg.EffectiveFrom)

	cancelled, err := svc.CancelPrivacySchedule(ctx, connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{Id: scheduledDraft.Msg.Id}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT, cancelled.Msg.Status)
	require.Nil(t, cancelled.Msg.EffectiveFrom)

	rescheduled, err := svc.SchedulePrivacy(ctx, connect.NewRequest(&managev1.SchedulePrivacyRequest{
		Id:               scheduledDraft.Msg.Id,
		EffectiveFrom:    timestamppb.New(effectiveFrom.Add(time.Hour)),
		ExpectedRevision: scheduledDraft.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED, rescheduled.Msg.Status)
	var rescheduledUpdateRun model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"privacy_id = ? AND template_event_key = ? AND status = ?",
		scheduledDraft.Msg.Id,
		email.EventPrivacyUpdate.String(),
		legaldomain.CampaignDeliveryRunStatusScheduled,
	).First(&rescheduledUpdateRun).Error)

	activated, err := svc.ActivatePrivacyNow(ctx, connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
		Id: scheduledDraft.Msg.Id, ExpectedRevision: scheduledDraft.Msg.Revision,
	}))
	require.NoError(t, err)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE, activated.Msg.Status)
	requireRelationWhereCount(t, db, "privacy_history", "id = ? AND status = ?", 1, activeDraft.Msg.Id, managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String())
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"id = ? AND status = ?",
		1,
		rescheduledUpdateRun.ID,
		legaldomain.CampaignDeliveryRunStatusCancelled,
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"id = ? AND status = ?",
		1,
		supersededEffectiveRun.ID,
		legaldomain.CampaignDeliveryRunStatusScheduled,
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"id = ? AND status = ?",
		1,
		terminalEffectiveRun.ID,
		legaldomain.CampaignDeliveryRunStatusSent,
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"privacy_id = ? AND template_event_key = ? AND status = ?",
		1,
		scheduledDraft.Msg.Id,
		email.EventPrivacyEffective.String(),
		legaldomain.CampaignDeliveryRunStatusScheduled,
	)
}

func TestScheduledTermsActivationCancelsPendingUpdateDeliveryRunIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventTermsUpdate.String(),
	)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventTermsEffective.String(),
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	service := newTermsServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithTermsContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)
	draft, err := service.CreateTermsVersion(
		ctx,
		connect.NewRequest(&managev1.CreateTermsVersionRequest{
			Title:    ptrString("Scheduled terms activation"),
			Document: legalPolicyDocumentFixture("en", "Scheduled terms activation"),
		}),
	)
	require.NoError(t, err)
	_, err = service.ScheduleTerms(
		ctx,
		connect.NewRequest(&managev1.ScheduleTermsRequest{
			Id:               draft.Msg.Id,
			EffectiveFrom:    timestamppb.New(time.Now().Add(24 * time.Hour).UTC()),
			ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.NoError(t, err)
	var updateRun model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"terms_id = ? AND template_event_key = ? AND status = ?",
		draft.Msg.Id,
		email.EventTermsUpdate.String(),
		legaldomain.CampaignDeliveryRunStatusScheduled,
	).First(&updateRun).Error)

	activated, err := service.ActivateTermsNow(
		ctx,
		connect.NewRequest(&managev1.ActivateTermsNowRequest{
			Id: draft.Msg.Id, ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.NoError(t, err)
	require.Equal(
		t,
		managev1.TermsStatus_TERMS_STATUS_ACTIVE,
		activated.Msg.Status,
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"id = ? AND status = ?",
		1,
		updateRun.ID,
		legaldomain.CampaignDeliveryRunStatusCancelled,
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"terms_id = ? AND template_event_key = ? AND status = ?",
		1,
		draft.Msg.Id,
		email.EventTermsEffective.String(),
		legaldomain.CampaignDeliveryRunStatusScheduled,
	)
}

func TestImmediateLegalActivationRollsBackWithoutEffectiveNoticeTemplateIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	service := newPrivacyServiceForLegalIntegrationTest(
		db, "https://example.com", "", spiceDB,
		legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)
	draft, err := service.CreatePrivacyVersion(
		ctx,
		connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title:    ptrString("Activation without mail template"),
			Document: legalPolicyDocumentFixture("en", "Activation without mail template"),
		}),
	)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.EmailTemplate{}).
		Where("event_key = ?", email.EventPrivacyEffective.String()).
		Update("event_key", nil).Error)

	_, err = service.ActivatePrivacyNow(
		ctx,
		connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
			Id: draft.Msg.Id, ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), err)
	var status string
	require.NoError(t, db.Table("privacy_history").Select("status").Where("id = ?", draft.Msg.Id).Scan(&status).Error)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(), status)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"privacy_id = ?",
		0,
		draft.Msg.Id,
	)
}

func TestLegalScheduleAllowsOnlyOnePendingVersionIntegration(t *testing.T) {
	t.Run("privacy", func(t *testing.T) {
		db := newLegalIntegrationDB(t)
		seedLegalDeliveryTemplateIntegration(
			t,
			db,
			email.EventPrivacyUpdate.String(),
		)
		ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
		service := newPrivacyServiceForLegalIntegrationTest(
			db,
			"https://example.com",
			"https://cdn.example.com",
			spiceDB,
			legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
		)
		first, err := service.CreatePrivacyVersion(
			ctx,
			connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
				Title:    ptrString("First pending privacy"),
				Document: legalPolicyDocumentFixture("en", "First pending privacy"),
			}),
		)
		require.NoError(t, err)
		_, err = service.SchedulePrivacy(
			ctx,
			connect.NewRequest(&managev1.SchedulePrivacyRequest{
				Id:               first.Msg.Id,
				ExpectedRevision: first.Msg.Revision,
				EffectiveFrom: timestamppb.New(
					time.Now().Add(24 * time.Hour).UTC(),
				),
			}),
		)
		require.NoError(t, err)
		second, err := service.CreatePrivacyVersion(
			ctx,
			connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
				Title:    ptrString("Second pending privacy"),
				Document: legalPolicyDocumentFixture("en", "Second pending privacy"),
			}),
		)
		require.NoError(t, err)
		_, err = service.SchedulePrivacy(
			ctx,
			connect.NewRequest(&managev1.SchedulePrivacyRequest{
				Id:               second.Msg.Id,
				ExpectedRevision: second.Msg.Revision,
				EffectiveFrom: timestamppb.New(
					time.Now().Add(48 * time.Hour).UTC(),
				),
			}),
		)
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		requireRelationWhereCount(
			t,
			db,
			"privacy_history",
			"status = ?",
			1,
			managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
		)
		requireRelationWhereCount(
			t,
			db,
			"privacy_history",
			"id = ? AND status = ?",
			1,
			second.Msg.Id,
			managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT.String(),
		)
		_, err = service.CancelPrivacySchedule(
			ctx,
			connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{
				Id: first.Msg.Id,
			}),
		)
		require.NoError(t, err)
		_, err = service.SchedulePrivacy(
			ctx,
			connect.NewRequest(&managev1.SchedulePrivacyRequest{
				Id:               second.Msg.Id,
				ExpectedRevision: second.Msg.Revision,
				EffectiveFrom: timestamppb.New(
					time.Now().Add(48 * time.Hour).UTC(),
				),
			}),
		)
		require.NoError(t, err)
	})

	t.Run("terms", func(t *testing.T) {
		db := newLegalIntegrationDB(t)
		seedLegalDeliveryTemplateIntegration(
			t,
			db,
			email.EventTermsUpdate.String(),
		)
		ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
		service := newTermsServiceForLegalIntegrationTest(
			db,
			"https://example.com",
			"https://cdn.example.com",
			spiceDB,
			legaldomain.WithTermsContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
		)
		first, err := service.CreateTermsVersion(
			ctx,
			connect.NewRequest(&managev1.CreateTermsVersionRequest{
				Title:    ptrString("First pending terms"),
				Document: legalPolicyDocumentFixture("en", "First pending terms"),
			}),
		)
		require.NoError(t, err)
		_, err = service.ScheduleTerms(
			ctx,
			connect.NewRequest(&managev1.ScheduleTermsRequest{
				Id:               first.Msg.Id,
				ExpectedRevision: first.Msg.Revision,
				EffectiveFrom: timestamppb.New(
					time.Now().Add(24 * time.Hour).UTC(),
				),
			}),
		)
		require.NoError(t, err)
		second, err := service.CreateTermsVersion(
			ctx,
			connect.NewRequest(&managev1.CreateTermsVersionRequest{
				Title:    ptrString("Second pending terms"),
				Document: legalPolicyDocumentFixture("en", "Second pending terms"),
			}),
		)
		require.NoError(t, err)
		_, err = service.ScheduleTerms(
			ctx,
			connect.NewRequest(&managev1.ScheduleTermsRequest{
				Id:               second.Msg.Id,
				ExpectedRevision: second.Msg.Revision,
				EffectiveFrom: timestamppb.New(
					time.Now().Add(48 * time.Hour).UTC(),
				),
			}),
		)
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
		requireRelationWhereCount(
			t,
			db,
			"terms_history",
			"status = ?",
			1,
			managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
		)
		requireRelationWhereCount(
			t,
			db,
			"terms_history",
			"id = ? AND status = ?",
			1,
			second.Msg.Id,
			managev1.TermsStatus_TERMS_STATUS_DRAFT.String(),
		)
		_, err = service.CancelTermsSchedule(
			ctx,
			connect.NewRequest(&managev1.CancelTermsScheduleRequest{
				Id: first.Msg.Id,
			}),
		)
		require.NoError(t, err)
		_, err = service.ScheduleTerms(
			ctx,
			connect.NewRequest(&managev1.ScheduleTermsRequest{
				Id:               second.Msg.Id,
				ExpectedRevision: second.Msg.Revision,
				EffectiveFrom: timestamppb.New(
					time.Now().Add(48 * time.Hour).UTC(),
				),
			}),
		)
		require.NoError(t, err)
	})
}

func TestDraftLegalDocumentTitlesUseBlockRevisionIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	store := newLegalLifecycleContentBlockStore(t, spiceDB)
	contributor := integrationTestUUID()
	testutil.InsertAuthorizedDocumentContributor(t, db, spiceDB, contributor)

	termsService := newTermsServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithTermsContentBlockStore(store),
	)
	termsDraft, err := termsService.CreateTermsVersion(
		ctx,
		connect.NewRequest(&managev1.CreateTermsVersionRequest{
			Title:    ptrString("Initial terms title"),
			Document: legalPolicyDocumentFixture("en", "Initial terms title"),
		}),
	)
	require.NoError(t, err)
	internalTerms := legaldomain.NewInternalTermsService(db, legalIntegrationDependencies(db, "https://cdn.example.com"))
	legaldomain.WithInternalTermsContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(internalTerms)
	termsUpdated, err := internalTerms.UpdateTermsLocaleMetadata(
		ctx,
		connect.NewRequest(&intrav1.UpdateTermsLocaleMetadataRequest{
			TermsId: termsDraft.Msg.Id, Locale: "en", Title: ptrString("Updated terms title"),
			ExpectedRevision: termsDraft.Msg.Revision, ContributorMemberIds: []string{contributor},
		}),
	)
	require.NoError(t, err)
	require.NotEqual(t, termsDraft.Msg.Revision, termsUpdated.Msg.DocumentRevision)
	var storedTermsTitle string
	require.NoError(t, db.Table("terms_history").Select("title").Where("id = ?", termsDraft.Msg.Id).Scan(&storedTermsTitle).Error)
	require.Equal(t, "Updated terms title", storedTermsTitle)

	privacyService := newPrivacyServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithPrivacyContentBlockStore(store),
	)
	privacyDraft, err := privacyService.CreatePrivacyVersion(
		ctx,
		connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title:    ptrString("Initial privacy title"),
			Document: legalPolicyDocumentFixture("en", "Initial privacy title"),
		}),
	)
	require.NoError(t, err)
	internalPrivacy := legaldomain.NewInternalPrivacyService(db, legalIntegrationDependencies(db, "https://cdn.example.com"))
	legaldomain.WithInternalPrivacyContentBlocks(store, spiceDB, testcollaboration.NewCheckpoints(db, spiceDB))(internalPrivacy)
	privacyUpdated, err := internalPrivacy.UpdatePrivacyLocaleMetadata(
		ctx,
		connect.NewRequest(&intrav1.UpdatePrivacyLocaleMetadataRequest{
			PrivacyId: privacyDraft.Msg.Id, Locale: "en", Title: ptrString("Updated privacy title"),
			ExpectedRevision: privacyDraft.Msg.Revision, ContributorMemberIds: []string{contributor},
		}),
	)
	require.NoError(t, err)
	require.NotEqual(t, privacyDraft.Msg.Revision, privacyUpdated.Msg.DocumentRevision)
	var storedPrivacyTitle string
	require.NoError(t, db.Table("privacy_history").Select("title").Where("id = ?", privacyDraft.Msg.Id).Scan(&storedPrivacyTitle).Error)
	require.Equal(t, "Updated privacy title", storedPrivacyTitle)
}

func TestLegalDeliveryTemplateAndLayoutLifecycleGuardsIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	templateID, layoutID := seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventPrivacyUpdate.String(),
	)
	store := newLegalLifecycleContentBlockStore(t, spiceDB)
	privacyService := newPrivacyServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithPrivacyContentBlockStore(store),
	)
	draft, err := privacyService.CreatePrivacyVersion(
		ctx,
		connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title:    ptrString("Durable email source"),
			Document: legalPolicyDocumentFixture("en", "Durable email source"),
		}),
	)
	require.NoError(t, err)
	_, err = privacyService.SchedulePrivacy(
		ctx,
		connect.NewRequest(&managev1.SchedulePrivacyRequest{
			Id:               draft.Msg.Id,
			EffectiveFrom:    timestamppb.New(time.Now().Add(24 * time.Hour).UTC()),
			ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.NoError(t, err)

	var scheduledRun model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"privacy_id = ? AND status = ?",
		draft.Msg.Id,
		legaldomain.CampaignDeliveryRunStatusScheduled,
	).First(&scheduledRun).Error)
	require.Equal(t, legaldomain.EmailDeliveryRunKindLegalNotice, scheduledRun.RunKind)
	require.Equal(t, draft.Msg.Id, ptrStringValue(scheduledRun.PrivacyID))
	require.NotNil(t, scheduledRun.SourcePrivacyVersion)
	require.Nil(t, scheduledRun.TermsID)
	require.Equal(t, templateID, ptrStringValue(scheduledRun.SourceTemplateID))
	require.Equal(t, layoutID, ptrStringValue(scheduledRun.SourceLayoutID))
	require.True(t, scheduledRun.DefinitionSealed)
	err = emailauthoringadapter.NewCampaignDeliveryReferences().RequireLayoutMutable(
		ctx, db, layoutID,
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	var terminalRun *model.CampaignDeliveryRun
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var createErr error
		terminalRun, createErr = legaladapter.NewNoticeRuntime(nil).CreateRun(
			ctx,
			tx,
			legaldomain.EmailDeliveryReferenceTypePrivacy,
			draft.Msg.Id,
			email.EventPrivacyUpdate.String(),
			map[string]string{
				"policy_title":   "Privacy update",
				"effective_date": "2026-08-01",
				"preview_url":    "https://example.com/privacy/preview",
			},
			time.Now().Add(48*time.Hour).UTC(),
		)
		if createErr != nil {
			return createErr
		}
		now := time.Now().UTC()
		return tx.Model(&model.CampaignDeliveryRun{}).
			Where("id = ?", terminalRun.ID).
			Updates(structured.Fields{
				"status":       campaign.CampaignDeliveryRunStatusFailed,
				"completed_at": now,
			}).Error
	}))
	require.NotNil(t, terminalRun)

	templateService := emailauthoring.NewEmailTemplateService(
		db,
		nil,
		emailauthoringadapter.NewRuntime(),
		"https://cdn.example.com",
		"https://example.com",
		spiceDB,
		emailauthoring.WithEmailTemplateContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		emailauthoring.WithEmailTemplateCampaignDeliveryReferences(
			emailauthoringadapter.NewCampaignDeliveryReferences(),
		),
	)
	layoutService := emailauthoring.NewEmailLayoutService(
		db,
		"https://cdn.example.com",
		"https://example.com",
		spiceDB,
		emailauthoring.WithEmailLayoutContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		emailauthoring.WithEmailLayoutCampaignDeliveryReferences(
			emailauthoringadapter.NewCampaignDeliveryReferences(),
		),
	)
	template, err := templateService.GetEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.GetEmailTemplateRequest{Id: templateID}),
	)
	require.NoError(t, err)
	require.Equal(t, int32(2), template.Msg.DeliveryRunCount)
	layout, err := layoutService.GetEmailLayout(
		ctx,
		connect.NewRequest(&managev1.GetEmailLayoutRequest{Id: layoutID}),
	)
	require.NoError(t, err)
	require.Equal(t, int32(1), layout.Msg.TemplateCount)
	require.Equal(t, int32(2), layout.Msg.DeliveryRunCount)

	templateName := "Frozen template"
	_, err = templateService.UpdateEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.UpdateEmailTemplateRequest{
			Id:   templateID,
			Name: &templateName,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	layoutName := "Frozen layout"
	_, err = layoutService.UpdateEmailLayout(
		ctx,
		connect.NewRequest(&managev1.UpdateEmailLayoutRequest{
			Id:   layoutID,
			Name: &layoutName,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	_, err = templateService.UpdateEventMapping(
		ctx,
		connect.NewRequest(&managev1.UpdateEventMappingRequest{
			Event: email.EventPrivacyUpdate.String(),
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))

	cancelled, err := privacyService.CancelPrivacySchedule(
		ctx,
		connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{Id: draft.Msg.Id}),
	)
	require.NoError(t, err)
	require.Equal(
		t,
		managev1.PrivacyStatus_PRIVACY_STATUS_DRAFT,
		cancelled.Msg.Status,
	)
	var scheduledStatus string
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Select("status").
		Where("id = ?", scheduledRun.ID).
		Scan(&scheduledStatus).Error)
	require.Equal(t, legaldomain.CampaignDeliveryRunStatusCancelled, scheduledStatus)
	var terminalStatus string
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Select("status").
		Where("id = ?", terminalRun.ID).
		Scan(&terminalStatus).Error)
	require.Equal(t, campaign.CampaignDeliveryRunStatusFailed, terminalStatus)

	templateName = "Editable after cancellation"
	_, err = templateService.UpdateEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.UpdateEmailTemplateRequest{
			Id:   templateID,
			Name: &templateName,
		}),
	)
	require.NoError(t, err)
	layoutName = "Editable after cancellation"
	_, err = layoutService.UpdateEmailLayout(
		ctx,
		connect.NewRequest(&managev1.UpdateEmailLayoutRequest{
			Id:   layoutID,
			Name: &layoutName,
		}),
	)
	require.NoError(t, err)
	_, err = templateService.UpdateEventMapping(
		ctx,
		connect.NewRequest(&managev1.UpdateEventMappingRequest{
			Event: email.EventPrivacyUpdate.String(),
		}),
	)
	require.NoError(t, err)
	emptyLayoutID := ""
	_, err = templateService.UpdateEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.UpdateEmailTemplateRequest{
			Id:       templateID,
			LayoutId: &emptyLayoutID,
		}),
	)
	require.NoError(t, err)

	_, err = templateService.DeleteEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.DeleteEmailTemplateRequest{Id: templateID}),
	)
	require.NoError(t, err)
	_, err = layoutService.DeleteEmailLayout(
		ctx,
		connect.NewRequest(&managev1.DeleteEmailLayoutRequest{Id: layoutID}),
	)
	require.NoError(t, err)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"id IN ? AND source_template_id IS NULL AND source_layout_id IS NULL AND source_template_updated_at IS NOT NULL AND source_layout_updated_at IS NOT NULL AND render_snapshot IS NOT NULL",
		2,
		[]string{scheduledRun.ID, terminalRun.ID},
	)
	_, err = privacyService.DeletePrivacy(
		ctx,
		connect.NewRequest(&managev1.DeletePrivacyRequest{
			Id: draft.Msg.Id, ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	requireRelationWhereCount(
		t,
		db,
		"privacy_history",
		"id = ?",
		1,
		draft.Msg.Id,
	)
	requireRelationWhereCount(t, db, "privacy_history", "id = ? AND source_locale = 'en'", 1, draft.Msg.Id)
}

func TestPrivacyCancelRejectsSendingDeliveryRunIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventPrivacyUpdate.String(),
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	privacyService := newPrivacyServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)
	draft, err := privacyService.CreatePrivacyVersion(
		ctx,
		connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title:    ptrString("Sending privacy"),
			Document: legalPolicyDocumentFixture("en", "Sending privacy"),
		}),
	)
	require.NoError(t, err)
	_, err = privacyService.SchedulePrivacy(
		ctx,
		connect.NewRequest(&managev1.SchedulePrivacyRequest{
			Id:               draft.Msg.Id,
			EffectiveFrom:    timestamppb.New(time.Now().Add(24 * time.Hour).UTC()),
			ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.NoError(t, err)
	var run model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"privacy_id = ? AND status = ?",
		draft.Msg.Id,
		legaldomain.CampaignDeliveryRunStatusScheduled,
	).First(&run).Error)
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Where("id = ?", run.ID).
		Update("status", legaldomain.CampaignDeliveryRunStatusSending).Error)

	_, err = privacyService.CancelPrivacySchedule(
		ctx,
		connect.NewRequest(&managev1.CancelPrivacyScheduleRequest{Id: draft.Msg.Id}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	var privacy model.Privacy
	require.NoError(t, db.First(&privacy, "id = ?", draft.Msg.Id).Error)
	require.Equal(
		t,
		managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
		privacy.Status,
	)
	var reloadedRun model.CampaignDeliveryRun
	require.NoError(t, db.First(&reloadedRun, "id = ?", run.ID).Error)
	require.Equal(t, legaldomain.CampaignDeliveryRunStatusSending, reloadedRun.Status)
}

func TestPrivacyActivationDoesNotMutateSendingSupersededDeliveryRunIntegration(
	t *testing.T,
) {
	db := newLegalIntegrationDB(t)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventPrivacyEffective.String(),
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	service := newPrivacyServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)
	activeDraft, err := service.CreatePrivacyVersion(
		ctx,
		connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title:    ptrString("Currently active privacy"),
			Document: legalPolicyDocumentFixture("en", "Currently active privacy"),
		}),
	)
	require.NoError(t, err)
	_, err = service.ActivatePrivacyNow(
		ctx,
		connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
			Id: activeDraft.Msg.Id, ExpectedRevision: activeDraft.Msg.Revision,
		}),
	)
	require.NoError(t, err)
	var sendingRun model.CampaignDeliveryRun
	require.NoError(t, db.Where(
		"privacy_id = ? AND template_event_key = ? AND status = ?",
		activeDraft.Msg.Id,
		email.EventPrivacyEffective.String(),
		legaldomain.CampaignDeliveryRunStatusScheduled,
	).First(&sendingRun).Error)
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Where("id = ?", sendingRun.ID).
		Update("status", legaldomain.CampaignDeliveryRunStatusSending).Error)

	incomingDraft, err := service.CreatePrivacyVersion(
		ctx,
		connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
			Title:    ptrString("Incoming privacy"),
			Document: legalPolicyDocumentFixture("en", "Incoming privacy"),
		}),
	)
	require.NoError(t, err)
	_, err = service.ActivatePrivacyNow(
		ctx,
		connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
			Id: incomingDraft.Msg.Id, ExpectedRevision: incomingDraft.Msg.Revision,
		}),
	)
	require.NoError(t, err)
	requireRelationWhereCount(
		t,
		db,
		"privacy_history",
		"id = ? AND status = ?",
		1,
		activeDraft.Msg.Id,
		managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
	)
	requireRelationWhereCount(
		t,
		db,
		"privacy_history",
		"id = ? AND status = ?",
		1,
		incomingDraft.Msg.Id,
		managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"id = ? AND status = ?",
		1,
		sendingRun.ID,
		legaldomain.CampaignDeliveryRunStatusSending,
	)
	requireRelationWhereCount(
		t,
		db,
		"email_delivery_run",
		"privacy_id = ? AND template_event_key = ?",
		1,
		incomingDraft.Msg.Id,
		email.EventPrivacyEffective.String(),
	)
}

func TestPrivacyActivationDoesNotMutateStartedUpdateRunIntegration(t *testing.T) {
	for _, status := range []string{
		legaldomain.CampaignDeliveryRunStatusSending,
		legaldomain.CampaignDeliveryRunStatusSent,
	} {
		t.Run(status, func(t *testing.T) {
			db := newLegalIntegrationDB(t)
			seedLegalDeliveryTemplateIntegration(t, db, email.EventPrivacyUpdate.String())
			seedLegalDeliveryTemplateIntegration(t, db, email.EventPrivacyEffective.String())
			ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
			service := newPrivacyServiceForLegalIntegrationTest(
				db, "https://example.com", "", spiceDB,
				legaldomain.WithPrivacyContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
			)
			draft, err := service.CreatePrivacyVersion(
				ctx,
				connect.NewRequest(&managev1.CreatePrivacyVersionRequest{
					Title:    ptrString("Started update notice " + status),
					Document: legalPolicyDocumentFixture("en", "Started update notice "+status),
				}),
			)
			require.NoError(t, err)
			_, err = service.SchedulePrivacy(
				ctx,
				connect.NewRequest(&managev1.SchedulePrivacyRequest{
					Id:               draft.Msg.Id,
					EffectiveFrom:    timestamppb.New(time.Now().Add(24 * time.Hour).UTC()),
					ExpectedRevision: draft.Msg.Revision,
				}),
			)
			require.NoError(t, err)
			var updateRun model.CampaignDeliveryRun
			require.NoError(t, db.Where(
				"privacy_id = ? AND template_event_key = ? AND status = ?",
				draft.Msg.Id,
				email.EventPrivacyUpdate.String(),
				legaldomain.CampaignDeliveryRunStatusScheduled,
			).First(&updateRun).Error)
			updates := structured.Fields{"status": status}
			if status == legaldomain.CampaignDeliveryRunStatusSent {
				updates["completed_at"] = time.Now().UTC()
			}
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where("id = ?", updateRun.ID).
				Updates(updates).Error)

			activated, err := service.ActivatePrivacyNow(
				ctx,
				connect.NewRequest(&managev1.ActivatePrivacyNowRequest{
					Id: draft.Msg.Id, ExpectedRevision: draft.Msg.Revision,
				}),
			)
			require.NoError(t, err)
			require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE, activated.Msg.Status)
			requireRelationWhereCount(
				t,
				db,
				"email_delivery_run",
				"id = ? AND status = ?",
				1,
				updateRun.ID,
				status,
			)
		})
	}
}

func TestTermsDeliveryHistoryBlocksDraftDeleteIntegration(t *testing.T) {
	db := newLegalIntegrationDB(t)
	seedLegalDeliveryTemplateIntegration(
		t,
		db,
		email.EventTermsUpdate.String(),
	)
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	service := newTermsServiceForLegalIntegrationTest(
		db,
		"https://example.com",
		"https://cdn.example.com",
		spiceDB,
		legaldomain.WithTermsContentBlockStore(newLegalLifecycleContentBlockStore(t, spiceDB)),
	)
	draft, err := service.CreateTermsVersion(
		ctx,
		connect.NewRequest(&managev1.CreateTermsVersionRequest{
			Title:    ptrString("Durable terms history"),
			Document: legalPolicyDocumentFixture("en", "Durable terms history"),
		}),
	)
	require.NoError(t, err)
	_, err = service.ScheduleTerms(
		ctx,
		connect.NewRequest(&managev1.ScheduleTermsRequest{
			Id:               draft.Msg.Id,
			EffectiveFrom:    timestamppb.New(time.Now().Add(24 * time.Hour).UTC()),
			ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.NoError(t, err)
	_, err = service.CancelTermsSchedule(
		ctx,
		connect.NewRequest(&managev1.CancelTermsScheduleRequest{Id: draft.Msg.Id}),
	)
	require.NoError(t, err)
	_, err = service.DeleteTerms(
		ctx,
		connect.NewRequest(&managev1.DeleteTermsRequest{
			Id: draft.Msg.Id, ExpectedRevision: draft.Msg.Revision,
		}),
	)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	requireRelationWhereCount(
		t,
		db,
		"terms_history",
		"id = ?",
		1,
		draft.Msg.Id,
	)
	requireRelationWhereCount(t, db, "terms_history", "id = ? AND source_locale = 'en'", 1, draft.Msg.Id)
}

func seedLegalDeliveryTemplateIntegration(
	t *testing.T,
	db *gorm.DB,
	eventKey string,
) (string, string) {
	t.Helper()
	suffix := uuid.NewString()
	ctx, spiceDB := legalIntegrationAdminCtxWithIdentityAndSpiceDB(t, db)
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	layout, err := emailauthoring.NewEmailLayoutService(
		db,
		"https://cdn.example.com",
		"https://example.com",
		spiceDB,
		emailauthoring.WithEmailLayoutContentBlockStore(store),
		emailauthoring.WithEmailLayoutCampaignDeliveryReferences(
			emailauthoringadapter.NewCampaignDeliveryReferences(),
		),
	).CreateEmailLayout(
		ctx,
		connect.NewRequest(&managev1.CreateEmailLayoutRequest{
			Name:         "Legal delivery layout " + suffix,
			Key:          "legal_delivery_layout_" + strings.ReplaceAll(suffix, "-", "_"),
			SourceLocale: "en",
			HtmlContent:  "<html><body>{{content}}</body></html>",
		}),
	)
	require.NoError(t, err)
	template, err := emailauthoring.NewEmailTemplateService(
		db,
		nil,
		emailauthoringadapter.NewRuntime(),
		"https://cdn.example.com",
		"https://example.com",
		spiceDB,
		emailauthoring.WithEmailTemplateContentBlockStore(store),
		emailauthoring.WithEmailTemplateCampaignDeliveryReferences(
			emailauthoringadapter.NewCampaignDeliveryReferences(),
		),
	).CreateEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.CreateEmailTemplateRequest{
			Key:          "legal_delivery_template_" + strings.ReplaceAll(suffix, "-", "_"),
			Name:         "Legal delivery template " + suffix,
			Subject:      "Legal delivery notice",
			SourceLocale: "en",
		}),
	)
	require.NoError(t, err)
	templateSourceTitle := "Legal delivery notice"
	publishEmailTemplateSourceBlocksForIntegration(t, db, spiceDB, template.Msg.Id, templateSourceTitle)
	layoutID := layout.Msg.Id
	templateService := emailauthoring.NewEmailTemplateService(
		db,
		nil,
		emailauthoringadapter.NewRuntime(),
		"https://cdn.example.com",
		"https://example.com",
		spiceDB,
		emailauthoring.WithEmailTemplateContentBlockStore(store),
		emailauthoring.WithEmailTemplateCampaignDeliveryReferences(
			emailauthoringadapter.NewCampaignDeliveryReferences(),
		),
	)
	_, err = templateService.UpdateEmailTemplate(
		ctx,
		connect.NewRequest(&managev1.UpdateEmailTemplateRequest{
			Id:       template.Msg.Id,
			LayoutId: &layoutID,
		}),
	)
	require.NoError(t, err)
	templateID := template.Msg.Id
	_, err = templateService.UpdateEventMapping(
		ctx,
		connect.NewRequest(&managev1.UpdateEventMappingRequest{
			Event:      eventKey,
			TemplateId: &templateID,
		}),
	)
	require.NoError(t, err)
	return templateID, layoutID
}

func newLegalLifecycleContentBlockStore(
	t *testing.T,
	spiceDB *auth.SpiceDBClient,
) *contentblock.Store {
	t.Helper()
	store, err := contentblock.NewGeneratedStore(filemedia.NewContentBlockFileReuseAuthorizer(spiceDB))
	require.NoError(t, err)
	return store
}
