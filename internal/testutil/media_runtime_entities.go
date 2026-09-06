//go:build integration

package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/contentblock"
	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

type ManagedEditorEntity struct {
	Name         string
	EntityID     string
	EntityType   managev1.TranscodeEntityType
	SourceLocale string
}

type IncompleteUploadFixture struct {
	UploadID string
	FileID   string
}

type ManagedReleaseTrackPendingUploadFixture struct {
	ReleaseID string
	TrackID   string
	UploadID  string
	FileID    string
	AttemptID string
	FileName  string
	FileSize  int64
}

func CreateManagedEditorEntities(
	t require.TestingT,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	managerAccountIdentityID string,
) []ManagedEditorEntity {
	post := CreateManagedEditorEntity(t, db, spiceDB, managerAccountIdentityID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST, "ko")
	page := CreateManagedEditorEntity(t, db, spiceDB, managerAccountIdentityID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE, "en")
	work := CreateManagedEditorEntity(t, db, spiceDB, managerAccountIdentityID, managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK, "ja")
	return []ManagedEditorEntity{post, page, work}
}

func CreateManagedEditorEntity(
	t require.TestingT,
	db *gorm.DB,
	spiceDB *auth.SpiceDBClient,
	managerAccountIdentityID string,
	entityType managev1.TranscodeEntityType,
	sourceLocale string,
) ManagedEditorEntity {
	entityID := uuid.NewString()
	now := time.Now().UTC()

	switch entityType {
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_POST:
		documentID := createManagedEditorContentDocument(t, db, "post", sourceLocale)
		require.NoError(t, db.Create(&model.Post{
			ID:                entityID,
			ContentDocumentID: &documentID,
			SourceLocale:      sourceLocale,
			DocumentLayout:    model.DefaultDocumentLayout(),
			Status:            model.PostStatus("POST_STATUS_DRAFT"),
			CommentsEnabled:   true,
			CreatedAt:         now,
			UpdatedAt:         now,
		}).Error)
		maybeGrantResourceManager(t, spiceDB, "post", entityID, managerAccountIdentityID)
		return ManagedEditorEntity{
			Name:         "post",
			EntityID:     entityID,
			EntityType:   entityType,
			SourceLocale: sourceLocale,
		}
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_PAGE:
		documentID := createManagedEditorContentDocument(t, db, "page", sourceLocale)
		require.NoError(t, db.Create(&model.Page{
			ID:                entityID,
			ContentDocumentID: &documentID,
			SourceLocale:      sourceLocale,
			DocumentLayout:    model.DefaultDocumentLayout(),
			Status:            model.PageStatus("PAGE_STATUS_DRAFT"),
			ShowTitle:         true,
			CreatedAt:         now,
			UpdatedAt:         now,
		}).Error)
		maybeGrantResourceManager(t, spiceDB, "page", entityID, managerAccountIdentityID)
		return ManagedEditorEntity{
			Name:         "page",
			EntityID:     entityID,
			EntityType:   entityType,
			SourceLocale: sourceLocale,
		}
	case managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_WORK:
		documentID := createManagedEditorContentDocument(t, db, "work", sourceLocale)
		untilYear := int32(now.Year())
		untilMonth := int32(int(now.Month()))
		require.NoError(t, db.Create(&model.Work{
			ID:                entityID,
			ContentDocumentID: &documentID,
			SourceLocale:      sourceLocale,
			Type:              managev1.WorkType_WORK_TYPE_MUSIC_PROJECT.String(),
			Year:              untilYear,
			Month:             untilMonth,
			UntilYear:         &untilYear,
			UntilMonth:        &untilMonth,
			IsPresent:         false,
			Status:            managev1.WorkStatus_WORK_STATUS_DRAFT.String(),
			CreatedAt:         now,
			UpdatedAt:         now,
		}).Error)
		maybeGrantResourceManager(t, spiceDB, "work", entityID, managerAccountIdentityID)
		return ManagedEditorEntity{
			Name:         "work",
			EntityID:     entityID,
			EntityType:   entityType,
			SourceLocale: sourceLocale,
		}
	default:
		require.FailNow(t, fmt.Sprintf("unsupported managed editor entity type: %s", entityType.String()))
		return ManagedEditorEntity{}
	}
}

