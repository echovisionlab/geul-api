//go:build integration

package worker

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/config"
	"github.com/echovisionlab/geul-api/internal/email"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestScheduledLegalActivationAtomicallySwitchesCanonicalOgTargetIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		table           string
		activeStatus    string
		scheduledStatus string
		archivedStatus  string
		referenceType   string
		updateEvent     string
		effectiveEvent  string
		activate        func(*Handlers) error
	}{
		{
			name: "privacy", table: "privacy_history",
			activeStatus:    managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
			referenceType:   legaldomain.EmailDeliveryReferenceTypePrivacy,
			updateEvent:     email.EventPrivacyUpdate.String(),
			effectiveEvent:  email.EventPrivacyEffective.String(),
			activate: func(h *Handlers) error {
				return h.handleActivatePrivacy(t.Context())
			},
		},
		{
			name: "terms", table: "terms_history",
			activeStatus:    managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
			referenceType:   legaldomain.EmailDeliveryReferenceTypeTerms,
			updateEvent:     email.EventTermsUpdate.String(),
			effectiveEvent:  email.EventTermsEffective.String(),
			activate: func(h *Handlers) error {
				return h.handleActivateTerms(t.Context())
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newCommittedWorkerIntegrationDB(t)
			seedWorkerLegalDeliveryTemplate(t, db, testCase.updateEvent)
			seedWorkerLegalDeliveryTemplate(t, db, testCase.effectiveEvent)
			now := time.Now().UTC()
			activeID := uuid.NewString()
			scheduledID := uuid.NewString()
			activeDocumentID := seedWorkerPolicyContentDocument(t, db)
			scheduledDocumentID := seedWorkerPolicyContentDocument(t, db)
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.table+" (id, version, title, content, status, effective_from, created_at, updated_at, content_document_id) VALUES (?, 1, ?, '', ?, ?, ?, ?, ?), (?, 2, ?, '', ?, ?, ?, ?, ?)",
				activeID, "Active A", testCase.activeStatus, now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(-24*time.Hour), activeDocumentID,
				scheduledID, "Scheduled B", testCase.scheduledStatus, now.Add(-time.Minute), now.Add(-time.Hour), now.Add(-time.Hour), scheduledDocumentID,
			).Error)
			seedWorkerPolicyTranslationSource(t, db, testCase.name, activeID)
			seedWorkerPolicyTranslationSource(t, db, testCase.name, scheduledID)

			terminalRun := createCommittedWorkerLegalDeliveryRun(
				t, db, testCase.referenceType, activeID, testCase.effectiveEvent,
				legalDeliveryVariables(testCase.name, true), now.Add(-2*time.Hour),
			)
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where("id = ?", terminalRun.ID).
				Updates(structured.Fields{
					"status":       campaign.CampaignDeliveryRunStatusSent,
					"completed_at": now.Add(-time.Hour),
				}).Error)
			supersededEffectiveRun := createCommittedWorkerLegalDeliveryRun(
				t, db, testCase.referenceType, activeID, testCase.effectiveEvent,
				legalDeliveryVariables(testCase.name, true), now,
			)
			incomingUpdateRun := createCommittedWorkerLegalDeliveryRun(
				t, db, testCase.referenceType, scheduledID, testCase.updateEvent,
				legalDeliveryVariables(testCase.name, false), now,
			)

			planner := newWorkerOGPlanner(db, "https://cdn.example.com")
			activePlan, err := legaladapter.RequestSaved(
				t.Context(), db, planner, testCase.name, activeID, "", true, "seed_active_og",
			)
			require.NoError(t, err)
			require.NotNil(t, activePlan)

			// Saving/scheduling B while A is active must not seize the fixed route.
			earlyPlan, err := legaladapter.RequestSaved(
				t.Context(), db, planner, testCase.name, scheduledID, "", true, "scheduled_too_early",
			)
			require.NoError(t, err)
			require.Nil(t, earlyPlan)

			routeID := legaladapter.RouteID(testCase.name)
			var englishTarget model.OgGenerationTarget
			require.NoError(t, db.First(
				&englishTarget,
				"entity_type = ? AND entity_id = ? AND locale = 'en'",
				testCase.name,
				routeID,
			).Error)
			require.NotNil(t, englishTarget.LatestGenerationID)
			activeEnglishGenerationID := *englishTarget.LatestGenerationID
			lifecycle := newWorkerOGLifecycle(db, "https://cdn.example.com")
			claim, err := lifecycle.Claim(t.Context(), activeEnglishGenerationID)
			require.NoError(t, err)
			require.Equal(t, og.Claimed, claim.Result)
			digest := make([]byte, 32)
			digest[0] = 0x42
			status, _, err := lifecycle.Complete(t.Context(), activeEnglishGenerationID, claim.LeaseToken, &commonv1.AssetWriteResult{
				AssetId: activeEnglishGenerationID, FileSize: 1024, Sha256: digest,
			})
			require.NoError(t, err)
			require.Equal(t, model.OgGenerationStatusReady, status)

			handlers := &Handlers{
				db:          db,
				auditWriter: apitelemetry.NewDurableWriter(db),
				ogPlanner:   newWorkerOGPlanner(db, "https://cdn.example.com"),
				config: &config.Config{
					CDNURL: "https://cdn.example.com", SiteOrigin: "https://www.example.com",
				},
			}
			require.NoError(t, testCase.activate(handlers))

			var activeStatus, scheduledStatus string
			require.NoError(t, db.Table(testCase.table).Select("status").Where("id = ?", activeID).Scan(&activeStatus).Error)
			require.NoError(t, db.Table(testCase.table).Select("status").Where("id = ?", scheduledID).Scan(&scheduledStatus).Error)
			require.Equal(t, testCase.archivedStatus, activeStatus)
			require.Equal(t, testCase.activeStatus, scheduledStatus)
			requireDeliveryRunStatus(
				t,
				db,
				terminalRun.ID,
				campaign.CampaignDeliveryRunStatusSent,
			)
			requireDeliveryRunStatus(
				t,
				db,
				supersededEffectiveRun.ID,
				campaign.CampaignDeliveryRunStatusScheduled,
			)
			requireDeliveryRunStatus(
				t,
				db,
				incomingUpdateRun.ID,
				campaign.CampaignDeliveryRunStatusCancelled,
			)
			var incomingEffectiveRuns int64
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where(
					legalDeliveryReferenceWhere(testCase.name),
					scheduledID,
					testCase.effectiveEvent,
					campaign.CampaignDeliveryRunStatusScheduled,
				).
				Count(&incomingEffectiveRuns).Error)
			require.Equal(t, int64(1), incomingEffectiveRuns)

			var currentTarget model.OgGenerationTarget
			require.NoError(t, db.First(&currentTarget, "id = ?", englishTarget.ID).Error)
			require.NotNil(t, currentTarget.LatestGenerationID)
			require.NotEqual(t, activeEnglishGenerationID, *currentTarget.LatestGenerationID)
			var replacement model.OgGeneration
			require.NoError(t, db.First(&replacement, "id = ?", *currentTarget.LatestGenerationID).Error)
			var snapshot struct {
				Title string `json:"title"`
			}
			require.NoError(t, json.Unmarshal(replacement.EntitySnapshot, &snapshot))
			require.Equal(t, "Scheduled B", snapshot.Title)

			// The previous title-bearing raster is released as soon as its
			// replacement is queued. Metadata falls back to Site OG until the
			// new immutable generation completes.
			var bindingCount int64
			require.NoError(t, db.Model(&model.PublicAssetBinding{}).
				Where(
					"owner_type = ? AND owner_id = ? AND binding_key = 'og:en'",
					testCase.name,
					routeID,
				).
				Count(&bindingCount).Error)
			require.Zero(t, bindingCount)
			var oldAsset model.PublicAsset
			require.NoError(t, db.First(&oldAsset, "id = ?", activeEnglishGenerationID).Error)
			require.Equal(t, model.PublicAssetStatusDeletePending, oldAsset.Status)

			var activeHistory model.OgGeneration
			require.NoError(t, db.First(&activeHistory, "id = ?", activeEnglishGenerationID).Error)
			require.Equal(t, model.OgGenerationStatusReady, activeHistory.Status)

			require.NoError(t, testCase.activate(handlers))
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where(
					legalDeliveryReferenceWhere(testCase.name),
					scheduledID,
					testCase.effectiveEvent,
					campaign.CampaignDeliveryRunStatusScheduled,
				).
				Count(&incomingEffectiveRuns).Error)
			require.Equal(t, int64(1), incomingEffectiveRuns)
		})
	}
}

