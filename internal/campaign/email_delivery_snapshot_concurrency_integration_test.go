//go:build integration

package campaign

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestEmailLayoutDeleteAndCampaignAssignmentDoNotDeadlockIntegration(
	t *testing.T,
) {
	for _, deleteFirst := range []bool{false, true} {
		name := "assignment_first"
		if deleteFirst {
			name = "delete_first"
		}
		t.Run(name, func(t *testing.T) {
			db := newCampaignConcurrentIntegrationDB(t)
			ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
			contentBlocks := testutil.NewEmailContentBlockStore(t, spiceDB)
			references := emailauthoringadapter.NewCampaignDeliveryReferences()
			campaignID := ""
			layoutID := ""
			t.Cleanup(func() {
				cleanupConcurrentCampaignSnapshotFixture(
					t,
					db,
					ctx,
					spiceDB,
					"",
					campaignID,
					layoutID,
				)
			})
			layout, err := emailauthoring.NewEmailLayoutService(
				db,
				"https://cdn.example.test",
				"https://www.example.test",
				spiceDB,
				emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
				emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
			).CreateEmailLayout(
				ctx,
				connect.NewRequest(&managev1.CreateEmailLayoutRequest{
					Name: "Delete race layout " + uuid.NewString(),
					Key: "delete_race_layout_" +
						strings.ReplaceAll(uuid.NewString(), "-", "_"),
					HtmlContent:  "<html><body>{{content}}</body></html>",
					SourceLocale: "en",
				}),
			)
			require.NoError(t, err)
			layoutID = layout.Msg.Id
			campaign, err := NewCampaignService(
				db,
				newCampaignRuntimeFixture(nil, nil),
				"https://cdn.example.test",
				"https://www.example.test",
				spiceDB,
				WithCampaignContentBlockStore(contentBlocks),
				WithCampaignEmailAuthoring(campaignEmailAuthoringConcurrencyFixture{}),
			).CreateCampaign(
				ctx,
				connect.NewRequest(&managev1.CreateCampaignRequest{
					Name:         "Delete race campaign " + uuid.NewString(),
					Subject:      "Delete race",
					SourceLocale: "en",
					Target:       campaignAllTarget(),
				}),
			)
			require.NoError(t, err)
			campaignID = campaign.Msg.Campaign.Id

			deleteApplication := durableEmailApplicationName(
				"layout_delete",
			)
			assignmentApplication := durableEmailApplicationName(
				"layout_assign",
			)
			deleteDB := newNamedCampaignDeliveryIntegrationDB(
				t,
				deleteApplication,
			)
			assignmentDB := newNamedCampaignDeliveryIntegrationDB(
				t,
				assignmentApplication,
			)
			deleteSource := func(
				operationCtx context.Context,
				operationDB *gorm.DB,
			) error {
				_, deleteErr := emailauthoring.NewEmailLayoutService(
					operationDB,
					"https://cdn.example.test",
					"https://www.example.test",
					spiceDB,
					emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
					emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
				).DeleteEmailLayout(
					operationCtx,
					connect.NewRequest(&managev1.DeleteEmailLayoutRequest{
						Id: layout.Msg.Id,
					}),
				)
				return deleteErr
			}
			assign := func(
				operationCtx context.Context,
				operationDB *gorm.DB,
			) error {
				_, assignErr := NewCampaignService(
					operationDB,
					newCampaignRuntimeFixture(nil, nil),
					"https://cdn.example.test",
					"https://www.example.test",
					spiceDB,
					WithCampaignEmailAuthoring(campaignEmailAuthoringConcurrencyFixture{}),
				).UpdateCampaignConfiguration(
					operationCtx,
					connect.NewRequest(
						&managev1.UpdateCampaignConfigurationRequest{
							Id:             campaign.Msg.Campaign.Id,
							TargetMode:     managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL,
							LayoutId:       &layoutID,
							RecipientScope: managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS,
						},
					),
				)
				return assignErr
			}

			start := make(chan struct{})
			deleteDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
				<-start
				return deleteSource(operationCtx, deleteDB)
			})
			assignmentDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
				<-start
				return assign(operationCtx, assignmentDB)
			})
			close(start)
			deleteErr := waitForDurableEmailOperation(t, deleteDone)
			assignmentErr := waitForDurableEmailOperation(t, assignmentDone)
			deleteWon := deleteErr == nil
			if deleteWon {
				require.Equal(t, connect.CodeNotFound, connect.CodeOf(assignmentErr))
			} else {
				require.NoError(t, assignmentErr)
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(deleteErr), deleteErr)
			}

			var storedLayout model.EmailLayout
			layoutErr := db.First(&storedLayout, "id = ?", layout.Msg.Id).Error
			var storedCampaign model.Campaign
			require.NoError(t, db.First(
				&storedCampaign,
				"id = ?",
				campaign.Msg.Campaign.Id,
			).Error)
			if deleteWon {
				require.ErrorIs(t, layoutErr, gorm.ErrRecordNotFound)
				require.Nil(t, storedCampaign.LayoutID)
			} else {
				require.NoError(t, layoutErr)
				require.Equal(t, layout.Msg.Id, ptrStringValue(storedCampaign.LayoutID))
			}
		})
	}
}

