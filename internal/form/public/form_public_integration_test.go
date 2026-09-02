//go:build integration

package public

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	formadapter "github.com/echovisionlab/geul-api/internal/adapters/form"
	sharelinkadapter "github.com/echovisionlab/geul-api/internal/adapters/sharelink"
	"github.com/echovisionlab/geul-api/internal/crypto"
	formdomain "github.com/echovisionlab/geul-api/internal/form"
	"github.com/echovisionlab/geul-api/internal/model"
	sharelinkdomain "github.com/echovisionlab/geul-api/internal/sharelink"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	openv1 "github.com/echovisionlab/geul-event-contracts/gen/api/open/v1"
)

func TestPublicFormServiceAccessPasswordSubmitAndLimitIntegration(t *testing.T) {
	db := testutil.NewIntegrationDB(t)
	adminID := uuid.NewString()
	testutil.SeedKratosIdentityFixture(t, db, testutil.KratosIdentityFixture{ID: adminID, Name: "Public Form Admin"})
	adminMemberID := seedFormPublicAdminMemberIdentityLink(t, db, adminID, "Public Form Admin")

	form := createPublishedPublicFormForIntegration(t, db, adminMemberID, adminID, publicFormFixtureInput{
		Title:                    "Public Password Form",
		Password:                 "correct-password",
		MaxSubmissions:           publicInt32Ptr(1),
		AllowDuplicateSubmission: boolPtr(true),
	})
	featuredImageFileID, _ := seedCanonicalPublicFileFixture(t, db, "featured.webp", "image/webp", "image")
	_, err := newPublicManageFormService(t, db, adminID).SetFormFeaturedImage(
		formPublicPrincipalContext(adminMemberID, adminID),
		connect.NewRequest(&managev1.SetFormFeaturedImageRequest{
			FormId: form.id,
			FileId: featuredImageFileID,
		}),
	)
	require.NoError(t, err)
	ogAssetID, ogAssetURL := seedBoundOgAssetFixture(t, db, "form", form.id, "og")
	require.NoError(t, db.Model(&model.Form{}).Where("id = ?", form.id).Update("og_asset_id", ogAssetID).Error)
	publicSvc := newPublicFormServiceForIntegration(t, db)

	missingPasswordAccess, err := publicSvc.CheckAccess(context.Background(), connect.NewRequest(&openv1.CheckFormAccessRequest{
		Slug: form.slug,
	}))
	require.NoError(t, err)
	require.False(t, missingPasswordAccess.Msg.Accessible)
	require.Equal(t, openv1.FormAccessReason_FORM_ACCESS_REASON_PASSWORD_REQUIRED, missingPasswordAccess.Msg.Reason)
	require.NotNil(t, missingPasswordAccess.Msg.Form)
	require.True(t, missingPasswordAccess.Msg.Form.GetHasPassword())
	require.Empty(t, missingPasswordAccess.Msg.Form.GetSchema())
	require.Equal(t, ogAssetURL, missingPasswordAccess.Msg.Form.GetOgAsset().GetUrl())
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", featuredImageFileID), missingPasswordAccess.Msg.Form.GetFeaturedImageAsset().GetUrl())

	wrongPassword, err := publicSvc.VerifyPassword(context.Background(), connect.NewRequest(&openv1.VerifyFormPasswordRequest{
		Slug:     form.slug,
		Password: "wrong-password",
	}))
	require.NoError(t, err)
	require.False(t, wrongPassword.Msg.Valid)

	correctPassword, err := publicSvc.VerifyPassword(context.Background(), connect.NewRequest(&openv1.VerifyFormPasswordRequest{
		Slug:     form.slug,
		Password: "correct-password",
	}))
	require.NoError(t, err)
	require.True(t, correctPassword.Msg.Valid)

	loadedByID, err := publicSvc.CheckAccess(context.Background(), connect.NewRequest(&openv1.CheckFormAccessRequest{
		Slug:     form.id,
		Password: stringPtr("correct-password"),
	}))
	require.NoError(t, err)
	require.True(t, loadedByID.Msg.Accessible)
	require.Equal(t, form.id, loadedByID.Msg.Form.GetId())
	require.Equal(t, "Public Password Form", loadedByID.Msg.Form.GetTitle())
	require.Equal(t, readyPublicAssetURLForFileFixture(t, db, "https://cdn.example.com", featuredImageFileID), loadedByID.Msg.Form.GetFeaturedImageAsset().GetUrl())
	require.Equal(t, ogAssetID, loadedByID.Msg.Form.GetOgAsset().GetAssetId())
	require.JSONEq(t, string(publicFormSchemaForIntegration()), string(loadedByID.Msg.Form.GetSchema()))

	_, err = publicSvc.Submit(context.Background(), connect.NewRequest(&openv1.SubmitFormRequest{
		FormId:   form.id,
		Password: stringPtr("wrong-password"),
		Data:     []byte(`{"email":"blocked@example.com"}`),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	_, err = publicSvc.Submit(context.Background(), connect.NewRequest(&openv1.SubmitFormRequest{
		FormId:   form.id,
		Password: stringPtr("correct-password"),
		Data:     []byte(`{"unknown":"not-in-current-schema"}`),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	var rejectedSubmissionCount int64
	require.NoError(t, db.Model(&model.FormSubmission{}).Where("form_id = ?", form.id).Count(&rejectedSubmissionCount).Error)
	require.Zero(t, rejectedSubmissionCount)

	submitted, err := publicSvc.Submit(context.Background(), connect.NewRequest(&openv1.SubmitFormRequest{
		FormId:   form.id,
		Password: stringPtr("correct-password"),
		Data:     []byte(`{"email":"hello@example.com"}`),
	}))
	require.NoError(t, err)
	require.NotEmpty(t, submitted.Msg.SubmissionId)

	dashboardLink, err := sharelinkdomain.NewService(db, sharelinkadapter.NewAuthority(db, testutil.IntegrationSpiceDB(t), nil)).CreateShareLink(
		formPublicPrincipalContext(adminMemberID, adminID),
		connect.NewRequest(&managev1.CreateShareLinkRequest{
			EntityType: managev1.ShareLinkEntityType_SHARE_LINK_ENTITY_TYPE_FORM_DASHBOARD,
			EntityId:   form.id,
			Password:   stringPtr("dashboard-password"),
		}),
	)
	require.NoError(t, err)
	missingDashboardProof, err := publicSvc.CheckAccess(context.Background(), connect.NewRequest(&openv1.CheckFormAccessRequest{
		Slug:       form.slug,
		ShareToken: &dashboardLink.Msg.ShareLink.Token,
		Target:     openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD,
	}))
	require.NoError(t, err)
	require.False(t, missingDashboardProof.Msg.Accessible)
	require.Equal(t, openv1.FormAccessReason_FORM_ACCESS_REASON_FORM_NOT_FOUND, missingDashboardProof.Msg.Reason)

	dashboardAccess, err := publicSvc.CheckAccess(context.Background(), connect.NewRequest(&openv1.CheckFormAccessRequest{
		Slug:          form.slug,
		ShareToken:    &dashboardLink.Msg.ShareLink.Token,
		SharePassword: stringPtr("dashboard-password"),
		Target:        openv1.FormAccessTarget_FORM_ACCESS_TARGET_DASHBOARD,
	}))
	require.NoError(t, err)
	require.True(t, dashboardAccess.Msg.Accessible)
	dashboard, err := publicSvc.GetDashboard(context.Background(), connect.NewRequest(&openv1.GetFormDashboardRequest{
		Slug:          form.slug,
		ShareToken:    dashboardLink.Msg.ShareLink.Token,
		SharePassword: stringPtr("dashboard-password"),
	}))
	require.NoError(t, err)
	require.EqualValues(t, 1, dashboard.Msg.Dashboard.GetTotalSubmissions())

	limitAccess, err := publicSvc.CheckAccess(context.Background(), connect.NewRequest(&openv1.CheckFormAccessRequest{
		Slug:     form.slug,
		Password: stringPtr("correct-password"),
	}))
	require.NoError(t, err)
	require.False(t, limitAccess.Msg.Accessible)
	require.Equal(t, openv1.FormAccessReason_FORM_ACCESS_REASON_MAX_SUBMISSIONS_REACHED, limitAccess.Msg.Reason)

	_, err = publicSvc.Submit(context.Background(), connect.NewRequest(&openv1.SubmitFormRequest{
		FormId:   form.id,
		Password: stringPtr("correct-password"),
		Data:     []byte(`{"email":"late@example.com"}`),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

type publicFormFixtureInput struct {
	Title                    string
	IsPublic                 *bool
	RequireAuth              *bool
	AllowedRoles             []string
	AllowDuplicateSubmission *bool
	Password                 string
	MaxSubmissions           *int32
	OpensAt                  *time.Time
	ClosesAt                 *time.Time
}

type publicFormFixture struct {
	id   string
	slug string
}

func createPublishedPublicFormForIntegration(
	t *testing.T,
	db *gorm.DB,
	adminMemberID string,
	adminID string,
	input publicFormFixtureInput,
) publicFormFixture {
	t.Helper()
	form := createManagedFormForIntegration(t, db, adminMemberID, adminID, input)
	status := managev1.FormStatus_FORM_STATUS_PUBLISHED
	_, err := newPublicManageFormService(t, db, adminID).UpdateForm(
		formPublicPrincipalContext(adminMemberID, adminID),
		connect.NewRequest(&managev1.UpdateFormRequest{
			Id:     form.id,
			Status: &status,
		}),
	)
	require.NoError(t, err)
	return form
}

func createManagedFormForIntegration(
	t *testing.T,
	db *gorm.DB,
	adminMemberID string,
	adminID string,
	input publicFormFixtureInput,
) publicFormFixture {
	t.Helper()
	if input.Title == "" {
		input.Title = "Public Form"
	}
	if input.IsPublic == nil {
		input.IsPublic = boolPtr(true)
	}
	if input.AllowDuplicateSubmission == nil {
		input.AllowDuplicateSubmission = boolPtr(true)
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	slug := "public-form-" + suffix
	createReq := &managev1.CreateFormRequest{
		Title:                    input.Title,
		Slug:                     &slug,
		Schema:                   publicFormSchemaForIntegration(),
		IsPublic:                 input.IsPublic,
		RequireAuth:              input.RequireAuth,
		AllowedRoles:             input.AllowedRoles,
		AllowDuplicateSubmission: input.AllowDuplicateSubmission,
		MaxSubmissions:           input.MaxSubmissions,
	}
	if input.OpensAt != nil {
		createReq.OpensAt = timestamppb.New(*input.OpensAt)
	}
	if input.ClosesAt != nil {
		createReq.ClosesAt = timestamppb.New(*input.ClosesAt)
	}
	if input.Password != "" {
		createReq.Password = &input.Password
	}

	created, err := newPublicManageFormService(t, db, adminID).CreateForm(
		formPublicPrincipalContext(adminMemberID, adminID),
		connect.NewRequest(createReq),
	)
	require.NoError(t, err)
	return publicFormFixture{id: created.Msg.Id, slug: slug}
}

// newPublicFormServiceForIntegration composes the migrated Form public
// boundary for this shared cross-domain integration suite.
func newPublicFormServiceForIntegration(t *testing.T, db *gorm.DB) *FormService {
	t.Helper()
	assets := formadapter.NewAssets("https://cdn.example.com")
	return NewFormService(db, crypto.NewPasswordHasher(&crypto.Argon2idParams{
		Memory:      64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  8,
		KeyLength:   16,
	}), testutil.IntegrationSpiceDB(t), formdomain.Dependencies{PublicAssets: assets})
}

func publicFormSchemaForIntegration() []byte {
	return []byte(`{"id":"public-form-schema","steps":[{"id":"step-1","title":"Contact","fields":[{"id":"field-email","key":"email","label":"Email address","type":"email"},{"id":"field-consent","key":"consent","label":"Consent","type":"checkbox"}]}]}`)
}
