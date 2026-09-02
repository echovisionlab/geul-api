//go:build integration

package campaign

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"

	"github.com/echovisionlab/geul-api/internal/auth"
	emailutil "github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mq"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

func ensureBulkEmailAudienceKratosIdentityColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	testutil.EnsureKratosIdentityFixtureColumns(t, db)
}

func seedBulkEmailAudienceIdentity(
	t *testing.T,
	db *gorm.DB,
	email string,
	createdAt time.Time,
) string {
	t.Helper()
	identityID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{
		ID:        identityID,
		Email:     email,
		Name:      "Campaign recipient",
		CreatedAt: createdAt,
	})
	seedCampaignActiveMemberEmailPair(t, db, identityID, email)
	require.NoError(t, db.Create(&model.NewsletterSubscription{
		IdentityID:   identityID,
		SubscribedAt: createdAt,
	}).Error)
	return identityID
}

func publishCampaignSourceForDeliveryIntegration(
	t *testing.T,
	db *gorm.DB,
	campaignID string,
	subject string,
	contentHTML string,
) {
	t.Helper()
	_ = subject
	publishCampaignSourceBlocksForIntegration(
		t, db, testutil.IntegrationSpiceDB(t), campaignID, emailutil.StripHTML(contentHTML),
	)
}

func cleanupConcurrentCampaignDeliveryFixture(
	t *testing.T,
	db *gorm.DB,
	runID string,
	campaignID string,
	identityID string,
	segmentID string,
) {
	t.Helper()
	runID = strings.TrimSpace(runID)
	campaignID = strings.TrimSpace(campaignID)
	identityID = strings.TrimSpace(identityID)
	segmentID = strings.TrimSpace(segmentID)

	if runID != "" {
		require.NoError(t, db.Exec(
			`DELETE FROM email_delivery_run WHERE id = ?`,
			runID,
		).Error)
	}
	if campaignID != "" {
		// Also delete by owner so a failure between run creation and assigning
		// runID cannot leak durable history into a later shared-DB test.
		require.NoError(t, db.Exec(
			`DELETE FROM email_delivery_run WHERE campaign_id = ?`,
			campaignID,
		).Error)
		require.NoError(t, db.Exec(
			`DELETE FROM campaign WHERE id = ?`,
			campaignID,
		).Error)
	}
	if segmentID != "" {
		require.NoError(t, db.Exec(
			`UPDATE audience_segment
			    SET archived_at = now(), updated_at = now()
			  WHERE id = ? AND archived_at IS NULL`,
			segmentID,
		).Error)
	}
	if identityID != "" {
		require.NoError(t, db.Exec(
			`DELETE FROM kratos.identities WHERE id = ?`,
			identityID,
		).Error)
	}
}

func logPostgresErrorDetail(t *testing.T, err error) {
	t.Helper()
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) {
		return
	}
	t.Logf(
		"postgres error code=%s detail=%q hint=%q where=%q table=%q routine=%q",
		postgresError.Code,
		postgresError.Detail,
		postgresError.Hint,
		postgresError.Where,
		postgresError.TableName,
		postgresError.Routine,
	)
}

func newCampaignDeliveryIntegrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := testutil.NewIntegrationDB(t)
	ensureBulkEmailAudienceKratosIdentityColumns(t, db)
	return db
}

