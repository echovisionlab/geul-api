//go:build integration

package emailauthoring

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	authzedv1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	githubtelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func TestEmailAuthoringAIDocumentExactMutationAuthorizesBeforeCompilerAndRollsBackValidateIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	ctx, realSpiceDB := testutil.IntegrationAdminContext(t, db)
	store := testutil.NewEmailContentBlockStore(t, realSpiceDB)
	writer := githubtelemetry.NewDurableWriter(db)
	references := integrationCampaignDeliveryReferences{}

	template, err := NewEmailTemplateService(
		db, nil, emailTemplateRuntimeFixture{}, "", "", realSpiceDB,
		WithEmailTemplateContentBlockStore(store),
		WithEmailTemplateCampaignDeliveryReferences(references),
	).CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
		Key: "ai_document_exact_template_" + strings.ReplaceAll(testutil.IntegrationUUID(), "-", ""), Name: "AI document exact Template",
		Subject: "Template before Validate", SourceLocale: "en",
	}))
	require.NoError(t, err)

	layout, err := NewEmailLayoutService(
		db, "", "", realSpiceDB,
		WithEmailLayoutCampaignDeliveryReferences(references),
		WithEmailLayoutContentBlockStore(store),
	).CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Key: "ai_document_exact_layout_" + strings.ReplaceAll(testutil.IntegrationUUID(), "-", ""), Name: "AI document exact Layout",
		HtmlContent: "<html><body><p>Layout before Validate</p>{{content}}</body></html>", SourceLocale: "en",
	}))
	require.NoError(t, err)

	allowedSpiceDB, allowedRecorder := newEmailAuthoringAIDocumentPermissionServer(t, true)
	templateOwner := NewAuditedInternalEmailTemplateService(
		db, writer, allowedSpiceDB,
		WithInternalEmailTemplateContentBlockStore(store),
		WithInternalEmailTemplateCheckpoints(testcollaboration.NewCheckpoints(db, realSpiceDB)),
		WithInternalEmailTemplateCampaignDeliveryReferences(references),
	)
	templateApplication, err := NewAIDocumentService(templateOwner)
	require.NoError(t, err)

	compilerFailure := errors.New("stop after authorized Email Template compiler")
	compilerCalls := 0
	_, err = templateApplication.ExecuteAIDocumentMutation(
		ctx, template.Msg.Id, "en", AIDocumentExecutionValidate,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			compilerCalls++
			require.Equal(t, template.Msg.Id, state.TemplateID)
			return AIDocumentMutation{}, compilerFailure
		},
	)
	require.ErrorIs(t, err, compilerFailure)
	require.Equal(t, 1, compilerCalls)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailTemplate.Edit, template.Msg.Id,
	)

	var templateSubjectBefore string
	var templateRevisionBefore uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.subject, document.revision
		FROM email_template
		JOIN email_template_translation AS translation
		  ON translation.entity_id = email_template.id AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = email_template.content_document_id
		WHERE email_template.id = ?
	`, template.Msg.Id).Row().Scan(&templateSubjectBefore, &templateRevisionBefore))
	templateResult, err := templateApplication.ExecuteAIDocumentMutation(
		ctx, template.Msg.Id, "en", AIDocumentExecutionValidate,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID := uuid.MustParse(state.ViewerMemberID)
			revision := uuid.MustParse(state.DocumentRevision)
			return AIDocumentMutation{
				TemplateID: state.TemplateID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
				SetSubject: true, Subject: "Template changed only inside Validate",
				Batch: &contentblock.Batch{
					DocumentID: state.DocumentID, ExpectedRevision: revision,
					ContributorMemberIDs: []uuid.UUID{memberID},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, templateResult.Changed)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailTemplate.Edit, template.Msg.Id,
	)
	var templateSubjectAfter string
	var templateRevisionAfter uuid.UUID
	require.NoError(t, db.Raw(`
		SELECT translation.subject, document.revision
		FROM email_template
		JOIN email_template_translation AS translation
		  ON translation.entity_id = email_template.id AND translation.locale = 'en'
		JOIN content_document AS document ON document.id = email_template.content_document_id
		WHERE email_template.id = ?
	`, template.Msg.Id).Row().Scan(&templateSubjectAfter, &templateRevisionAfter))
	require.Equal(t, templateSubjectBefore, templateSubjectAfter)
	require.Equal(t, templateRevisionBefore, templateRevisionAfter)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_template", template.Msg.Id, "en", 0)

	templateApplySubject := "Template changed by Apply"
	templateResult, err = templateApplication.ExecuteAIDocumentMutation(
		ctx, template.Msg.Id, "en", AIDocumentExecutionApply,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID := uuid.MustParse(state.ViewerMemberID)
			return AIDocumentMutation{
				TemplateID: state.TemplateID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
				SetSubject: true, Subject: templateApplySubject,
				Batch: &contentblock.Batch{
					DocumentID: state.DocumentID, ExpectedRevision: uuid.MustParse(state.DocumentRevision),
					ContributorMemberIDs: []uuid.UUID{memberID},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, templateResult.Changed)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailTemplate.Edit, template.Msg.Id,
	)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_template", template.Msg.Id, "en", 1)

	templateResult, err = templateApplication.ExecuteAIDocumentMutation(
		ctx, template.Msg.Id, "en", AIDocumentExecutionApply,
		func(state AIDocumentState) (AIDocumentMutation, error) {
			memberID := uuid.MustParse(state.ViewerMemberID)
			return AIDocumentMutation{
				TemplateID: state.TemplateID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence: state.LocaleExists, ContributorMember: memberID,
				SetSubject: true, Subject: templateApplySubject,
				Batch: &contentblock.Batch{
					DocumentID: state.DocumentID, ExpectedRevision: uuid.MustParse(state.DocumentRevision),
					ContributorMemberIDs: []uuid.UUID{memberID},
				},
			}, nil
		},
	)
	require.NoError(t, err)
	require.False(t, templateResult.Changed)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailTemplate.Edit, template.Msg.Id,
	)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_template", template.Msg.Id, "en", 1)

	layoutOwner := NewAuditedEmailLayoutService(
		db, "", "", writer, allowedSpiceDB,
		WithEmailLayoutCampaignDeliveryReferences(references),
		WithEmailLayoutContentBlockStore(store),
	)
	layoutApplication, err := NewEmailLayoutAIDocumentService(layoutOwner)
	require.NoError(t, err)
	compilerFailure = errors.New("stop after authorized Email Layout compiler")
	compilerCalls = 0
	_, err = layoutApplication.ExecuteAIDocumentMutation(
		ctx, layout.Msg.Id, "en", EmailLayoutAIDocumentExecutionValidate,
		func(state EmailLayoutAIDocumentState) (EmailLayoutAIDocumentMutation, error) {
			compilerCalls++
			require.Equal(t, layout.Msg.Id, state.LayoutID)
			return EmailLayoutAIDocumentMutation{}, compilerFailure
		},
	)
	require.ErrorIs(t, err, compilerFailure)
	require.Equal(t, 1, compilerCalls)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailLayout.Edit, layout.Msg.Id,
	)

	var layoutHTMLBefore string
	require.NoError(t, db.Table("email_layout_translation").
		Select("html_content").
		Where("entity_id = ? AND locale = 'en'", layout.Msg.Id).
		Row().Scan(&layoutHTMLBefore))
	layoutResult, err := layoutApplication.ExecuteAIDocumentMutation(
		ctx, layout.Msg.Id, "en", EmailLayoutAIDocumentExecutionValidate,
		func(state EmailLayoutAIDocumentState) (EmailLayoutAIDocumentMutation, error) {
			require.NotEmpty(t, state.Units)
			values := emailLayoutLocaleValues(state)
			values[state.Units[0].Handle] = "Layout changed only inside Validate"
			return EmailLayoutAIDocumentMutation{
				LayoutID: state.LayoutID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedTargetRevision:   state.TargetRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence:    state.LocaleExists,
				ContributorMemberID: uuid.MustParse(state.ViewerMemberID),
				Values:              values, ReplaceValues: true,
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, layoutResult.Changed)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailLayout.Edit, layout.Msg.Id,
	)
	var layoutHTMLAfter string
	require.NoError(t, db.Table("email_layout_translation").
		Select("html_content").
		Where("entity_id = ? AND locale = 'en'", layout.Msg.Id).
		Row().Scan(&layoutHTMLAfter))
	require.Equal(t, layoutHTMLBefore, layoutHTMLAfter)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_layout", layout.Msg.Id, "en", 0)

	layoutApplyValue := "Layout changed by Apply"
	layoutResult, err = layoutApplication.ExecuteAIDocumentMutation(
		ctx, layout.Msg.Id, "en", EmailLayoutAIDocumentExecutionApply,
		func(state EmailLayoutAIDocumentState) (EmailLayoutAIDocumentMutation, error) {
			values := emailLayoutLocaleValues(state)
			values[state.Units[0].Handle] = layoutApplyValue
			return EmailLayoutAIDocumentMutation{
				LayoutID: state.LayoutID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedTargetRevision:   state.TargetRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence:    state.LocaleExists,
				ContributorMemberID: uuid.MustParse(state.ViewerMemberID),
				Values:              values, ReplaceValues: true,
			}, nil
		},
	)
	require.NoError(t, err)
	require.True(t, layoutResult.Changed)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailLayout.Edit, layout.Msg.Id,
	)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_layout", layout.Msg.Id, "en", 1)

	layoutResult, err = layoutApplication.ExecuteAIDocumentMutation(
		ctx, layout.Msg.Id, "en", EmailLayoutAIDocumentExecutionApply,
		func(state EmailLayoutAIDocumentState) (EmailLayoutAIDocumentMutation, error) {
			values := emailLayoutLocaleValues(state)
			values[state.Units[0].Handle] = layoutApplyValue
			return EmailLayoutAIDocumentMutation{
				LayoutID: state.LayoutID, Locale: state.Locale,
				ExpectedDocumentRevision: state.DocumentRevision,
				ExpectedTargetRevision:   state.TargetRevision, ExpectedSource: state.SourceLocale,
				ExpectedPresence:    state.LocaleExists,
				ContributorMemberID: uuid.MustParse(state.ViewerMemberID),
				Values:              values, ReplaceValues: true,
			}, nil
		},
	)
	require.NoError(t, err)
	require.False(t, layoutResult.Changed)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, allowedRecorder.take(), policyv1.EmailLayout.Edit, layout.Msg.Id,
	)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_layout", layout.Msg.Id, "en", 1)

	deniedSpiceDB, deniedRecorder := newEmailAuthoringAIDocumentPermissionServer(t, false)
	deniedTemplateApplication, err := NewAIDocumentService(NewAuditedInternalEmailTemplateService(
		db, writer, deniedSpiceDB,
		WithInternalEmailTemplateContentBlockStore(store),
		WithInternalEmailTemplateCheckpoints(testcollaboration.NewCheckpoints(db, realSpiceDB)),
		WithInternalEmailTemplateCampaignDeliveryReferences(references),
	))
	require.NoError(t, err)
	compilerCalls = 0
	_, err = deniedTemplateApplication.ExecuteAIDocumentMutation(
		ctx, template.Msg.Id, "en", AIDocumentExecutionApply,
		func(AIDocumentState) (AIDocumentMutation, error) {
			compilerCalls++
			return AIDocumentMutation{}, nil
		},
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Zero(t, compilerCalls)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, deniedRecorder.take(), policyv1.EmailTemplate.Edit, template.Msg.Id,
	)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_template", template.Msg.Id, "en", 1)
	deniedLayoutApplication, err := NewEmailLayoutAIDocumentService(NewAuditedEmailLayoutService(
		db, "", "", writer, deniedSpiceDB,
		WithEmailLayoutCampaignDeliveryReferences(references),
		WithEmailLayoutContentBlockStore(store),
	))
	require.NoError(t, err)
	compilerCalls = 0
	_, err = deniedLayoutApplication.ExecuteAIDocumentMutation(
		ctx, layout.Msg.Id, "en", EmailLayoutAIDocumentExecutionApply,
		func(EmailLayoutAIDocumentState) (EmailLayoutAIDocumentMutation, error) {
			compilerCalls++
			return EmailLayoutAIDocumentMutation{}, nil
		},
	)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Zero(t, compilerCalls)
	requireEmailAuthoringAIDocumentPermissionRequest(
		t, deniedRecorder.take(), policyv1.EmailLayout.Edit, layout.Msg.Id,
	)
	requireEmailAuthoringAIDocumentLocaleAudits(t, db, "email_layout", layout.Msg.Id, "en", 1)
}

func requireEmailAuthoringAIDocumentLocaleAudits(
	t *testing.T,
	db *gorm.DB,
	targetType string,
	targetID string,
	locale string,
	want int,
) {
	t.Helper()
	var rows []struct{ Attributes []byte }
	require.NoError(t, db.Raw(`
		SELECT attributes
		FROM public.domain_audit
		WHERE target_type = ? AND target_id = ?
		  AND attributes @> '{"changed_fields":["locale_content"]}'::jsonb
		ORDER BY occurred_at, audit_id
	`, targetType, targetID).Scan(&rows).Error)
	require.Len(t, rows, want)
	for _, row := range rows {
		var attributes map[string]any
		require.NoError(t, json.Unmarshal(row.Attributes, &attributes))
		require.Equal(t, locale, attributes["locale"])
		require.Equal(t, "updated", attributes["item_operation"])
		require.NotContains(t, attributes, "source_revision")
	}
}

type emailAuthoringAIDocumentPermissionServer struct {
	authzedv1.UnimplementedPermissionsServiceServer

	mu       sync.Mutex
	allowed  bool
	requests []*authzedv1.CheckPermissionRequest
}

func (s *emailAuthoringAIDocumentPermissionServer) CheckPermission(
	_ context.Context,
	request *authzedv1.CheckPermissionRequest,
) (*authzedv1.CheckPermissionResponse, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	s.mu.Unlock()
	permissionship := authzedv1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION
	if s.allowed {
		permissionship = authzedv1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION
	}
	return &authzedv1.CheckPermissionResponse{Permissionship: permissionship}, nil
}

func (s *emailAuthoringAIDocumentPermissionServer) take() []*authzedv1.CheckPermissionRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := append([]*authzedv1.CheckPermissionRequest(nil), s.requests...)
	s.requests = nil
	return requests
}

func newEmailAuthoringAIDocumentPermissionServer(
	t *testing.T,
	allowed bool,
) (*auth.SpiceDBClient, *emailAuthoringAIDocumentPermissionServer) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	recorder := &emailAuthoringAIDocumentPermissionServer{allowed: allowed}
	authzedv1.RegisterPermissionsServiceServer(server, recorder)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	client, err := auth.NewSpiceDBClient(listener.Addr().String(), "integration-test-token", true)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
		server.GracefulStop()
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			require.NoError(t, closeErr)
		}
		serveErr := <-serveErrors
		if !errors.Is(serveErr, grpc.ErrServerStopped) {
			require.NoError(t, serveErr)
		}
	})
	return client, recorder
}

func requireEmailAuthoringAIDocumentPermissionRequest(
	t *testing.T,
	requests []*authzedv1.CheckPermissionRequest,
	wantAction func(string) (policyv1.Can, error),
	resourceID string,
) {
	t.Helper()
	want, err := wantAction(resourceID)
	require.NoError(t, err)
	require.Len(t, requests, 1, "one logical mutation must make one exact authorization decision")
	require.Equal(t, want.Action().Permission(), requests[0].GetPermission())
	require.Equal(t, want.Resource().Type(), requests[0].GetResource().GetObjectType())
	require.Equal(t, want.Resource().ID(), requests[0].GetResource().GetObjectId())
}
