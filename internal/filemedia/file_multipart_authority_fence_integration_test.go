//go:build integration

package filemedia

import (
	"context"
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMultipartVerifiedFileMutationRevalidatesIndependentFileAndActorAuthorityIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	ctx := auth.WithUser(context.Background(), manager.AuthUserInfo())

	t.Run("stored target drift is rejected before File state", func(t *testing.T) {
		session := seedFinalizingMultipartAuthoritySession(t, stack.DB)
		authority, err := newMultipartFileIngestAuthority(session)
		require.NoError(t, err)
		require.NoError(t, stack.DB.Model(&model.UploadSession{}).
			Where("upload_id = ?", session.UploadID).
			Update("upload_type", managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String()).Error)

		err = (&FileService{db: stack.DB, spiceDB: stack.SpiceDBClient}).createVerifiedFileIngestRecord(
			ctx,
			verifiedMultipartAuthorityFile(session.FileID),
			managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
			"",
			authority,
		)
		require.ErrorContains(t, err, "upload authority changed")
		requireFileAuthorizationFenceLeftNoFile(t, stack.DB, session.FileID)
	})

	t.Run("inactive bilateral identity fence is rejected before File state", func(t *testing.T) {
		session := seedFinalizingMultipartAuthoritySession(t, stack.DB)
		authority, err := newMultipartFileIngestAuthority(session)
		require.NoError(t, err)
		require.NoError(t, stack.DB.Model(&model.Member{}).
			Where("id = ?", manager.MemberID).
			Update("account_identity_id", nil).Error)

		err = (&FileService{db: stack.DB, spiceDB: stack.SpiceDBClient}).createVerifiedFileIngestRecord(
			ctx,
			verifiedMultipartAuthorityFile(session.FileID),
			managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE,
			managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_UNSPECIFIED,
			"",
			authority,
		)
		require.ErrorContains(t, err, "authority was revoked")
		requireFileAuthorizationFenceLeftNoFile(t, stack.DB, session.FileID)
	})
}

func seedFinalizingMultipartAuthoritySession(
	t *testing.T,
	db *gorm.DB,
) model.UploadSession {
	t.Helper()
	now := time.Now().UTC()
	attemptID := uuid.NewString()
	session := model.UploadSession{
		UploadID:       uuid.NewString(),
		FileID:         uuid.NewString(),
		UploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		FileName:       "authority.jpg",
		FileSize:       1024,
		AttemptID:      &attemptID,
		RequestedMime:  "image/jpeg",
		TotalParts:     1,
		ChunkSize:      1024,
		Status:         model.UploadSessionStatusFinalizing,
		LastActivityAt: now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	require.NoError(t, db.Omit("EntityID", "EntityType", "SlotID", "ExpectedFileID").Create(&session).Error)
	return session
}

func verifiedMultipartAuthorityFile(fileID string) structured.Fields {
	return structured.Fields{
		"id":                fileID,
		"file_name":         "authority.jpg",
		"mime_type":         "image/jpeg",
		"file_size":         int64(1024),
		"extension":         "jpg",
		"sha256":            make([]byte, 32),
		"ingest_attempt_id": uuid.NewString(),
	}
}

func requireFileAuthorizationFenceLeftNoFile(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.Zero(t, count)
}
