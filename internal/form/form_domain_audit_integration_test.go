//go:build integration

package form_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/crypto"
	"github.com/echovisionlab/geul-api/internal/model"
	sharelinkdomain "github.com/echovisionlab/geul-api/internal/sharelink"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestFormDomainAuditRootSettingsLifecycleAssetAndDeleteIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminCtx, spiceDB := testutil.IntegrationAdminContext(t, db)
	user := auth.GetUser(adminCtx)
	require.NotNil(t, user)
	identityID, memberID := user.IdentityID.String(), user.MemberID.String()
	ctx := testutil.NewAuditContext(t, identityID, memberID)
	writer := apitelemetry.NewDurableWriter(db)
	service := newAuditedFormServiceForIntegration(t, db, identityID, writer, writer, spiceDB)

	created, err := service.CreateForm(ctx, connect.NewRequest(&managev1.CreateFormRequest{Title: "Audited Form", Schema: integrationFormSchema()}))
	require.NoError(t, err)
	formID := created.Msg.Id
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.Form.Manage, formID, identityID, true)
	slug, password := "audited-form", "audit-password"
	public := true
	_, err = service.UpdateForm(ctx, connect.NewRequest(&managev1.UpdateFormRequest{Id: formID, Slug: &slug, IsPublic: &public, Password: &password}))
	require.NoError(t, err)
	published := managev1.FormStatus_FORM_STATUS_PUBLISHED
	_, err = service.UpdateForm(ctx, connect.NewRequest(&managev1.UpdateFormRequest{Id: formID, Status: &published}))
	require.NoError(t, err)
	// Repeating semantic values must not add a Domain Audit row.
	_, err = service.UpdateForm(ctx, connect.NewRequest(&managev1.UpdateFormRequest{Id: formID, Status: &published, Password: &password}))
	require.NoError(t, err)

	fileID := seedFormImageBindingFile(t, db, "form/"+formID+"/audit.webp")
	_, err = service.SetFormFeaturedImage(ctx, connect.NewRequest(&managev1.SetFormFeaturedImageRequest{FormId: formID, FileId: fileID}))
	require.NoError(t, err)
	_, err = service.DeleteFormFeaturedImage(ctx, connect.NewRequest(&managev1.DeleteFormFeaturedImageRequest{FormId: formID}))
	require.NoError(t, err)

	links := sharelinkdomain.NewService(db, sharelinkadapter.NewAuthority(db, spiceDB, writer))
	createdLink, err := links.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM, EntityId: formID}))
	require.NoError(t, err)
	_, err = links.DeleteShareLink(ctx, connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: createdLink.Msg.ShareLink.Id}))
	require.NoError(t, err)
	dashboardLink, err := links.CreateShareLink(ctx, connect.NewRequest(&managev1.CreateShareLinkRequest{EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD, EntityId: formID}))
	require.NoError(t, err)
	_, err = links.DeleteShareLink(ctx, connect.NewRequest(&managev1.DeleteShareLinkRequest{Id: dashboardLink.Msg.ShareLink.Id}))
	require.NoError(t, err)

	submission := &model.FormSubmission{ID: integrationTestUUID(), FormID: formID, Data: []byte(`{"private_answer":"must not audit"}`)}
	require.NoError(t, db.Create(submission).Error)
	_, err = service.DeleteFormSubmission(ctx, connect.NewRequest(&managev1.DeleteFormSubmissionRequest{Id: submission.ID}))
	require.NoError(t, err)
	// A missing target is not a mutation and must not append a second record.
	_, err = service.DeleteFormSubmission(ctx, connect.NewRequest(&managev1.DeleteFormSubmissionRequest{Id: submission.ID}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	var submissionAuditRows []formAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_type = 'form_submission' AND target_id = ?`, submission.ID).Scan(&submissionAuditRows).Error)
	require.Len(t, submissionAuditRows, 1)
	require.Equal(t, "form_submission.deleted", submissionAuditRows[0].Action)
	require.Equal(t, memberID, submissionAuditRows[0].ActorMemberID)
	require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), submissionAuditRows[0].RequestID)
	require.JSONEq(t, `{}`, string(submissionAuditRows[0].Attributes))

	rollbackSubmission := &model.FormSubmission{ID: integrationTestUUID(), FormID: formID, Data: []byte(`{"email":"private@example.test"}`)}
	require.NoError(t, db.Create(rollbackSubmission).Error)
	failingDelete := newAuditedFormServiceForIntegration(t, db, identityID, writer, failingFormAuditAppender{}, spiceDB)
	_, err = failingDelete.DeleteFormSubmission(ctx, connect.NewRequest(&managev1.DeleteFormSubmissionRequest{Id: rollbackSubmission.ID}))
	require.Error(t, err)
	var rollbackCount int64
	require.NoError(t, db.Model(&model.FormSubmission{}).Where("id = ?", rollbackSubmission.ID).Count(&rollbackCount).Error)
	require.EqualValues(t, 1, rollbackCount)
	_, err = service.DeleteForm(ctx, connect.NewRequest(&managev1.DeleteFormRequest{Id: formID}))
	require.NoError(t, err)
	testutil.RequireSynchronousResourceAuthorization(t, spiceDB, policyv1.Form.Manage, formID, identityID, false)

	var rows []formAuditRow
	require.NoError(t, db.Raw(`SELECT action, target_type, target_id, actor_member_id::text AS actor_member_id, request_id::text AS request_id, attributes FROM domain_audit WHERE target_type = 'form' AND target_id = ? ORDER BY occurred_at, audit_id`, formID).Scan(&rows).Error)
	require.Len(t, rows, 10)
	require.Equal(t, []string{"form.created", "form.updated", "form.updated", "form.updated", "form.updated", "form.updated", "form.updated", "form.updated", "form.updated", "form.deleted"}, []string{rows[0].Action, rows[1].Action, rows[2].Action, rows[3].Action, rows[4].Action, rows[5].Action, rows[6].Action, rows[7].Action, rows[8].Action, rows[9].Action})
	for _, row := range rows {
		require.Equal(t, memberID, row.ActorMemberID)
		require.Equal(t, sharedtelemetry.RequestIDFromContext(ctx), row.RequestID)
		require.NotContains(t, string(row.Attributes), password)
		require.NotContains(t, string(row.Attributes), "access_password")
	}
	require.Contains(t, string(rows[1].Attributes), `"direct_public"`)
	require.Contains(t, string(rows[1].Attributes), `"slug"`)
	require.Contains(t, string(rows[2].Attributes), `"status"`)
	require.Contains(t, string(rows[3].Attributes), `"featured_image"`)
	require.Contains(t, string(rows[5].Attributes), `"share_links"`)
	require.Contains(t, string(rows[7].Attributes), `"item_scope"`)
	require.Contains(t, string(rows[7].Attributes), `"dashboard"`)

	failing := newAuditedFormServiceForIntegration(t, db, identityID, writer, failingFormAuditAppender{}, spiceDB)
	var beforeCount int64
	require.NoError(t, db.Model(&model.Form{}).Count(&beforeCount).Error)
	_, err = failing.CreateForm(ctx, connect.NewRequest(&managev1.CreateFormRequest{Title: "must rollback", Schema: integrationFormSchema()}))
	require.Error(t, err)
	var count int64
	require.NoError(t, db.Model(&model.Form{}).Count(&count).Error)
	require.Equal(t, beforeCount, count)
}

func integrationFormAuditHasher() *crypto.PasswordHasher {
	return crypto.NewPasswordHasher(&crypto.Argon2idParams{Memory: 64, Iterations: 1, Parallelism: 1, SaltLength: 8, KeyLength: 16})
}
