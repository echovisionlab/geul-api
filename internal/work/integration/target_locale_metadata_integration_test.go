//go:build integration

package integration

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/testutil"
	workdomain "github.com/echovisionlab/geul-api/internal/work"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestUpdateWorkLocaleMetadataTrimsTargetTitleIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	identityID := testutil.IntegrationUUID()
	testutil.SeedPostIntegrationIdentity(t, db, identityID, "Work target title trim")
	ctx := workIntegrationAdminCtx(identityID)
	workService := newWorkIntegrationService(t, db, identityID, referenceNoopFileDeleter{})
	isPresent := true
	created, err := workService.CreateWork(ctx, connect.NewRequest(&managev1.CreateWorkRequest{
		Title: "Work target title trim " + testutil.IntegrationUUID(),
		Type:  managev1.WorkType_WORK_TYPE_ARTICLE, Year: 2026, Month: 8, IsPresent: &isPresent,
		Document: workTargetCASIntegrationDocument(uuid.New(), "en", "source"),
	}))
	require.NoError(t, err)
	store, err := contentblock.NewGeneratedStore(newContentBlockFileReuseAuthorizer(integrationSpiceDB(t)))
	require.NoError(t, err)
	application := newWorkServiceForTest(t, workdomain.Deps{DB: db, ContentBlocks: store})
	internal := workdomain.NewInternalConnectHandler(application)
	contributor := testutil.PostIntegrationMemberID(identityID)
	missing, err := application.LoadAIDocument(ctx, created.Msg.Id, "fr")
	require.NoError(t, err)
	_, err = applyWorkAIDocument(ctx, application, workAIDocumentIntegrationMutation(missing, contributor, func(mutation *workdomain.AIDocumentMutation) {
		mutation.CreateTranslation = true
	}))
	require.NoError(t, err)

	update := func(title string) *intrav1.UpdateWorkLocaleMetadataResponse {
		t.Helper()
		state, loadErr := application.LoadAIDocument(ctx, created.Msg.Id, "fr")
		require.NoError(t, loadErr)
		response, updateErr := internal.UpdateWorkLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateWorkLocaleMetadataRequest{
			WorkId: created.Msg.Id, Locale: "fr", Title: &title,
			ExpectedRevision: state.DocumentRevision, ExpectedTargetRevision: state.TargetRevision,
			ContributorMemberIds: []string{contributor},
		}))
		require.NoError(t, updateErr)
		return response.Msg
	}

	require.True(t, update("  Nouveau titre ").Changed)
	require.Equal(t, "Nouveau titre", *loadWorkPointerLocale(t, db, created.Msg.Id, "fr").Title)
	require.False(t, update(" Nouveau titre  ").Changed, "a padded copy of the current title is not a change")
	require.Equal(t, "Nouveau titre", *loadWorkPointerLocale(t, db, created.Msg.Id, "fr").Title)
}