func TestEmailLayoutDeleteAndTemplateAssignmentDoNotDeadlockIntegration(
	t *testing.T,
) {
	for _, deleteFirst := range []bool{false, true} {
		name := "assignment_first"
		if deleteFirst {
			name = "delete_first"
		}
		t.Run(name, func(t *testing.T) {
			db := newCampaignConcurrentIntegrationDB(t)
			ctx, spiceDB := testutil.IntegrationAdminContext(t, db)
			contentBlocks := testutil.NewEmailContentBlockStore(t, spiceDB)
			references := emailauthoringadapter.NewCampaignDeliveryReferences()
			layoutID := ""
			templateID := ""
			t.Cleanup(func() {
				templateService := emailauthoring.NewEmailTemplateService(
					db,
					nil,
					emailauthoringadapter.NewRuntime(),
					"https://cdn.example.test",
					"https://www.example.test",
					spiceDB,
					emailauthoring.WithEmailTemplateContentBlockStore(contentBlocks),
					emailauthoring.WithEmailTemplateCampaignDeliveryReferences(references),
				)
				if templateID != "" {
					noLayout := ""
					_, err := templateService.UpdateEmailTemplate(
						ctx,
						connect.NewRequest(
							&managev1.UpdateEmailTemplateRequest{
								Id:       templateID,
								LayoutId: &noLayout,
							},
						),
					)
					if connect.CodeOf(err) != connect.CodeNotFound {
						require.NoError(t, err)
					}
					_, err = templateService.DeleteEmailTemplate(
						ctx,
						connect.NewRequest(
							&managev1.DeleteEmailTemplateRequest{
								Id: templateID,
							},
						),
					)
					if connect.CodeOf(err) != connect.CodeNotFound {
						require.NoError(t, err)
					}
				}
				if layoutID != "" {
					_, err := emailauthoring.NewEmailLayoutService(
						db,
						"https://cdn.example.test",
						"https://www.example.test",
						spiceDB,
						emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
						emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
					).DeleteEmailLayout(
						ctx,
						connect.NewRequest(
							&managev1.DeleteEmailLayoutRequest{
								Id: layoutID,
							},
						),
					)
					if connect.CodeOf(err) != connect.CodeNotFound {
						require.NoError(t, err)
					}
				}
			})

			layout, err := emailauthoring.NewEmailLayoutService(
				db,
				"https://cdn.example.test",
				"https://www.example.test",
				spiceDB,
				emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
				emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
			).CreateEmailLayout(
				ctx,
				connect.NewRequest(&managev1.CreateEmailLayoutRequest{
					Name: "Template deleteSource race layout " +
						uuid.NewString(),
					Key: "template_delete_race_layout_" +
						strings.ReplaceAll(uuid.NewString(), "-", "_"),
					HtmlContent:  "<html><body>{{content}}</body></html>",
					SourceLocale: "en",
				}),
			)
			require.NoError(t, err)
			layoutID = layout.Msg.Id
			template, err := emailauthoring.NewEmailTemplateService(
				db,
				nil,
				emailauthoringadapter.NewRuntime(),
				"https://cdn.example.test",
				"https://www.example.test",
				spiceDB,
				emailauthoring.WithEmailTemplateContentBlockStore(contentBlocks),
				emailauthoring.WithEmailTemplateCampaignDeliveryReferences(references),
			).CreateEmailTemplate(
				ctx,
				connect.NewRequest(&managev1.CreateEmailTemplateRequest{
					Key: "template_delete_race_" +
						strings.ReplaceAll(uuid.NewString(), "-", "_"),
					Name:         "Template deleteSource race " + uuid.NewString(),
					Subject:      "Template deleteSource race",
					SourceLocale: "en",
				}),
			)
			require.NoError(t, err)
			templateID = template.Msg.Id

			deleteApplication := durableEmailApplicationName(
				"template_layout_delete",
			)
			assignmentApplication := durableEmailApplicationName(
				"template_layout_assign",
			)
			deleteDB := newNamedCampaignDeliveryIntegrationDB(
				t,
				deleteApplication,
			)
			assignmentDB := newNamedCampaignDeliveryIntegrationDB(
				t,
				assignmentApplication,
			)
			deleteSource := func(
				operationCtx context.Context,
				operationDB *gorm.DB,
			) error {
				_, deleteErr := emailauthoring.NewEmailLayoutService(
					operationDB,
					"https://cdn.example.test",
					"https://www.example.test",
					spiceDB,
					emailauthoring.WithEmailLayoutCampaignDeliveryReferences(references),
					emailauthoring.WithEmailLayoutContentBlockStore(contentBlocks),
				).DeleteEmailLayout(
					operationCtx,
					connect.NewRequest(
						&managev1.DeleteEmailLayoutRequest{
							Id: layout.Msg.Id,
						},
					),
				)
				return deleteErr
			}
			assign := func(
				operationCtx context.Context,
				operationDB *gorm.DB,
			) error {
				requestedLayoutID := layout.Msg.Id
				_, assignErr := emailauthoring.NewEmailTemplateService(
					operationDB,
					nil,
					emailauthoringadapter.NewRuntime(),
					"https://cdn.example.test",
					"https://www.example.test",
					spiceDB,
					emailauthoring.WithEmailTemplateContentBlockStore(contentBlocks),
					emailauthoring.WithEmailTemplateCampaignDeliveryReferences(references),
				).UpdateEmailTemplate(
					operationCtx,
					connect.NewRequest(
						&managev1.UpdateEmailTemplateRequest{
							Id:       template.Msg.Id,
							LayoutId: &requestedLayoutID,
						},
					),
				)
				return assignErr
			}

			start := make(chan struct{})
			deleteDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
				<-start
				return deleteSource(operationCtx, deleteDB)
			})
			assignmentDone := startDurableEmailOperation(t, ctx, func(operationCtx context.Context) error {
				<-start
				return assign(operationCtx, assignmentDB)
			})
			close(start)
			deleteErr := waitForDurableEmailOperation(t, deleteDone)
			assignmentErr := waitForDurableEmailOperation(t, assignmentDone)
			deleteWon := deleteErr == nil
			if deleteWon {
				require.Equal(t, connect.CodeNotFound, connect.CodeOf(assignmentErr))
			} else {
				require.NoError(t, assignmentErr)
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(deleteErr))
			}

			var storedLayout model.EmailLayout
			layoutErr := db.First(
				&storedLayout,
				"id = ?",
				layout.Msg.Id,
			).Error
			var storedTemplate model.EmailTemplate
			require.NoError(t, db.First(
				&storedTemplate,
				"id = ?",
				template.Msg.Id,
			).Error)
			if deleteWon {
				require.ErrorIs(t, layoutErr, gorm.ErrRecordNotFound)
				require.Nil(t, storedTemplate.LayoutID)
			} else {
				require.NoError(t, layoutErr)
				require.Equal(t, layout.Msg.Id, ptrStringValue(storedTemplate.LayoutID))
			}
		})
	}
}

