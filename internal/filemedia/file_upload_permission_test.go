//go:build integration

package filemedia

import (
	"context"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestResolveUploadPermissionTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		uploadType        managev1.UploadType
		entityType        string
		wantResource      string
		wantSpiceDB       bool
		wantAdminOnly     bool
		wantAuthorOnly    bool
		wantUserOwned     bool
		wantValidationErr bool
	}{
		{
			name:         "post featured image",
			uploadType:   managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
			wantResource: "post",
			wantSpiceDB:  true,
		},
		{
			name:          "page featured image override",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE,
			entityType:    managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE.String(),
			wantResource:  "page",
			wantSpiceDB:   true,
			wantAdminOnly: true,
		},
		{
			name:           "independent editor audio",
			uploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
			wantAuthorOnly: true,
		},
		{
			name:          "work featured image admin only",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_WORK_FEATURED_IMAGE,
			wantResource:  "work",
			wantSpiceDB:   true,
			wantAdminOnly: true,
		},
		{
			name:          "form featured image admin only",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_FORM_FEATURED_IMAGE,
			wantResource:  "form",
			wantSpiceDB:   true,
			wantAdminOnly: true,
		},
		{
			name:          "program event poster shared by event and series is admin only",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER,
			wantAdminOnly: true,
		},
		{
			name:           "independent editor attachment",
			uploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT,
			wantAuthorOnly: true,
		},
		{
			name:           "independent editor mesh",
			uploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH,
			wantAuthorOnly: true,
		},
		{
			name:          "site logo admin only",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
			wantAdminOnly: true,
		},
		{
			name:          "client logo admin only",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_CLIENT_LOGO,
			wantAdminOnly: true,
		},
		{
			name:          "user avatar ownership",
			uploadType:    managev1.UploadType_UPLOAD_TYPE_USER_AVATAR,
			wantResource:  "user",
			wantUserOwned: true,
		},
		{
			name:           "independent editor image",
			uploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			wantAuthorOnly: true,
		},
		{
			name:              "editor unsupported entity type",
			uploadType:        managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO,
			entityType:        "TRANSCODE_ENTITY_TYPE_NOT_REAL",
			wantValidationErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveUploadPermissionTarget(tt.uploadType, tt.entityType)
			if tt.wantValidationErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantResource, got.resourceType)
			require.Equal(t, tt.wantSpiceDB, got.requiresSpiceDBCheck)
			require.Equal(t, tt.wantAdminOnly, got.adminOnly)
			require.Equal(t, tt.wantAuthorOnly, got.authorOnly)
			require.Equal(t, tt.wantUserOwned, got.userOwned)
		})
	}
}

func TestCheckEntityPermissionAllowsAdminProgramEventPosterForSeriesID(t *testing.T) {
	t.Parallel()

	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc := &FileService{spiceDB: stack.SpiceDBClient}
	ctx := auth.WithUser(context.Background(), admin.AuthUserInfo())
	target, err := resolveUploadPermissionTarget(managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER, "")
	require.NoError(t, err)

	require.NoError(t, svc.checkRoleAndSpiceDBUploadPermission(ctx, target, "program-event-series-id", admin.MemberID))
}

func TestCheckEntityPermissionRejectsNonAdminProgramEventPoster(t *testing.T) {
	t.Parallel()

	stack := testutil.SetupOryStack(t)
	user := stack.CreateUser(t, policyv1.Role.User().ID())
	svc := &FileService{spiceDB: stack.SpiceDBClient}
	ctx := auth.WithUser(context.Background(), user.AuthUserInfo())
	target, err := resolveUploadPermissionTarget(managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER, "")
	require.NoError(t, err)

	err = svc.checkRoleAndSpiceDBUploadPermission(ctx, target, "program-event-series-id", user.MemberID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "admin")
}

func TestCheckPartUploadPermissionAllowsAdminProgramEventPosterForSeriesID(t *testing.T) {
	t.Parallel()

	stack := testutil.SetupOryStack(t)
	admin := stack.CreateUser(t, policyv1.Role.Admin().ID())
	svc := &FileService{spiceDB: stack.SpiceDBClient}
	target, err := resolveUploadPermissionTarget(managev1.UploadType_UPLOAD_TYPE_PROGRAM_EVENT_POSTER, "")
	require.NoError(t, err)
	require.NoError(t, svc.checkRoleAndSpiceDBUploadPermission(auth.WithUser(context.Background(), admin.AuthUserInfo()), target, "program-event-series-id", admin.MemberID))
}

func TestCheckEntityPermissionAllowsIndependentEditorMesh(t *testing.T) {
	t.Parallel()

	svc := &FileService{}
	target, entityID, err := svc.resolvePartUploadPermissionTarget(context.Background(), managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH, model.UploadSession{})
	require.NoError(t, err)
	require.True(t, target.authorOnly)
	require.Empty(t, entityID)
}

func TestCheckPartUploadPermissionRejectsDocumentTargetForEditorMesh(t *testing.T) {
	t.Parallel()

	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE.String()
	svc := &FileService{}

	_, _, err := svc.resolvePartUploadPermissionTarget(context.Background(), managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH, model.UploadSession{
		UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH.String(),
		EntityType: &entityType,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "omit document entity type")
}