func TestAudienceArchiveAndCancelCampaignShareCampaignFirstLockOrderIntegration(
	t *testing.T,
) {
	db := newCampaignConcurrentIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	adminContext := auth.WithUser(context.Background(), admin.AuthUserInfo())
	var runID, campaignID, segmentID string
	t.Cleanup(func() {
		cleanupConcurrentCampaignDeliveryFixture(
			t,
			db,
			runID,
			campaignID,
			"",
			segmentID,
		)
	})
	now := time.Now().UTC()
	content := "<p>Audience archive versus cancel</p>"
	segment, err := newCampaignAudienceService(db, stack.SpiceDBClient).CreateSegment(
		adminContext,
		connect.NewRequest(&managev1.CreateSegmentRequest{
			Name:        "Archive cancel race " + uuid.NewString(),
			SegmentType: managev1.SegmentType_SEGMENT_TYPE_MEMBERS_BY_FILTER,
			Config:      &managev1.SegmentConfig{},
		}),
	)
	require.NoError(t, err)
	segmentID = segment.Msg.Id
	campaign := model.Campaign{
		Name:           "Audience archive versus cancel",
		Subject:        "Audience archive versus cancel",
		ContentHTML:    &content,
		Status:         managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(),
		TargetMode:     model.CampaignTargetModeSegment,
		RecipientScope: campaignRecipientScopeSubscribedUsers,
		SegmentID:      &segmentID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	createdCampaign, err := NewCampaignService(
		db, newCampaignRuntimeFixture(nil, nil), "", "", stack.SpiceDBClient,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)),
	).CreateCampaign(adminContext, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name: campaign.Name, Subject: campaign.Subject, SourceLocale: "en",
		Target: &managev1.CreateCampaignRequest_SegmentId{SegmentId: segmentID},
	}))
	require.NoError(t, err)
	campaignID = createdCampaign.Msg.Campaign.Id
	require.NoError(t, db.First(&campaign, "id = ?", campaignID).Error)
	publishCampaignSourceForDeliveryIntegration(
		t,
		db,
		campaign.ID,
		campaign.Subject,
		content,
	)
	require.NoError(t, db.Model(&model.Campaign{}).
		Where("id = ?", campaign.ID).
		Updates(structured.Fields{
			"status":       managev1.CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String(),
			"scheduled_at": now,
		}).Error)
	var run *model.CampaignDeliveryRun
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		ref, createErr := createCampaignDeliveryRun(
			t.Context(),
			tx,
			campaign,
			now,
			0,
			campaignAudienceTargets{},
			testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient),
			nil,
			NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil),
		)
		if createErr != nil {
			return createErr
		}
		var persisted model.CampaignDeliveryRun
		if err := tx.First(&persisted, "id = ?", ref.ID).Error; err != nil {
			return err
		}
		run = &persisted
		return nil
	}))
	require.NotNil(t, run)
	runID = run.ID

	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	blockerFinished := false
	defer func() {
		if !blockerFinished {
			require.NoError(t, blocker.Rollback().Error)
		}
	}()
	require.NoError(t, blocker.Exec(`SET LOCAL lock_timeout = '5s'`).Error)
	var lockedCampaign model.Campaign
	require.NoError(t, blocker.Clauses(clause.Locking{
		Strength: "UPDATE",
	}).First(&lockedCampaign, "id = ?", campaign.ID).Error)

	archiveApplication := "geul_archive_cancel_" +
		strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	cancelApplication := "geul_cancel_archive_" +
		strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	archiveDB := newNamedCampaignDeliveryIntegrationDB(t, archiveApplication)
	cancelDB := newNamedCampaignDeliveryIntegrationDB(t, cancelApplication)

	archiveResult := make(chan error, 1)
	go func() {
		_, archiveErr := newCampaignAudienceService(archiveDB, stack.SpiceDBClient).ArchiveSegment(
			adminContext,
			connect.NewRequest(&managev1.ArchiveSegmentRequest{Id: segmentID}),
		)
		archiveResult <- archiveErr
	}()
	cancelResult := make(chan error, 1)
	go func() {
		_, cancelErr := NewCampaignService(
			cancelDB,
			newCampaignRuntimeFixture(nil, nil),
			"https://cdn.example.test",
			"https://example.test",
			stack.SpiceDBClient,
			WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil)),
		).CancelCampaign(
			adminContext,
			connect.NewRequest(&managev1.CancelCampaignRequest{Id: campaign.ID}),
		)
		cancelResult <- cancelErr
	}()

	requireCampaignDeliveryOperationWaitingOnLock(
		t,
		db,
		archiveApplication,
	)
	requireCampaignDeliveryOperationWaitingOnLock(
		t,
		db,
		cancelApplication,
	)
	require.NoError(t, blocker.Commit().Error)
	blockerFinished = true

	select {
	case archiveErr := <-archiveResult:
		logPostgresErrorDetail(t, archiveErr)
		require.NoError(t, archiveErr)
	case <-time.After(5 * time.Second):
		t.Fatal("audience archive deadlocked with campaign cancellation")
	}
	select {
	case cancelErr := <-cancelResult:
		if cancelErr != nil {
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(cancelErr))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("campaign cancellation deadlocked with audience archive")
	}

	var persistedSegment model.AudienceSegment
	require.NoError(t, db.First(&persistedSegment, "id = ?", segmentID).Error)
	require.NotNil(t, persistedSegment.ArchivedAt)
	var persistedCampaign model.Campaign
	require.NoError(t, db.First(&persistedCampaign, "id = ?", campaign.ID).Error)
	require.Equal(t, managev1.CampaignStatus_CAMPAIGN_STATUS_DRAFT.String(), persistedCampaign.Status)
	var persistedRun model.CampaignDeliveryRun
	require.NoError(t, db.First(&persistedRun, "id = ?", run.ID).Error)
	require.Equal(t, CampaignDeliveryRunStatusCancelled, persistedRun.Status)
}