func createManagedEditorContentDocument(
	t require.TestingT,
	db *gorm.DB,
	profile string,
	sourceLocale string,
) string {
	store, err := contentblock.NewGeneratedStore(editorFixtureFileReuseAuthorizer{})
	require.NoError(t, err)
	var documentID string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		snapshot, err := store.CreateDocument(context.Background(), tx, contentblock.CreateInput{
			Profile: profile, SourceLocale: sourceLocale,
		})
		if err != nil {
			return err
		}
		documentID = snapshot.Document.ID.String()
		return nil
	}))
	return documentID
}

func maybeGrantResourceManager(
	t require.TestingT,
	spiceDB *auth.SpiceDBClient,
	resourceType string,
	resourceID string,
	managerAccountIdentityID string,
) {
	if spiceDB == nil || managerAccountIdentityID == "" {
		return
	}
	postResource, err := policyv1.Post.Resource(resourceID)
	require.NoError(t, err)
	if resourceType != postResource.Type() {
		// Page and Work have no direct manager relation in the closed schema;
		// their access is derived exclusively from platform roles.
		return
	}
	actor, err := policyv1.NewAccountIdentityActor(managerAccountIdentityID)
	require.NoError(t, err)
	mutation, err := policyv1.Post.TouchAuthor(resourceID, actor)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(context.Background(), mutation)
	require.NoError(t, err)
}

func CreateReleaseFixture(
	t require.TestingT,
	db *gorm.DB,
) string {
	releaseID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(
			`INSERT INTO content_document (id, profile) VALUES (?, 'compact')`,
			documentID,
		).Error; err != nil {
			return err
		}
		return tx.Exec(
			`INSERT INTO release (id, type, content_document_id) VALUES (?, 'RELEASE_TYPE_ALBUM', ?)`,
			releaseID,
			documentID,
		).Error
	}))
	return releaseID
}

func CreateManagedReleaseTrack(
	t require.TestingT,
	db *gorm.DB,
	releaseID string,
	title string,
) string {
	trackID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO track (id, release_id, track_number, title)
		VALUES (?, ?, 1, ?)
	`, trackID, releaseID, title).Error)
	return trackID
}

// AssertManagedReleaseTrackAuthority validates the relational Track root used
// by the original-audio attachment flow.
func AssertManagedReleaseTrackAuthority(
	t *testing.T,
	db *gorm.DB,
	releaseID string,
	trackID string,
) {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("track").
		Where("id = ? AND release_id = ?", trackID, releaseID).
		Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func SeedReleaseTrackPendingUploadFixture(
	t *testing.T,
	db *gorm.DB,
	releaseID string,
	trackID string,
	fileSize int64,
) ManagedReleaseTrackPendingUploadFixture {
	t.Helper()

	upload := NewIncompleteUploadFixture()
	now := time.Now().UTC()
	pending := ManagedReleaseTrackPendingUploadFixture{
		ReleaseID: releaseID,
		TrackID:   trackID,
		UploadID:  upload.UploadID,
		FileID:    upload.FileID,
		AttemptID: uuid.NewString(),
		FileName:  "expired-track-upload-" + uuid.NewString() + ".wav",
		FileSize:  fileSize,
	}

	var count int64
	require.NoError(t, db.Table("track").Where("id = ? AND release_id = ?", trackID, releaseID).Count(&count).Error)
	require.EqualValues(t, 1, count)

	chunkSize := int64(10 * 1024 * 1024)
	totalParts := int32((pending.FileSize + chunkSize - 1) / chunkSize)
	require.NoError(t, db.Exec(`
		INSERT INTO upload_session (
			upload_id,
			file_id,
			upload_type,
			entity_id,
			entity_type,
			file_name,
			file_size,
			file_last_modified,
			attempt_id,
			requested_mime,
			total_parts,
			chunk_size,
			status,
			last_activity_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'uploading', ?)
	`,
		pending.UploadID,
		pending.FileID,
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String(),
		trackID,
		managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String(),
		pending.FileName,
		pending.FileSize,
		now.UnixMilli(),
		pending.AttemptID,
		"audio/wav",
		totalParts,
		chunkSize,
		now,
	).Error)

	return pending
}

func ReadReleaseTrackOriginalFileID(
	t *testing.T,
	db *gorm.DB,
	releaseID string,
	trackID string,
) string {
	t.Helper()

	var fileID *string
	require.NoError(t, db.Table("track").
		Select("audio_original_file_id").
		Where("id = ? AND release_id = ?", trackID, releaseID).
		Scan(&fileID).Error)
	if fileID == nil {
		return ""
	}
	return *fileID
}

func NewIncompleteUploadFixture() IncompleteUploadFixture {
	return IncompleteUploadFixture{
		UploadID: uuid.NewString(),
		FileID:   uuid.NewString(),
	}
}
