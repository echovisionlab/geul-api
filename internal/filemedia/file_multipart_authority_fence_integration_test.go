//go:build integration

package filemedia

import (
	"context"
	"testing"
	"time"

	filemediapostgres "github.com/echovisionlab/geul-api/internal/filemedia/postgres"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMultipartVerifiedFileMutationRevalidatesIndependentFileAndActorAuthorityIntegration(t *testing.T) {
	t.Parallel()
	stack := testutil.SetupOryStack(t)
	manager := stack.CreateUser(t, policyv1.Role.Author().ID())
	ctx := auditedUserContext(context.Background(), manager.AuthUserInfo())

	t.Run("stored target drift is rejected before File state", func(t *testing.T) {
		session := seedFinalizingMultipartAuthoritySession(t, stack.DB)
		authority, err := newMultipartFileIngestAuthority(session)
		require.NoError(t, err)
		require.NoError(t, stack.DB.Model(&model.UploadSession{}).
			Where("upload_id = ?", session.UploadID).
			Update("upload_type", managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO.String()).Error)

		err = (withTestMutations(withNativeFileUnits(&Service{spiceDB: stack.SpiceDBClient}, stack.DB, nativeFilePorts{}))).createVerifiedFileIngestRecord(
			ctx,
			verifiedMultipartAuthorityFile(session.FileID), UploadEditorImage, EntityUnspecified,

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

		err = (withTestMutations(withNativeFileUnits(&Service{spiceDB: stack.SpiceDBClient}, stack.DB, nativeFilePorts{}))).createVerifiedFileIngestRecord(
			ctx,
			verifiedMultipartAuthorityFile(session.FileID), UploadEditorImage, EntityUnspecified,

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
) domain.UploadSession {
	t.Helper()
	now := time.Now().UTC()
	attemptID := uuid.NewString()
	session := domain.UploadSession{
		UploadID:       uuid.NewString(),
		FileID:         uuid.NewString(),
		UploadType:     managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE.String(),
		FileName:       "authority.jpg",
		FileSize:       1024,
		AttemptID:      &attemptID,
		RequestedMime:  "image/jpeg",
		TotalParts:     1,
		ChunkSize:      1024,
		Status:         domain.UploadSessionStatusFinalizing,
		LastActivityAt: now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	row := filemediapostgres.UploadSessionRow(session)
	require.NoError(t, db.Omit("EntityID", "EntityType", "SlotID", "ExpectedFileID").Create(&row).Error)
	return session
}

func verifiedMultipartAuthorityFile(fileID string) fileIngestRecord {
	return fileIngestRecord{
		id:              fileID,
		fileName:        "authority.jpg",
		mimeType:        "image/jpeg",
		fileSize:        1024,
		extension:       "jpg",
		sha256:          make([]byte, 32),
		ingestAttemptID: uuid.NewString(),
	}
}

func requireFileAuthorizationFenceLeftNoFile(t *testing.T, db *gorm.DB, fileID string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	require.Zero(t, count)
}