func TestScheduledLegalActivationRollsBackStatusWhenOgPlanningFailsIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name             string
		table            string
		backgroundColumn string
		activeStatus     string
		scheduledStatus  string
		activate         func(*Handlers) error
	}{
		{
			name: "privacy", table: "privacy_history", backgroundColumn: "privacy_og_background_file_id",
			activeStatus: managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(), scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			activate: func(h *Handlers) error { return h.handleActivatePrivacy(t.Context()) },
		},
		{
			name: "terms", table: "terms_history", backgroundColumn: "terms_og_background_file_id",
			activeStatus: managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(), scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			activate: func(h *Handlers) error { return h.handleActivateTerms(t.Context()) },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newCommittedWorkerIntegrationDB(t)
			now := time.Now().UTC()
			activeID := uuid.NewString()
			scheduledID := uuid.NewString()
			activeDocumentID := seedWorkerPolicyContentDocument(t, db)
			scheduledDocumentID := seedWorkerPolicyContentDocument(t, db)
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.table+" (id, version, title, content, status, effective_from, created_at, updated_at, content_document_id) VALUES (?, 1, 'Active A', '', ?, ?, ?, ?, ?), (?, 2, 'Scheduled B', '', ?, ?, ?, ?, ?)",
				activeID, testCase.activeStatus, now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(-24*time.Hour), activeDocumentID,
				scheduledID, testCase.scheduledStatus, now.Add(-time.Minute), now.Add(-time.Hour), now.Add(-time.Hour), scheduledDocumentID,
			).Error)
			seedWorkerPolicyTranslationSource(t, db, testCase.name, activeID)
			seedWorkerPolicyTranslationSource(t, db, testCase.name, scheduledID)
			missingReadyAssetFileID := uuid.NewString()
			require.NoError(t, db.Exec(
				"INSERT INTO file (id, file_name, mime_type, file_size, extension, sha256) VALUES (?, 'missing', 'image/webp', 1, 'webp', ?)",
				missingReadyAssetFileID,
				make([]byte, 32),
			).Error)
			require.NoError(t, db.Exec(
				"UPDATE site_settings SET "+testCase.backgroundColumn+" = ? WHERE id = 1",
				missingReadyAssetFileID,
			).Error)

			handlers := &Handlers{
				db:          db,
				config:      &config.Config{CDNURL: "https://cdn.example.com"},
				auditWriter: apitelemetry.NewDurableWriter(db),
				ogPlanner:   newWorkerOGPlanner(db, "https://cdn.example.com"),
			}
			require.Error(t, testCase.activate(handlers))

			var activeStatus, scheduledStatus string
			require.NoError(t, db.Table(testCase.table).Select("status").Where("id = ?", activeID).Scan(&activeStatus).Error)
			require.NoError(t, db.Table(testCase.table).Select("status").Where("id = ?", scheduledID).Scan(&scheduledStatus).Error)
			require.Equal(t, testCase.activeStatus, activeStatus)
			require.Equal(t, testCase.scheduledStatus, scheduledStatus)
			var runCount int64
			require.NoError(t, db.Model(&model.OgGenerationRun{}).Count(&runCount).Error)
			require.Zero(t, runCount)
		})
	}
}