func TestCampaignDeliveryMutationEntryPointsLockCampaignBeforeRunIntegration(
	t *testing.T,
) {
	tests := []struct {
		name            string
		runStatus       string
		recipientStatus string
		invoke          func(
			context.Context,
			*gorm.DB,
			*auth.SpiceDBClient,
			*recordingCampaignPublisher,
			string,
		) error
		wantRunStatus      string
		wantCampaignStatus string
		wantPublishCount   int
	}{
		{
			name:      "materialize",
			runStatus: CampaignDeliveryRunStatusSending,
			invoke: func(
				ctx context.Context,
				db *gorm.DB,
				spiceDB *auth.SpiceDBClient,
				_ *recordingCampaignPublisher,
				runID string,
			) error {
				return MaterializeCampaignDeliveryRun(ctx, db, spiceDB, runID)
			},
			wantRunStatus: CampaignDeliveryRunStatusSending,
			wantCampaignStatus: managev1.
				CampaignStatus_CAMPAIGN_STATUS_SENDING.String(),
		},
		{
			name:            "finalize",
			runStatus:       CampaignDeliveryRunStatusSending,
			recipientStatus: CampaignDeliveryRecipientStatusPending,
			invoke: func(
				ctx context.Context,
				db *gorm.DB,
				_ *auth.SpiceDBClient,
				_ *recordingCampaignPublisher,
				runID string,
			) error {
				var recipient model.CampaignDeliveryRecipient
				if err := db.WithContext(ctx).
					Select("id").
					First(&recipient, "run_id = ?", runID).Error; err != nil {
					return err
				}
				return MarkCampaignDeliveryRecipientResultWithAudit(
					ctx,
					db,
					nil,
					recipient.ID,
					CampaignDeliveryRecipientStatusSent,
					"provider-message-lock-test",
					"",
					nil,
				)
			},
			wantRunStatus: CampaignDeliveryRunStatusSent,
			wantCampaignStatus: managev1.
				CampaignStatus_CAMPAIGN_STATUS_SENT.String(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newCampaignConcurrentIntegrationDB(t)
			stack := testutil.SetupOryStack(t)
			admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
			adminContext := auth.WithUser(context.Background(), admin.AuthUserInfo())
			var runID, campaignID, identityID string
			t.Cleanup(func() {
				cleanupConcurrentCampaignDeliveryFixture(
					t,
					db,
					runID,
					campaignID,
					identityID,
					"",
				)
			})
			ensureBulkEmailAudienceKratosIdentityColumns(t, db)
			now := time.Now().UTC()
			content := "<p>Campaign mutation lock-order test</p>"
			campaignStatus := managev1.
				CampaignStatus_CAMPAIGN_STATUS_SENDING.String()
			if tt.runStatus == CampaignDeliveryRunStatusScheduled {
				campaignStatus = managev1.
					CampaignStatus_CAMPAIGN_STATUS_SCHEDULED.String()
			}
			campaignService := NewCampaignService(
				db, newCampaignRuntimeFixture(nil, nil), "", "", stack.SpiceDBClient,
				WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)),
			)
			created, err := campaignService.CreateCampaign(
				adminContext,
				connect.NewRequest(&managev1.CreateCampaignRequest{
					Name:         "Campaign mutation lock-order test",
					Subject:      "Campaign mutation lock-order test",
					SourceLocale: "en",
					Target:       campaignAllTarget(),
				}),
			)
			require.NoError(t, err)
			campaignID = created.Msg.Campaign.Id
			publishCampaignSourceBlocksForIntegration(
				t,
				db,
				stack.SpiceDBClient,
				campaignID,
				emailutil.StripHTML(content),
			)
			require.NoError(t, db.Model(&model.Campaign{}).
				Where("id = ?", campaignID).
				Updates(structured.Fields{
					"status":       campaignStatus,
					"scheduled_at": now,
				}).Error)
			var campaign model.Campaign
			require.NoError(t, db.First(&campaign, "id = ?", campaignID).Error)
			identityID = seedBulkEmailAudienceIdentity(
				t,
				db,
				"mutation-lock-"+uuid.NewString()+"@example.test",
				now,
			)
			memberID := memberIDForCampaignLockTest(t, db, identityID)

			var run *model.CampaignDeliveryRun
			require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
				ref, err := createCampaignDeliveryRun(
					t.Context(),
					tx,
					campaign,
					now,
					0,
					nil,
					testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient),
					nil,
					NewCampaignDeliveryRuntime(stack.SpiceDBClient, nil),
				)
				if err != nil {
					return err
				}
				var persisted model.CampaignDeliveryRun
				if err := tx.First(&persisted, "id = ?", ref.ID).Error; err != nil {
					return err
				}
				run = &persisted
				return nil
			}))
			require.NotNil(t, run)
			runID = run.ID
			runUpdates := structured.Fields{
				"status":       tt.runStatus,
				"target_count": 1,
			}
			if tt.runStatus == CampaignDeliveryRunStatusSending {
				runUpdates["started_at"] = now
			}
			require.NoError(t, db.Model(&model.CampaignDeliveryRun{}).
				Where("id = ?", run.ID).
				Updates(runUpdates).Error)
			if tt.recipientStatus != "" {
				recipient := model.CampaignDeliveryRecipient{
					ID: uuid.NewString(), RunID: run.ID,
					RecipientEmail: identityEmailForCampaignLockTest(
						t,
						db,
						identityID,
					),
					IdentityID:           &identityID,
					MemberID:             &memberID,
					RecipientContextType: BulkEmailContextNewsletterSubscription,
					Status:               tt.recipientStatus,
					CreatedAt:            now,
					UpdatedAt:            now,
				}
				if tt.recipientStatus == CampaignDeliveryRecipientStatusSent {
					providerMessageID := "provider-message-lock-fixture"
					recipient.ProviderMessageID = &providerMessageID
					recipient.TerminalAt = &now
				}
				recipient.NormalizedRecipientEmail = emailutil.NormalizeAddressForDelivery(
					recipient.RecipientEmail,
				)
				require.NoError(t, db.Create(&recipient).Error)
			}
			blocker := db.Begin()
			require.NoError(t, blocker.Error)
			blockerFinished := false
			defer func() {
				if !blockerFinished {
					require.NoError(t, blocker.Rollback().Error)
				}
			}()
			var lockedCampaign model.Campaign
			require.NoError(t, blocker.Clauses(
				clause.Locking{Strength: "UPDATE"},
			).First(&lockedCampaign, "id = ?", campaign.ID).Error)

			applicationName := "geul_entry_lock_" +
				strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
			operationDB := newNamedCampaignDeliveryIntegrationDB(
				t,
				applicationName,
			)
			publisher := &recordingCampaignPublisher{}
			result := make(chan error, 1)
			go func() {
				result <- tt.invoke(
					context.Background(),
					operationDB,
					stack.SpiceDBClient,
					publisher,
					run.ID,
				)
			}()

			requireCampaignDeliveryOperationWaitingOnLock(
				t,
				db,
				applicationName,
			)
			probe := db.Begin()
			require.NoError(t, probe.Error)
			var unlockedRun model.CampaignDeliveryRun
			require.NoError(t, probe.Clauses(clause.Locking{
				Strength: "UPDATE",
				Options:  "NOWAIT",
			}).First(&unlockedRun, "id = ?", run.ID).Error)
			require.NoError(t, probe.Rollback().Error)

			require.NoError(t, blocker.Rollback().Error)
			blockerFinished = true
			select {
			case err := <-result:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("campaign delivery mutation did not resume")
			}

			var persistedRun model.CampaignDeliveryRun
			require.NoError(t, db.First(
				&persistedRun,
				"id = ?",
				run.ID,
			).Error)
			require.Equal(t, tt.wantRunStatus, persistedRun.Status)
			var persistedCampaign model.Campaign
			require.NoError(t, db.First(
				&persistedCampaign,
				"id = ?",
				campaign.ID,
			).Error)
			require.Equal(
				t,
				tt.wantCampaignStatus,
				persistedCampaign.Status,
			)
			require.Len(t, publisher.sendBulkJobs, tt.wantPublishCount)
		})
	}
}

