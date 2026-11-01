//go:build integration

package campaign

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	emailauthoringadapter "github.com/echovisionlab/geul-api/internal/adapters/emailauthoring"
	"github.com/echovisionlab/geul-api/internal/email"
	"github.com/echovisionlab/geul-api/internal/emailauthoring"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestEmailLayoutDeleteAndCampaignAssignmentDoNotDeadlockIntegration(
	t *testing.T,
) {
	t.Parallel()
	db := newCampaignConcurrentIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminAuditContext(t, db)
	contentBlocks := testutil.NewEmailContentBlockStore(t, spiceDB)
	references := emailauthoringadapter.NewCampaignDeliveryReferences()
	layout, err := emailauthoring.NewLayoutConnectHandler(newEmailAuthoringService(t, emailAuthoringDeps{DB: db, CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, References: references, ContentBlocks: contentBlocks})).CreateEmailLayout(
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
	campaign, err := NewConnectHandler(newTestService(t, Deps{DB: db, Runtime: newCampaignRuntimeFixture(nil, nil), CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, ContentBlocks: contentBlocks, EmailAuthoring: campaignEmailAuthoringConcurrencyFixture{}})).CreateCampaign(
		ctx,
		connect.NewRequest(&managev1.CreateCampaignRequest{
			Name:         "Delete race campaign " + uuid.NewString(),
			Subject:      "Delete race",
			SourceLocale: "en",
			Target:       campaignAllTarget(),
		}),
	)
	require.NoError(t, err)

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
		_, deleteErr := emailauthoring.NewLayoutConnectHandler(newEmailAuthoringService(t, emailAuthoringDeps{DB: operationDB, CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, References: references, ContentBlocks: contentBlocks})).DeleteEmailLayout(
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
		targetMode := managev1.CampaignTargetMode_CAMPAIGN_TARGET_MODE_ALL
		recipientScope := managev1.CampaignRecipientScope_CAMPAIGN_RECIPIENT_SCOPE_SUBSCRIBED_USERS
		_, assignErr := NewConnectHandler(newTestService(t, Deps{DB: operationDB, Runtime: newCampaignRuntimeFixture(nil, nil), CDNDomain: "https://cdn.example.test", SiteOrigin: "https://www.example.test", SpiceDB: spiceDB, EmailAuthoring: campaignEmailAuthoringConcurrencyFixture{}})).UpdateCampaignConfiguration(
			operationCtx,
			connect.NewRequest(
				&managev1.UpdateCampaignConfigurationRequest{
					Id:             campaign.Msg.Campaign.Id,
					TargetMode:     &targetMode,
					LayoutId:       &layout.Msg.Id,
					RecipientScope: &recipientScope,
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
	deleteErr := <-deleteDone
	assignmentErr := <-assignmentDone
	deleteWon := deleteErr == nil
	if deleteWon {
		require.Equal(t, connect.CodeNotFound, errs.CodeOf(assignmentErr))
	} else {
		require.NoError(t, assignmentErr)
		require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(deleteErr), deleteErr)
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
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
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