func TestScheduledLegalActivationDoesNotMutateSendingSupersededRunIntegration(t *testing.T) {
	for _, testCase := range []struct {
		name            string
		table           string
		activeStatus    string
		scheduledStatus string
		archivedStatus  string
		referenceType   string
		updateEvent     string
		effectiveEvent  string
		activate        func(*Handlers) error
	}{
		{
			name:            "privacy",
			table:           "privacy_history",
			activeStatus:    managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
			referenceType:   legaldomain.EmailDeliveryReferenceTypePrivacy,
			updateEvent:     email.EventPrivacyUpdate.String(),
			effectiveEvent:  email.EventPrivacyEffective.String(),
			activate: func(h *Handlers) error {
				return h.handleActivatePrivacy(t.Context())
			},
		},
		{
			name:            "terms",
			table:           "terms_history",
			activeStatus:    managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
			referenceType:   legaldomain.EmailDeliveryReferenceTypeTerms,
			updateEvent:     email.EventTermsUpdate.String(),
			effectiveEvent:  email.EventTermsEffective.String(),
			activate: func(h *Handlers) error {
				return h.handleActivateTerms(t.Context())
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newCommittedWorkerIntegrationDB(t)
			seedWorkerLegalDeliveryTemplate(t, db, testCase.updateEvent)
			seedWorkerLegalDeliveryTemplate(t, db, testCase.effectiveEvent)
			now := time.Now().UTC()
			activeID := uuid.NewString()
			scheduledID := uuid.NewString()
			activeDocumentID := seedWorkerPolicyContentDocument(t, db)
			scheduledDocumentID := seedWorkerPolicyContentDocument(t, db)
			require.NoError(t, db.Exec(
				"INSERT INTO "+testCase.table+" (id, version, title, content, status, effective_from, created_at, updated_at, content_document_id) VALUES (?, 1, 'Active A', '', ?, ?, ?, ?, ?), (?, 2, 'Scheduled B', '', ?, ?, ?, ?, ?)",
				activeID, testCase.activeStatus, now.Add(-24*time.Hour), now.Add(-24*time.Hour), now.Add(-24*time.Hour), activeDocumentID,
				scheduledID, testCase.scheduledStatus, now.Add(-time.Minute), now.Add(-time.Hour), now.Add(-time.Hour), scheduledDocumentID,
			).Error)
			seedWorkerPolicyTranslationSource(t, db, testCase.name, activeID)
			seedWorkerPolicyTranslationSource(t, db, testCase.name, scheduledID)
			sendingRun := createCommittedWorkerLegalDeliveryRun(
				t, db, testCase.referenceType, activeID, testCase.effectiveEvent,
				legalDeliveryVariables(testCase.name, true), now,
			)
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where("id = ?", sendingRun.ID).
				Update("status", campaign.CampaignDeliveryRunStatusSending).Error)
			incomingUpdateRun := createCommittedWorkerLegalDeliveryRun(
				t, db, testCase.referenceType, scheduledID, testCase.updateEvent,
				legalDeliveryVariables(testCase.name, false), now,
			)
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where("id = ?", incomingUpdateRun.ID).
				Update("status", campaign.CampaignDeliveryRunStatusSending).Error)

			handlers := &Handlers{
				db:          db,
				auditWriter: apitelemetry.NewDurableWriter(db),
				ogPlanner:   newWorkerOGPlanner(db, "https://cdn.example.com"),
				config: &config.Config{
					CDNURL:     "https://cdn.example.com",
					SiteOrigin: "https://www.example.com",
				},
			}
			require.NoError(t, testCase.activate(handlers))

			var activeStatus, scheduledStatus string
			require.NoError(t, db.Table(testCase.table).
				Select("status").
				Where("id = ?", activeID).
				Scan(&activeStatus).Error)
			require.NoError(t, db.Table(testCase.table).
				Select("status").
				Where("id = ?", scheduledID).
				Scan(&scheduledStatus).Error)
			require.Equal(t, testCase.archivedStatus, activeStatus)
			require.Equal(t, testCase.activeStatus, scheduledStatus)
			requireDeliveryRunStatus(
				t,
				db,
				sendingRun.ID,
				campaign.CampaignDeliveryRunStatusSending,
			)
			requireDeliveryRunStatus(
				t,
				db,
				incomingUpdateRun.ID,
				campaign.CampaignDeliveryRunStatusSending,
			)
			var incomingEffectiveRuns int64
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where(
					legalDeliveryReferenceWhere(testCase.name),
					scheduledID,
					testCase.effectiveEvent,
					campaign.CampaignDeliveryRunStatusScheduled,
				).
				Count(&incomingEffectiveRuns).Error)
			require.Equal(t, int64(1), incomingEffectiveRuns)
		})
	}
}