func newNamedCampaignDeliveryIntegrationDB(
	t *testing.T,
	applicationName string,
) *gorm.DB {
	t.Helper()
	stack := testutil.SetupOryStack(t)
	db, err := gorm.Open(gormpostgres.Open(stack.PostgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() {
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.Exec(
		`SELECT set_config('application_name', ?, false)`,
		applicationName,
	).Error)
	return db
}

func requireCampaignDeliveryOperationWaitingOnLock(
	t *testing.T,
	db *gorm.DB,
	applicationName string,
) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting bool
		if err := db.Raw(
			`SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE application_name = ?
					AND wait_event_type = 'Lock'
			)`,
			applicationName,
		).Scan(&waiting).Error; err != nil {
			return false
		}
		return waiting
	}, 5*time.Second, 20*time.Millisecond)
}

func identityEmailForCampaignLockTest(
	t *testing.T,
	db *gorm.DB,
	identityID string,
) string {
	t.Helper()
	var email string
	require.NoError(t, db.Model(&model.Member{}).
		Where("account_identity_id = ? AND deleted_at IS NULL", identityID).
		Pluck("primary_email", &email).Error)
	require.NotEmpty(t, email)
	return email
}

func memberIDForCampaignLockTest(
	t *testing.T,
	db *gorm.DB,
	identityID string,
) string {
	t.Helper()
	var memberID string
	require.NoError(t, db.Model(&model.Member{}).
		Where("account_identity_id = ? AND deleted_at IS NULL", identityID).
		Pluck("id", &memberID).Error)
	require.NotEmpty(t, memberID)
	return memberID
}

