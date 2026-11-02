package filemedia

import (
	"context"
	"time"

	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/model"
)

// ExpiredUploadSession is an upload session whose last activity is older
// than the cleanup cutoff.
type ExpiredUploadSession struct {
	ClientMediaBundleID   *string `gorm:"column:client_media_bundle_id"`
	ClientMediaManifest   *string `gorm:"column:client_media_manifest"`
	UploadType            string  `gorm:"column:upload_type"`
	UploadID              string  `gorm:"column:upload_id"`
	FileID                string  `gorm:"column:file_id"`
	RequestedMime         string  `gorm:"column:requested_mime"`
	EntityID              string  `gorm:"column:entity_id"`
	EntityType            *string `gorm:"column:entity_type"`
	SlotID                *string `gorm:"column:slot_id"`
	AttemptID             *string `gorm:"column:attempt_id"`
	ExpectedCurrentFileID *string `gorm:"column:expected_current_file_id"`
	Status                string  `gorm:"column:status"`
}

// ExpiredUploadSessions lists the sessions in statuses with no activity
// since cutoff.
func ExpiredUploadSessions(ctx context.Context, db *gorm.DB, statuses []model.UploadSessionStatus, cutoff time.Time) ([]ExpiredUploadSession, error) {
	var sessions []ExpiredUploadSession
	err := db.WithContext(ctx).
		Table("upload_session").
		Select(
			"upload_type", "upload_id", "file_id", "requested_mime", "entity_id", "entity_type",
			"slot_id", "attempt_id",
			"expected_current_file_id", "status", "client_media_bundle_id", "client_media_manifest",
		).
		Where("status IN ? AND last_activity_at < ?", statuses, cutoff).
		Find(&sessions).Error
	return sessions, err
}

// ClaimExpiredUploadSession aborts the session if it is still in statuses
// with no activity since cutoff, and reports whether it did.
func ClaimExpiredUploadSession(ctx context.Context, db *gorm.DB, uploadID string, statuses []model.UploadSessionStatus, cutoff time.Time) (bool, error) {
	claim := db.WithContext(ctx).
		Table("upload_session").
		Where("upload_id = ? AND status IN ? AND last_activity_at < ?", uploadID, statuses, cutoff).
		Update("status", model.UploadSessionStatusAborted)
	return claim.RowsAffected != 0, claim.Error
}

// DeleteAbortedUploadSession deletes a claimed session that saw no activity
// since cutoff and returns how many rows it deleted.
func DeleteAbortedUploadSession(ctx context.Context, db *gorm.DB, uploadID string, cutoff time.Time) (int64, error) {
	result := db.WithContext(ctx).
		Table("upload_session").
		Where("upload_id = ? AND status = ? AND last_activity_at < ?", uploadID, model.UploadSessionStatusAborted, cutoff).
		Delete(nil)
	return result.RowsAffected, result.Error
}