func TestScheduledLegalActivationIsNoopWithoutDueDocumentIntegration(t *testing.T) {
	db := newWorkerIntegrationDB(t)
	handlers := &Handlers{
		db:          db,
		config:      &config.Config{CDNURL: "https://cdn.example.com"},
		auditWriter: apitelemetry.NewDurableWriter(db),
		ogPlanner:   newWorkerOGPlanner(db, "https://cdn.example.com"),
	}
	require.NoError(t, handlers.handleActivatePrivacy(t.Context()))
	require.NoError(t, handlers.handleActivateTerms(t.Context()))
	var runCount int64
	require.NoError(t, db.Model(&model.OgGenerationRun{}).Count(&runCount).Error)
	require.Zero(t, runCount)
}

func TestScheduledLegalActivationRollsBackWithoutEffectiveNoticeTemplateIntegration(t *testing.T) {
	db := newCommittedWorkerIntegrationDB(t)
	now := time.Now().UTC()
	privacyID := uuid.NewString()
	contentDocumentID := seedWorkerPolicyContentDocument(t, db)
	require.NoError(t, db.Exec(
		"UPDATE email_template SET event_key = NULL WHERE event_key = ?",
		email.EventPrivacyEffective.String(),
	).Error)
	require.NoError(t, db.Exec(
		`INSERT INTO privacy_history (
			id, version, title, content, status, effective_from, created_at, updated_at, content_document_id
		) VALUES (?, 1, 'Due without mail template', '', ?, ?, ?, ?, ?)`,
		privacyID,
		managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
		now.Add(-time.Minute),
		now.Add(-time.Hour),
		now.Add(-time.Hour),
		contentDocumentID,
	).Error)
	seedWorkerPolicyTranslationSource(t, db, "privacy", privacyID)

	handlers := &Handlers{
		db:          db,
		auditWriter: apitelemetry.NewDurableWriter(db),
		ogPlanner:   newWorkerOGPlanner(db, "https://cdn.example.com"),
		config: &config.Config{
			SiteOrigin: "https://www.example.com",
		},
	}
	require.Error(t, handlers.handleActivatePrivacy(t.Context()))

	var status string
	require.NoError(t, db.Table("privacy_history").
		Select("status").
		Where("id = ?", privacyID).
		Scan(&status).Error)
	require.Equal(t, managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(), status)
	var runCount int64
	require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
		Where("privacy_id = ?", privacyID).
		Count(&runCount).Error)
	require.Zero(t, runCount)
}

