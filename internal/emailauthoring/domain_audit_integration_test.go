//go:build integration

package emailauthoring

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testcollaboration"
	"github.com/echovisionlab/geul-api/internal/testutil"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type emailAuthoringAuditRow struct {
	Action        string
	TargetType    string `gorm:"column:target_type"`
	ActorKind     string `gorm:"column:actor_kind"`
	ActorService  string `gorm:"column:actor_service"`
	ActorMemberID string `gorm:"column:actor_member_id"`
	RequestID     string `gorm:"column:request_id"`
	Attributes    []byte
}

type emailAuthoringFailingAppender struct{}

func (emailAuthoringFailingAppender) AppendDomainAuditInTransaction(context.Context, *gorm.DB, sharedtelemetry.AuditRecord) error {
	return errors.New("email authoring audit unavailable")
}

func TestEmailAuthoringDomainAuditIntegration(t *testing.T) {
	t.Parallel()
	db := newEmailAuthoringLocalKratosIntegrationDB(t)
	baseContext, spiceDB := testutil.IntegrationAdminAuditContext(t, db)
	admin := auth.GetUser(baseContext)
	require.NotNil(t, admin)
	identityID := string(admin.IdentityID)
	memberID := string(admin.MemberID)
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	writer := apitelemetry.NewDurableWriter(db)
	references := integrationCampaignDeliveryReferences{}
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	layouts := NewLayoutConnectHandler(newTestService(t, Deps{DB: db, Audit: writer, SpiceDB: spiceDB, References: references, ContentBlocks: store}))
	templates := NewTemplateConnectHandler(newTestService(t, Deps{DB: db, Runtime: emailTemplateRuntimeFixture{}, Audit: writer, SpiceDB: spiceDB, ContentBlocks: store, References: references}))

	layout, err := layouts.CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Key: "email_audit_layout", Name: "Email audit layout",
		HtmlContent: "<main>{{content}}</main>", SourceLocale: "en",
	}))
	require.NoError(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.EmailLayout.Manage, layout.Msg.Id, identityID, true)
	template, err := templates.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{Key: "email_audit_template", Name: "Email audit template", Subject: "Initial subject", SourceLocale: "en"}))
	require.NoError(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.EmailTemplate.Manage, template.Msg.Id, identityID, true)
	description := "must never be copied into audit attributes"
	_, err = templates.UpdateEmailTemplate(ctx, connect.NewRequest(&managev1.UpdateEmailTemplateRequest{Id: template.Msg.Id, Description: &description, LayoutId: &layout.Msg.Id}))
	require.NoError(t, err)
	_, err = templates.UpdateEmailTemplate(ctx, connect.NewRequest(&managev1.UpdateEmailTemplateRequest{Id: template.Msg.Id, Description: &description, LayoutId: &layout.Msg.Id}))
	require.NoError(t, err)
	_, err = templates.UpdateEventMapping(ctx, connect.NewRequest(&managev1.UpdateEventMappingRequest{Event: "welcome", TemplateId: &template.Msg.Id}))
	require.NoError(t, err)
	_, err = templates.UpdateEventMapping(ctx, connect.NewRequest(&managev1.UpdateEventMappingRequest{Event: "welcome", TemplateId: nil}))
	require.NoError(t, err)

	// Trusted collaboration saves attribute each accepted mutation to exactly
	// one origin Member. Email authoring owns no separate Version checkpoint.
	internalTemplate := NewInternalTemplateConnectHandler(newTestInternalService(t, InternalDeps{DB: db, Audit: writer, SpiceDB: spiceDB, ContentBlocks: store, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB), References: references}))
	applied, err := internalTemplate.ApplyBlockBatch(ctx, connect.NewRequest(&intrav1.ApplyEmailTemplateBlockBatchRequest{
		EmailTemplateId: template.Msg.Id,
		Locale:          template.Msg.Document.SourceLocale,
		Batch: testutil.NewParagraphBatch(
			template.Msg.Document,
			template.Msg.DocumentRevision,
			template.Msg.Document.SourceLocale,
			"source body",
			[]string{memberID},
		),
	}))
	require.NoError(t, err)
	staleSubject := "must not persist"
	_, err = internalTemplate.UpdateEmailTemplateLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateEmailTemplateLocaleMetadataRequest{
		EmailTemplateId:      template.Msg.Id,
		ExpectedRevision:     uuid.NewString(),
		Subject:              &staleSubject,
		ContributorMemberIds: []string{memberID},
		Locale:               template.Msg.Document.SourceLocale,
	}))
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	storedTemplateSubject, err := loadEmailTemplateSourceSubject(ctx, db, template.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, "Initial subject", storedTemplateSubject)

	// The trusted Collab service cannot turn attribution into authorization.
	// Revoking the contributor identity must reject the final persistence fence.
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.User())
	acceptedSubject := "Accepted trusted subject"
	_, err = internalTemplate.UpdateEmailTemplateLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateEmailTemplateLocaleMetadataRequest{
		EmailTemplateId:      template.Msg.Id,
		ExpectedRevision:     applied.Msg.DocumentRevision,
		Subject:              &acceptedSubject,
		ContributorMemberIds: []string{memberID},
		Locale:               template.Msg.Document.SourceLocale,
	}))
	require.Equal(t, connect.CodePermissionDenied, errs.CodeOf(err))
	storedTemplateSubject, err = loadEmailTemplateSourceSubject(ctx, db, template.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, "Initial subject", storedTemplateSubject)
	testutil.GrantIntegrationGlobalRole(t, spiceDB, identityID, policyv1.Role.Admin())
	metadata, err := internalTemplate.UpdateEmailTemplateLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateEmailTemplateLocaleMetadataRequest{
		EmailTemplateId:      template.Msg.Id,
		ExpectedRevision:     applied.Msg.DocumentRevision,
		Subject:              &acceptedSubject,
		ContributorMemberIds: []string{memberID},
		Locale:               template.Msg.Document.SourceLocale,
	}))
	require.NoError(t, err)
	storedTemplateSubject, err = loadEmailTemplateSourceSubject(ctx, db, template.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, acceptedSubject, storedTemplateSubject)
	rejectedSubject := "must roll back with Audit"
	_, err = NewInternalTemplateConnectHandler(newTestInternalService(t, InternalDeps{DB: db, Audit: emailAuthoringFailingAppender{}, SpiceDB: spiceDB, ContentBlocks: store, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB), References: references})).UpdateEmailTemplateLocaleMetadata(ctx, connect.NewRequest(&intrav1.UpdateEmailTemplateLocaleMetadataRequest{
		EmailTemplateId:      template.Msg.Id,
		ExpectedRevision:     metadata.Msg.DocumentRevision,
		Subject:              &rejectedSubject,
		ContributorMemberIds: []string{memberID},
		Locale:               template.Msg.Document.SourceLocale,
	}))
	require.Error(t, err)
	storedTemplateSubject, err = loadEmailTemplateSourceSubject(ctx, db, template.Msg.Id, "en")
	require.NoError(t, err)
	require.Equal(t, acceptedSubject, storedTemplateSubject)

	internalLayout := NewInternalLayoutConnectHandler(newTestInternalService(t, InternalDeps{DB: db, Audit: writer, Checkpoints: testcollaboration.NewCheckpoints(db, spiceDB), References: references, ContentBlocks: store}))
	layoutSourceLocale := layout.Msg.SourceLocale
	loadedLayout, err := internalLayout.LoadDocument(ctx, connect.NewRequest(&intrav1.LoadEmailLayoutDocumentRequest{
		EmailLayoutId: layout.Msg.Id,
		Locale:        layoutSourceLocale,
	}))
	require.NoError(t, err)
	_, err = internalLayout.SaveDocument(ctx, connect.NewRequest(&intrav1.SaveEmailLayoutDocumentRequest{
		EmailLayoutId: layout.Msg.Id, Locale: layoutSourceLocale,
		ContentHtml: "<main>source {{content}}</main>", ContentText: "source",
		ExpectedDocumentRevision: loadedLayout.Msg.DocumentRevision,
		ContributorMemberIds:     []string{memberID},
	}))
	require.NoError(t, err)

	_, err = templates.DeleteEmailTemplate(ctx, connect.NewRequest(&managev1.DeleteEmailTemplateRequest{Id: template.Msg.Id}))
	require.NoError(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.EmailTemplate.Manage, template.Msg.Id, identityID, false)
	_, err = layouts.DeleteEmailLayout(ctx, connect.NewRequest(&managev1.DeleteEmailLayoutRequest{Id: layout.Msg.Id}))
	require.NoError(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.EmailLayout.Manage, layout.Msg.Id, identityID, false)

	var rows []emailAuthoringAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, actor_kind, COALESCE(actor_service, '') AS actor_service, COALESCE(actor_member_id::text, '') AS actor_member_id, request_id::text, attributes FROM public.domain_audit WHERE target_type IN ('email_template', 'email_layout', 'email_event_mapping') ORDER BY occurred_at, audit_id`).Scan(&rows).Error)
	require.NotEmpty(t, rows)
	localeContentRows := 0
	for _, row := range rows {
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
		require.NotContains(t, string(row.Attributes), description)
		require.NotContains(t, string(row.Attributes), "source body")
		require.NotContains(t, string(row.Attributes), "source_revision")
		require.NotContains(t, string(row.Attributes), "content_hash")
		require.Equal(t, "member", row.ActorKind)
		require.Equal(t, memberID, row.ActorMemberID)
		if strings.Contains(string(row.Attributes), `"locale_content"`) {
			localeContentRows++
		}
	}
	require.Equal(t, 3, localeContentRows, "Template body, Template subject, and Layout source each own one mutation Audit")
}

func TestEmailTemplateMetadataUpdateRecordsNoLayoutChangeItDidNotMakeIntegration(t *testing.T) {
	t.Parallel()
	db := testutil.NewIntegrationDB(t)
	ctx, spiceDB := testutil.IntegrationAdminAuditContext(t, db)
	writer := apitelemetry.NewDurableWriter(db)
	references := integrationCampaignDeliveryReferences{}
	store := testutil.NewEmailContentBlockStore(t, spiceDB)
	authoring := newTestService(t, Deps{DB: db, Audit: writer, SpiceDB: spiceDB, ContentBlocks: store, References: references})
	layouts, templates := NewLayoutConnectHandler(authoring), NewTemplateConnectHandler(authoring)

	layout, err := layouts.CreateEmailLayout(ctx, connect.NewRequest(&managev1.CreateEmailLayoutRequest{
		Key: "kept_layout", Name: "Kept layout", HtmlContent: "<main>{{content}}</main>", SourceLocale: "en",
	}))
	require.NoError(t, err)
	template, err := templates.CreateEmailTemplate(ctx, connect.NewRequest(&managev1.CreateEmailTemplateRequest{
		Key: "kept_layout_template", Name: "Before rename", Subject: "Subject", SourceLocale: "en",
	}))
	require.NoError(t, err)
	_, err = templates.UpdateEmailTemplate(ctx, connect.NewRequest(&managev1.UpdateEmailTemplateRequest{Id: template.Msg.Id, LayoutId: &layout.Msg.Id}))
	require.NoError(t, err)
	name := "After rename"
	renamed, err := templates.UpdateEmailTemplate(ctx, connect.NewRequest(&managev1.UpdateEmailTemplateRequest{Id: template.Msg.Id, Name: &name}))
	require.NoError(t, err)
	require.Equal(t, layout.Msg.Id, renamed.Msg.GetLayoutId())

	var layoutChanges []struct {
		PreviousItemID string `gorm:"column:previous_item_id"`
		ItemID         string `gorm:"column:item_id"`
	}
	require.NoError(t, db.Raw(`
		SELECT COALESCE(attributes->>'previous_item_id', '') AS previous_item_id, COALESCE(attributes->>'item_id', '') AS item_id
		FROM public.domain_audit
		WHERE target_type = 'email_template' AND target_id = ? AND attributes @> '{"changed_fields":["layout"]}'::jsonb
		ORDER BY occurred_at, audit_id
	`, template.Msg.Id).Scan(&layoutChanges).Error)
	require.Len(t, layoutChanges, 1, "only the assignment changed the layout")
	require.Empty(t, layoutChanges[0].PreviousItemID)
	require.Equal(t, layout.Msg.Id, layoutChanges[0].ItemID)
}