func startDurableEmailOperation(
	t *testing.T,
	parentCtx context.Context,
	operation func(context.Context) error,
) <-chan error {
	t.Helper()
	operationCtx, cancel := context.WithCancel(parentCtx)
	done := make(chan error, 1)
	var worker sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		worker.Wait()
	})
	worker.Add(1)
	go func() {
		defer worker.Done()
		done <- operation(operationCtx)
	}()
	return done
}

func durableEmailApplicationName(prefix string) string {
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	return "geul_" + prefix + "_" + suffix[:16]
}

func waitForDurableEmailOperation(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for durable Email operation")
		return nil
	}
}

// campaignEmailAuthoringConcurrencyFixture mirrors the production Email
// Authoring adapter's deterministic Layout lock and snapshot boundary without
// importing that adapter back into its Campaign domain dependency.
type campaignEmailAuthoringConcurrencyFixture struct{}

func (campaignEmailAuthoringConcurrencyFixture) LockLayoutsForCampaign(
	ctx context.Context,
	tx *gorm.DB,
	layoutIDs ...string,
) (map[string]CampaignLayoutReference, error) {
	unique := make(map[string]struct{}, len(layoutIDs))
	for _, layoutID := range layoutIDs {
		layoutID = strings.TrimSpace(layoutID)
		if layoutID != "" {
			unique[layoutID] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return map[string]CampaignLayoutReference{}, nil
	}
	ordered := make([]string, 0, len(unique))
	for layoutID := range unique {
		ordered = append(ordered, layoutID)
	}
	sort.Strings(ordered)

	var layouts []CampaignLayoutReference
	if err := tx.WithContext(ctx).
		Table("email_layout").
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id, updated_at").
		Where("id IN ?", ordered).
		Order("id ASC").
		Find(&layouts).Error; err != nil {
		return nil, err
	}
	locked := make(map[string]CampaignLayoutReference, len(layouts))
	for _, layout := range layouts {
		locked[layout.ID] = layout
	}
	return locked, nil
}

func (campaignEmailAuthoringConcurrencyFixture) LoadLayoutSnapshot(
	ctx context.Context,
	db *gorm.DB,
	layoutID string,
) ([]CampaignLayoutLocaleSnapshot, error) {
	rows, err := email.LoadLayoutLocaleSnapshots(ctx, db, layoutID)
	if err != nil {
		return nil, err
	}
	snapshots := make([]CampaignLayoutLocaleSnapshot, 0, len(rows))
	for _, row := range rows {
		htmlContent := ""
		if row.ContentHTML != nil {
			htmlContent = *row.ContentHTML
		}
		snapshots = append(snapshots, CampaignLayoutLocaleSnapshot{
			Locale:         row.Locale,
			HTMLContent:    htmlContent,
			IsSourceLocale: row.IsSourceLocale,
		})
	}
	return snapshots, nil
}

func cleanupConcurrentCampaignSnapshotFixture(
	t *testing.T,
	db *gorm.DB,
	ctx context.Context,
	spiceDB *auth.SpiceDBClient,
	runID string,
	campaignID string,
	layoutID string,
) {
	t.Helper()
	runID = strings.TrimSpace(runID)
	campaignID = strings.TrimSpace(campaignID)
	layoutID = strings.TrimSpace(layoutID)
	if runID != "" {
		require.NoError(t, db.Exec(
			"DELETE FROM email_delivery_run WHERE id = ?",
			runID,
		).Error)
	}
	if campaignID != "" {
		// A failed assertion can happen after scheduling but before the fixture
		// records runID. Delete by durable owner as well so no active run leaks
		// into the lifecycle cleanup or a later subtest.
		require.NoError(t, db.Exec(
			"DELETE FROM email_delivery_run WHERE campaign_id = ?",
			campaignID,
		).Error)
		require.NoError(t, db.Exec(
			"DELETE FROM campaign WHERE id = ?",
			campaignID,
		).Error)
	}
	if layoutID != "" {
		_, err := emailauthoring.NewEmailLayoutService(
			db,
			"https://cdn.example.test",
			"https://www.example.test",
			spiceDB,
			emailauthoring.WithEmailLayoutCampaignDeliveryReferences(emailauthoringadapter.NewCampaignDeliveryReferences()),
			emailauthoring.WithEmailLayoutContentBlockStore(testutil.NewEmailContentBlockStore(t, spiceDB)),
		).DeleteEmailLayout(
			ctx,
			connect.NewRequest(&managev1.DeleteEmailLayoutRequest{
				Id: layoutID,
			}),
		)
		if connect.CodeOf(err) != connect.CodeNotFound {
			require.NoError(t, err)
		}
	}
}
