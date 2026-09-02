package filemedia

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestValidateMultipartCompletionVerifiedMimeRequiresDetectedMime(t *testing.T) {
	t.Parallel()

	_, err := validateMultipartCompletionVerifiedMime(
		managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
		model.UploadSession{
			UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		},
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE],
	)

	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), errs.MsgMimeNotVerified)
}

func TestValidateMultipartCompletionVerifiedMimeAppliesSiteEmailLogoPNGPolicy(t *testing.T) {
	t.Parallel()

	cfg := model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_SITE_LOGO]
	emailSlotID := siteEmailLogoSlotID
	regularSlotID := "logo"
	svgMime := "image/svg+xml"

	_, err := validateMultipartCompletionVerifiedMime(
		managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
		model.UploadSession{
			UploadType:    managev1.UploadType_UPLOAD_TYPE_SITE_LOGO.String(),
			DetectedMime:  &svgMime,
			RequestedMime: svgMime,
			SlotID:        &emailSlotID,
		},
		cfg,
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	require.Contains(t, err.Error(), "email logo uploads must be PNG")

	verifiedMime, err := validateMultipartCompletionVerifiedMime(
		managev1.UploadType_UPLOAD_TYPE_SITE_LOGO,
		model.UploadSession{
			UploadType:    managev1.UploadType_UPLOAD_TYPE_SITE_LOGO.String(),
			DetectedMime:  &svgMime,
			RequestedMime: svgMime,
			SlotID:        &regularSlotID,
		},
		cfg,
	)
	require.NoError(t, err)
	require.Equal(t, svgMime, verifiedMime)
}

func TestBuildManagedFileKeyUsesCanonicalMediaKeyForEveryLogoSlot(t *testing.T) {
	t.Parallel()

	svc := &FileService{}
	fileID := "11111111-1111-4111-8111-111111111111"

	emailKey, err := svc.buildManagedFileKey(fileID, siteEmailLogoStableMime)
	require.NoError(t, err)
	require.Equal(t, "media/"+fileID+".png", emailKey)

	regularKey, err := svc.buildManagedFileKey(fileID, "image/svg+xml")
	require.NoError(t, err)
	require.Equal(t, "media/"+fileID+".svg", regularKey)
}

func TestCompletedMultipartFileRecordProjectsMetadata(t *testing.T) {
	t.Parallel()

	detectedMime := "image/png"
	slotID := siteEmailLogoSlotID
	attemptID := "attempt-1"
	session := model.UploadSession{
		FileID:       "file-1",
		FileName:     "email-logo.png",
		FileSize:     512,
		DetectedMime: &detectedMime,
		SlotID:       &slotID,
		AttemptID:    &attemptID,
	}
	require.Equal(t, structured.Fields{
		"extension":         "png",
		"id":                "file-1",
		"file_name":         "email-logo",
		"mime_type":         "image/png",
		"file_size":         int64(512),
		"ingest_slot_id":    siteEmailLogoSlotID,
		"ingest_attempt_id": "attempt-1",
	}, completedMultipartFileRecord(session))
}