func TestCampaignDeliveryDispatcherEnqueuesDueRunInPGMQIntegration(t *testing.T) {
	db := newCampaignDeliveryIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	sqlDB, err := stack.DB.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	publisher, err := mq.NewPublisher(sqlDB)
	require.NoError(t, err)
	require.NoError(t, testutil.PurgePGMQQueue(ctx, sqlDB, eventpkg.QueueEmailCampaign))
	t.Cleanup(func() {
		require.NoError(t, testutil.PurgePGMQQueue(context.Background(), sqlDB, eventpkg.QueueEmailCampaign))
	})

	content := "<p>PGMQ scheduled campaign body</p>"
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx = auth.WithUser(context.Background(), admin.AuthUserInfo())
	campaignSvc := NewCampaignService(
		db, newCampaignRuntimeFixture(publisher, nil), "https://cdn.example.test", "https://www.example.test", stack.SpiceDBClient,
		WithCampaignContentBlockStore(testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)),
		WithCampaignEmailDelivery(NewCampaignDeliveryRuntime(stack.SpiceDBClient, publisher)),
	)
	created, err := campaignSvc.CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name:         "PGMQ scheduled campaign " + uuid.NewString(),
		Subject:      "PGMQ scheduled subject",
		SourceLocale: "en",
		Target:       campaignAllTarget(),
	}))
	require.NoError(t, err)
	campaignID := created.Msg.Campaign.Id
	publishCampaignSourceBlocksForIntegration(t, db, stack.SpiceDBClient, campaignID, emailutil.StripHTML(content))
	seedBulkEmailAudienceIdentity(t, db, "queue-scheduled-"+uuid.NewString()+"@example.com", time.Now().UTC())

	scheduledAt := time.Now().Add(2 * time.Second)
	_, err = campaignSvc.ScheduleCampaign(ctx, connect.NewRequest(&managev1.ScheduleCampaignRequest{
		Id:          campaignID,
		ScheduledAt: timestamppb.New(scheduledAt),
	}))
	require.NoError(t, err)

	var run model.CampaignDeliveryRun
	require.NoError(t, db.First(&run, "campaign_id = ?", campaignID).Error)
	require.WithinDuration(t, scheduledAt, run.ScheduledAt, time.Millisecond)

	dispatcher := NewAuditedCampaignDeliveryDispatcher(
		db,
		stack.SpiceDBClient,
		publisher,
		&campaignLocaleAuditCapture{},
	)
	var (
		dispatched  int
		dispatchErr error
	)
	require.Eventually(t, func() bool {
		dispatched, dispatchErr = dispatcher.DispatchDueEmailDeliveryRuns(ctx, 10)
		return dispatchErr == nil && dispatched == 1
	}, 5*time.Second, 20*time.Millisecond)
	require.NoError(t, dispatchErr)
	require.Equal(t, 1, dispatched)

	campaignEvent := requirePGMQCampaignEvent(t, sqlDB)
	require.Equal(t, run.ID, campaignEvent.GetDeliveryRunId())
}

func requirePGMQCampaignEvent(t *testing.T, db *sql.DB) *managev1.SendBulkEmailBatchEvent {
	t.Helper()

	var body []byte
	require.Eventually(t, func() bool {
		messages, err := testutil.ReadPGMQ(t.Context(), db, eventpkg.QueueEmailCampaign, time.Minute, 1)
		if err != nil || len(messages) == 0 {
			return false
		}
		message := messages[0]
		body, err = message.Envelope.Payload()
		require.NoError(t, err)
		require.NoError(t, testutil.CompletePGMQ(t.Context(), db, eventpkg.QueueEmailCampaign, message.TransportID))
		return true
	}, 5*time.Second, 50*time.Millisecond)

	var event managev1.SendBulkEmailBatchEvent
	require.NoError(t, proto.Unmarshal(body, &event))
	return &event
}
