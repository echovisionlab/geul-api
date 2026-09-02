//go:build integration

package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	githubtelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestCampaignAIDocumentExactMutationAuthorizesBeforeCompilerAndRollsBackValidateIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	store := testutil.NewEmailContentBlockStore(t, stack.SpiceDBClient)
	writer := githubtelemetry.NewDurableWriter(db)
	created, err := NewCampaignService(
		db, newCampaignRuntimeFixture(nil, nil), "", "", stack.SpiceDBClient,
		WithCampaignContentBlockStore(store),
	).CreateCampaign(ctx, connect.NewRequest(&managev1.CreateCampaignRequest{
		Name: "AI document exact Campaign", Subject: "Campaign before Validate",
		SourceLocale: "en", Target: campaignAllTarget(),
	}))
	require.NoError(t, err)
	campaignID := created.Msg.Campaign.Id

	checker := &countingCampaignAIDocumentPermissionChecker{allowed: true}
	application, err := NewAIDocumentService(NewAuditedInternalCampaignService(
		db, writer,
		WithInternalCampaignSpiceDB(checker),
		WithInternalCampaignContentBlockStore(store),
		WithInternalCampaignCheckpoints(testcollaboration.NewCheckpoints(db, stack.SpiceDBClient)),
	))
	require.NoError(t, err)
	compilerFailure := errors.New("stop after authorized Campaign compiler")
	compilerCalls := 0
	_, err = application.ExecuteAIDocumentMutation(
		ctx, campaignID, "en", AIDocumentExecutionValidate,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			compilerCalls++
			require.Equal(t, campaignID, state.CampaignID)
			return AIDocumentMutation{}, compilerFailure
		},
	)
	require.ErrorIs(t, err, compilerFailure)
	require.Equal(t, 1, compilerCalls)
	checker.requireOne(t, campaignID)

	var subjectBefore string
	var revisionBefore uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.subject, document.revision
		FROM campaign
		JOIN campaign_translation AS translation
		  ON translation.entity_id = campaign.id AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = campaign.content_document_id
		WHERE campaign.id = ?
	`, campaignID).Row().Scan(&subjectBefore, &revisionBefore))
	result, err := application.ExecuteAIDocumentMutation(
		ctx, campaignID, "en", AIDocumentExecutionValidate,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID := uuid.MustParse(state.ViewerMemberID)
			revision := uuid.MustParse(state.DocumentRevision)
			return AIDocumentMutation{
				CampaignID: state.CampaignID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
				SetSubject: true, Subject: "Campaign changed only inside Validate",
				Batch: &contentblock.Batch{
					DocumentID: state.DocumentID, ExpectedRevision: revision,
					ContributorMemberIDs: []uuid.UUID{memberID},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, result.Changed)
	checker.requireOne(t, campaignID)
	var subjectAfter string
	var revisionAfter uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.subject, document.revision
		FROM campaign
		JOIN campaign_translation AS translation
		  ON translation.entity_id = campaign.id AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = campaign.content_document_id
		WHERE campaign.id = ?
	`, campaignID).Row().Scan(&subjectAfter, &revisionAfter))
	require.Equal(t, subjectBefore, subjectAfter)
	require.Equal(t, revisionBefore, revisionAfter)
	requireCampaignAIDocumentLocaleAudits(t, db, campaignID, "en", 0)

	applySubject := "Campaign changed by Apply"
	result, err = application.ExecuteAIDocumentMutation(
		ctx, campaignID, "en", AIDocumentExecutionApply,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID := uuid.MustParse(state.ViewerMemberID)
			return AIDocumentMutation{
				CampaignID: state.CampaignID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
				SetSubject: true, Subject: applySubject,
				Batch: &contentblock.Batch{
					DocumentID: state.DocumentID, ExpectedRevision: uuid.MustParse(state.DocumentRevision),
					ContributorMemberIDs: []uuid.UUID{memberID},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, result.Changed)
	checker.requireOne(t, campaignID)
	requireCampaignAIDocumentLocaleAudits(t, db, campaignID, "en", 1)

	result, err = application.ExecuteAIDocumentMutation(
		ctx, campaignID, "en", AIDocumentExecutionApply,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID := uuid.MustParse(state.ViewerMemberID)
			return AIDocumentMutation{
				CampaignID: state.CampaignID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
				SetSubject: true, Subject: applySubject,
				Batch: &contentblock.Batch{
					DocumentID: state.DocumentID, ExpectedRevision: uuid.MustParse(state.DocumentRevision),
					ContributorMemberIDs: []uuid.UUID{memberID},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.False(t, result.Changed)
	checker.requireOne(t, campaignID)
	requireCampaignAIDocumentLocaleAudits(t, db, campaignID, "en", 1)

	checker.allowed = false
	compilerCalls = 0
	_, err = application.ExecuteAIDocumentMutation(
		ctx, campaignID, "en", AIDocumentExecutionApply,
		func(AIDocumentState) (AIDocumentMutation, error) {
			compilerCalls++
			return AIDocumentMutation{}, nil
		},
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Zero(t, compilerCalls)
	checker.requireOne(t, campaignID)
	requireCampaignAIDocumentLocaleAudits(t, db, campaignID, "en", 1)
}

func requireCampaignAIDocumentLocaleAudits(
	t *testing.T,
	db *gorm.DB,
	campaignID string,
	locale string,
	want int,
) {
	t.Helper()
	var rows []struct{ Attributes []byte }
	require.NoError(t, db.Raw(`
		SELECT attributes
		FROM public.domain_audit
		WHERE target_type = 'campaign' AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"]}'::jsonb
		ORDER BY occurred_at, audit_id
	`, campaignID).Scan(&rows).Error)
	require.Len(t, rows, want)
	for _, row := range rows {
		var attributes map[string]any
		require.NoError(t, json.Unmarshal(row.Attributes, &attributes))
		require.Equal(t, locale, attributes["locale"])
		require.Equal(t, "updated", attributes["item_operation"])
		require.NotContains(t, attributes, "source_revision")
	}
}

type countingCampaignAIDocumentPermissionChecker struct {
	allowed   bool
	decisions []policyv1.AuthorizationDecision
}

func (c *countingCampaignAIDocumentPermissionChecker) Can(
	_ context.Context,
	decision policyv1.AuthorizationDecision,
) (bool, error) {
	c.decisions = append(c.decisions, decision)
	return c.allowed, nil
}

func (c *countingCampaignAIDocumentPermissionChecker) CheckActorCan(
	_ context.Context,
	_ policyv1.Actor,
	_ policyv1.Can,
) (bool, error) {
	return c.allowed, nil
}

func (c *countingCampaignAIDocumentPermissionChecker) requireOne(t *testing.T, campaignID string) {
	t.Helper()
	require.Len(t, c.decisions, 1)
	require.Equal(t, campaignID, c.decisions[0].Resource().ID())
	require.Equal(t, "campaign", c.decisions[0].Resource().Type())
	require.Equal(t, "edit", c.decisions[0].Action().Name())
	require.Equal(t, "edit", c.decisions[0].Action().Permission())
	c.decisions = nil
}

var _ CollaborationPermissionChecker = (*countingCampaignAIDocumentPermissionChecker)(nil)
