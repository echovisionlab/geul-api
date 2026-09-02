//go:build integration

package page

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/contentblock"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
)

func TestPageAIDocumentExactMutationAuthorizesBeforeCompilerOnceIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	identityID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, identityID, "Page AI document exact mutation")
	spiceDB := integrationSpiceDB(t)
	grantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	ctx := workIntegrationAdminCtx(identityID)
	store := newPageIntegrationContentBlockStore(t, spiceDB)
	pageService := NewPageService(
		db,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		&recordingPageDeleteFileDeleter{},
		noopAsyncPublisher{},
		&fakeIdentityManager{identity: postIntegrationIdentity(identityID, "en")},
		spiceDB,
		WithPageContentBlockStore(store),
		WithPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	created, err := pageService.CreatePage(ctx, connect.NewRequest(&managev1.CreatePageRequest{
		Title: "Page AI document exact mutation",
	}))
	require.NoError(t, err)

	assertCompilerBoundary := func(allowed bool) {
		t.Helper()
		checker := &countingPageAIDocumentPermissionChecker{allowed: allowed}
		internal := NewInternalPageService(
			db,
			noopAsyncPublisher{},
			checker,
			newPageRuntimeForTest(db, "https://cdn.example.com"),
			WithInternalPageContentBlockStore(store),
			WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
		)
		application, err := NewAIDocumentService(internal)
		require.NoError(t, err)
		compilerFailure := errors.New("stop after authorized Page compiler")
		compilerCalls := 0
		_, err = application.ExecuteAIDocumentMutation(
			ctx,
			created.Msg.Id,
			"en",
			AIDocumentExecutionValidate,
			func(state AIDocumentState) (AIDocumentMutation, error) {
				compilerCalls++
				require.Equal(t, created.Msg.Id, state.Page.ID)
				return AIDocumentMutation{}, compilerFailure
			},
		)
		if allowed {
			require.ErrorIs(t, err, compilerFailure)
			require.Equal(t, 1, compilerCalls)
		} else {
			require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
			require.Zero(t, compilerCalls, "authorization denial reached the Page compiler")
		}
		require.Equal(t, []string{"edit"}, checker.actions)
		require.Equal(t, []string{created.Msg.Id}, checker.resourceIDs)
	}

	assertCompilerBoundary(true)
	assertCompilerBoundary(false)

	checker := &countingPageAIDocumentPermissionChecker{allowed: true}
	internal := NewInternalPageService(
		db,
		noopAsyncPublisher{},
		checker,
		newPageRuntimeForTest(db, "https://cdn.example.com"),
		WithInternalPageContentBlockStore(store),
		WithInternalPageContentBlockMediaHydrator(passthroughPageContentBlockMediaHydrator{}),
	)
	application, err := NewAIDocumentService(internal)
	require.NoError(t, err)
	var beforeTitle string
	var beforeRevision uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.title, document.revision
		FROM page
		JOIN page_translation AS translation
		  ON translation.entity_id = page.id
		 AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = page.content_document_id
		WHERE page.id = ?
	`, created.Msg.Id).Row().Scan(&beforeTitle, &beforeRevision))
	replacement := "Page Validate must roll back"
	result, err := application.ExecuteAIDocumentMutation(
		ctx,
		created.Msg.Id,
		"en",
		AIDocumentExecutionValidate,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			contributor := uuid.MustParse(state.ViewerMemberID)
			return AIDocumentMutation{
				PageID:               state.Page.ID,
				Locale:               state.Locale,
				ObservedSourceLocale: state.SourceLocale,
				ExpectedRevision:     state.Snapshot.Document.Revision,
				ObservedLocaleExists: state.LocaleExists,
				ContributorMemberID:  state.ViewerMemberID,
				Metadata:             AIDocumentMetadataPatch{SetTitle: true, Title: &replacement},
				Batch: contentblock.Batch{
					DocumentID:           state.Snapshot.Document.ID,
					ExpectedRevision:     state.Snapshot.Document.Revision,
					ContributorMemberIDs: []uuid.UUID{contributor},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, result.Changed)
	var afterTitle string
	var afterRevision uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.title, document.revision
		FROM page
		JOIN page_translation AS translation
		  ON translation.entity_id = page.id
		 AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = page.content_document_id
		WHERE page.id = ?
	`, created.Msg.Id).Row().Scan(&afterTitle, &afterRevision))
	require.Equal(t, beforeTitle, afterTitle)
	require.Equal(t, beforeRevision, afterRevision)
	require.Equal(t, []string{"edit"}, checker.actions)
}

type countingPageAIDocumentPermissionChecker struct {
	allowed     bool
	actions     []string
	resourceIDs []string
}

func (c *countingPageAIDocumentPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.actions = append(c.actions, decision.Action().Permission())
	c.resourceIDs = append(c.resourceIDs, decision.Resource().ID())
	return c.allowed, nil
}

func (c *countingPageAIDocumentPermissionChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	can policyv1.Can,
) (bool, error) {
	c.actions = append(c.actions, can.Action().Permission())
	c.resourceIDs = append(c.resourceIDs, can.Resource().ID())
	return c.allowed, nil
}

var _ CollaborationPermissionChecker = (*countingPageAIDocumentPermissionChecker)(nil)