func legalDeliveryVariables(
	entityType string,
	effective bool,
) map[string]string {
	if effective {
		return map[string]string{
			entityType + "_url": "https://www.example.com/" + entityType,
		}
	}
	return map[string]string{
		"policy_title":   "Scheduled " + entityType,
		"effective_date": "2026-08-01",
		"preview_url": "https://www.example.com/" + entityType +
			"/preview/test",
	}
}

func createCommittedWorkerLegalDeliveryRun(
	t *testing.T,
	db *gorm.DB,
	referenceType string,
	referenceID string,
	templateEvent string,
	templateData map[string]string,
	scheduledAt time.Time,
) *model.CampaignDeliveryRun {
	t.Helper()
	var run *model.CampaignDeliveryRun
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		run, err = legaladapter.NewNoticeRuntime(nil).CreateRun(
			t.Context(), tx, referenceType, referenceID, templateEvent, templateData, scheduledAt,
		)
		return err
	}))
	require.NotNil(t, run)
	return run
}

func legalDeliveryReferenceWhere(entityType string) string {
	return entityType + "_id = ? AND template_event_key = ? AND status = ?"
}

func seedWorkerPolicyContentDocument(t *testing.T, db *gorm.DB) string {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile, revision) VALUES (?, 'policy', ?)`,
		documentID,
		uuid.NewString(),
	).Error)
	return documentID
}

func seedWorkerPolicyTranslationSource(
	t *testing.T,
	db *gorm.DB,
	entityType string,
	entityID string,
) {
	t.Helper()
	if entityType != "privacy" && entityType != "terms" {
		t.Fatalf("unsupported legal policy type %q", entityType)
	}
	var title string
	require.NoError(t, db.Table(entityType+"_history").
		Select("title").Where("id = ?", entityID).Take(&title).Error)
	require.NoError(t, db.Table(entityType+"_translation").Create(map[string]any{
		"entity_id": entityID, "locale": "en", "title": title,
	}).Error)
}

func requireDeliveryRunStatus(
	t *testing.T,
	db *gorm.DB,
	runID string,
	status string,
) {
	t.Helper()
	var run model.CampaignDeliveryRun
	require.NoError(t, db.First(&run, "id = ?", runID).Error)
	require.Equal(t, status, run.Status)
}

func seedWorkerLegalDeliveryTemplate(
	t *testing.T,
	db *gorm.DB,
	eventKey string,
) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
		SELECT COUNT(*)
		FROM email_template AS template
		JOIN email_template_translation AS translation
		  ON translation.entity_id = template.id
		 AND translation.locale = template.source_locale
		WHERE template.event_key = ?
		  AND template.is_active = TRUE
	`, eventKey).Scan(&count).Error)
	require.EqualValues(t, 1, count, "current schema must seed one legal delivery template")
}
