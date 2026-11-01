//go:build integration

package workerpostgres

import (
	"encoding/json"
	"testing"
	"time"

	artistadapter "github.com/echovisionlab/geul-api/internal/adapters/artist"
	formogadapter "github.com/echovisionlab/geul-api/internal/adapters/form/og"
	labeladapter "github.com/echovisionlab/geul-api/internal/adapters/label"
	pageadapter "github.com/echovisionlab/geul-api/internal/adapters/page"
	postadapter "github.com/echovisionlab/geul-api/internal/adapters/post"
	releaseadapter "github.com/echovisionlab/geul-api/internal/adapters/release"
	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	sitesettingsadapter "github.com/echovisionlab/geul-api/internal/adapters/sitesettings"
	workadapter "github.com/echovisionlab/geul-api/internal/adapters/work"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	legaladapter "github.com/echovisionlab/geul-api/internal/adapters/legal"
	"github.com/echovisionlab/geul-api/internal/campaign"
	"github.com/echovisionlab/geul-api/internal/email"
	legaldomain "github.com/echovisionlab/geul-api/internal/legal"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/og"
	"github.com/echovisionlab/geul-api/internal/scheduler"
	"github.com/echovisionlab/geul-api/internal/structured"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	"github.com/echovisionlab/geul-api/internal/worker"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestScheduledLegalActivationAtomicallySwitchesCanonicalOgTargetIntegration(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		table           string
		activeStatus    string
		scheduledStatus string
		archivedStatus  string
		referenceType   string
		updateEvent     string
		effectiveEvent  string
		activate        func(*worker.Handlers) error
	}{
		{
			name: "privacy", table: "privacy_history",
			activeStatus:    managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(),
			scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.PrivacyStatus_PRIVACY_STATUS_ARCHIVED.String(),
			referenceType:   legaldomain.EmailDeliveryReferenceTypePrivacy,
			updateEvent:     email.EventPrivacyUpdate.String(),
			effectiveEvent:  email.EventPrivacyEffective.String(),
			activate:        func(h *worker.Handlers) error { return h.HandleScheduled(t.Context(), scheduler.JobActivatePrivacy) },
		},
		{
			name: "terms", table: "terms_history",
			activeStatus:    managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(),
			scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			archivedStatus:  managev1.TermsStatus_TERMS_STATUS_ARCHIVED.String(),
			referenceType:   legaldomain.EmailDeliveryReferenceTypeTerms,
			updateEvent:     email.EventTermsUpdate.String(),
			effectiveEvent:  email.EventTermsEffective.String(),
			activate:        func(h *worker.Handlers) error { return h.HandleScheduled(t.Context(), scheduler.JobActivateTerms) },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newLegalActivationDB(t)
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
			// Saving a policy requests OG in its own transaction, which also
			// owns the PGMQ enqueue.
			requestSaved := func(documentID, reason string) *og.Plan {
				var plan *og.Plan
				require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
					var err error
					plan, err = legaladapter.RequestSaved(t.Context(), tx, planner, testCase.name, documentID, "", true, reason)
					return err
				}))
				return plan
			}
			require.NotNil(t, requestSaved(activeID, "seed_active_og"))

			// Saving/scheduling B while A is active must not seize the fixed route.
			require.Nil(t, requestSaved(scheduledID, "scheduled_too_early"))

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

			handlers := newLegalActivationHandlers(t, db, "https://www.example.com")
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
					"owner_type = ? AND owner_id = ? AND binding_key = 'og_en'",
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
	t.Parallel()
	for _, testCase := range []struct {
		name             string
		table            string
		backgroundColumn string
		activeStatus     string
		scheduledStatus  string
		activate         func(*worker.Handlers) error
	}{
		{
			name: "privacy", table: "privacy_history", backgroundColumn: "privacy_og_background_file_id",
			activeStatus: managev1.PrivacyStatus_PRIVACY_STATUS_ACTIVE.String(), scheduledStatus: managev1.PrivacyStatus_PRIVACY_STATUS_SCHEDULED.String(),
			activate: func(h *worker.Handlers) error { return h.HandleScheduled(t.Context(), scheduler.JobActivatePrivacy) },
		},
		{
			name: "terms", table: "terms_history", backgroundColumn: "terms_og_background_file_id",
			activeStatus: managev1.TermsStatus_TERMS_STATUS_ACTIVE.String(), scheduledStatus: managev1.TermsStatus_TERMS_STATUS_SCHEDULED.String(),
			activate: func(h *worker.Handlers) error { return h.HandleScheduled(t.Context(), scheduler.JobActivateTerms) },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newLegalActivationDB(t)
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

			handlers := newLegalActivationHandlers(t, db, "")
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
	t.Parallel()
	for _, testCase := range []struct {
		name            string
		table           string
		activeStatus    string
		scheduledStatus string
		archivedStatus  string
		referenceType   string
		updateEvent     string
		effectiveEvent  string
		activate        func(*worker.Handlers) error
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
			activate:        func(h *worker.Handlers) error { return h.HandleScheduled(t.Context(), scheduler.JobActivatePrivacy) },
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
			activate:        func(h *worker.Handlers) error { return h.HandleScheduled(t.Context(), scheduler.JobActivateTerms) },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db := newLegalActivationDB(t)
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

			handlers := newLegalActivationHandlers(t, db, "https://www.example.com")
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

func TestScheduledLegalActivationRollsBackWithoutEffectiveNoticeTemplateIntegration(t *testing.T) {
	t.Parallel()
	db := newLegalActivationDB(t)
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

	handlers := newLegalActivationHandlers(t, db, "https://www.example.com")
	require.Error(t, handlers.HandleScheduled(t.Context(), scheduler.JobActivatePrivacy))

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
		run, err = legaladapter.NewNoticeRuntime().CreateRun(
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

// newLegalActivationDB returns a database owned by the calling test; the
// activation commits real top-level transactions.
func newLegalActivationDB(t *testing.T) *gorm.DB {
	t.Helper()
	return testutil.SetupAppPostgres(t, testutil.AppPostgresOptions{
		BootstrapKratosStub: true,
		ApplyAppSchemaSQL:   true,
	}).DB
}

// idlePorts stands in for the worker ports a legal activation never
// reaches; a call panics on the nil port.
type idlePorts struct {
	worker.FileMedia
	worker.DanglingFileCleanup
	worker.PublicAssetCleanup
	worker.OGCleanup
	worker.FaviconCleanup
	worker.UploadSessions
	worker.UploadStorage
	worker.FileIngestPublisher
	worker.ShareLinks
	worker.AuthCodeIssuances
	worker.QueueArchives
	worker.MemberDeletions
	worker.IdentityLister
	worker.TimedBans
	worker.GeoIPRefresher
	worker.CampaignDeliveries
	worker.EmailDelivery
	worker.BulkEmail
}

// newLegalActivationHandlers returns the worker with the production legal
// activation adapters over db: the job unit of work, its audit and OG
// planner, and the Legal port without a notice dispatcher.
func newLegalActivationHandlers(t *testing.T, db *gorm.DB, siteOrigin string) *worker.Handlers {
	t.Helper()
	units, err := NewUnits(db, Ports{
		Audit:     apitelemetry.NewDurableWriter(db),
		OGPlanner: newWorkerOGPlanner(db, "https://cdn.example.com"),
	})
	require.NoError(t, err)
	idle := idlePorts{}
	handlers, err := worker.NewHandlers(worker.Deps{
		Units: units, Legal: legalPolicies{db: db}, SiteOrigin: siteOrigin,
		FileMedia: idle, DanglingFiles: idle, PublicAssets: idle, OGCleanup: idle, FaviconCleanup: idle,
		Uploads: idle, UploadStorage: idle, FileIngest: idle, ShareLinks: idle, AuthCodes: idle,
		QueueArchives: idle, MemberDeletions: idle, Identities: idle, TimedBans: idle, GeoIP: idle,
		Campaigns: idle, EmailDelivery: idle, BulkEmail: idle,
	})
	require.NoError(t, err)
	return handlers
}

func newWorkerOGPlanner(db *gorm.DB, cdnDomain string) *og.Planner {
	return og.NewPlanner(db, cdnDomain, sitesettingsadapter.NewRenderConfig(), workerOGProjections()...)
}

func newWorkerOGLifecycle(db *gorm.DB, cdnDomain string) *og.Lifecycle {
	return og.NewLifecycle(db, cdnDomain, workerOGProjections()...)
}

func workerOGProjections() []og.Projection {
	return []og.Projection{
		postadapter.NewProjection(), pageadapter.NewProjection(), seriesadapter.NewProjection(),
		workadapter.NewProjection(), labeladapter.NewProjection(), artistadapter.NewProjection(),
		releaseadapter.NewProjection(), formogadapter.NewProjection(), legaladapter.NewProjection(),
		sitesettingsadapter.NewProjection(),
	}
}
