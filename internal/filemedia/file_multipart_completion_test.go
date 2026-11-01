package filemedia

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestValidateMultipartCompletionVerifiedMimeRequiresDetectedMime(t *testing.T) {
	t.Parallel()

	_, err := validateMultipartCompletionVerifiedMime(UploadEditorImage,

		domain.UploadSession{
			UploadType: managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		},
		domain.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String()],
	)

	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, errs.CodeOf(err))
	require.Contains(t, err.Error(), errs.MsgMimeNotVerified)
}

func TestValidateMultipartCompletionVerifiedMimeAppliesSiteEmailLogoPNGPolicy(t *testing.T) {
	t.Parallel()

	cfg := domain.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_SITE_LOGO.String()]
	emailSlotID := siteLogoSlotEmailLight
	regularSlotID := "logo"
	svgMime := "image/svg+xml"

	_, err := validateMultipartCompletionVerifiedMime(UploadSiteLogo,

		domain.UploadSession{
			UploadType:    managev1.UploadType_UPLOAD_TYPE_SITE_LOGO.String(),
			DetectedMime:  &svgMime,
			RequestedMime: svgMime,
			SlotID:        &emailSlotID,
		},
		cfg,
	)
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, errs.CodeOf(err))
	require.Contains(t, err.Error(), "email logo uploads must be PNG")

	verifiedMime, err := validateMultipartCompletionVerifiedMime(UploadSiteLogo,

		domain.UploadSession{
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

	svc := &Service{}
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
	slotID := siteLogoSlotEmailLight
	attemptID := "attempt-1"
	session := domain.UploadSession{
		FileID:       "file-1",
		FileName:     "email-logo.png",
		FileSize:     512,
		DetectedMime: &detectedMime,
		SlotID:       &slotID,
		AttemptID:    &attemptID,
	}
	require.Equal(t, File{ID: "file-1", FileName: "email-logo", MimeType: "image/png", FileSize: 512, Extension: "png", IngestSlotID: &slotID, IngestAttemptID: &attemptID}, completedMultipartFileRecord(session).file())
}
