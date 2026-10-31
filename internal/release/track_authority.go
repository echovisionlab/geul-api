package release

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// TrackAuthority owns Track-row serialization and mutations used by File
// ingest. File identity, upload sessions, output allocation, and transcode
// commands remain owned by the File domain.
type TrackAuthority struct {
	audit domainaudit.Appender
}

// PolicyAuthority exposes the Release-owned root and active-principal fence to
// exact Track download policy consumers.
type PolicyAuthority struct{}

func NewPolicyAuthority() *PolicyAuthority { return &PolicyAuthority{} }

func (*PolicyAuthority) RequireLockedView(
	ctx context.Context,
	tx *gorm.DB,
	checker *auth.SpiceDBClient,
	releaseID string,
) error {
	err := requireLockedReleaseAction(ctx, tx, checker, releaseID, releaseActionView)
	return err
}

func (*PolicyAuthority) RequireLockedEdit(
	ctx context.Context,
	tx *gorm.DB,
	checker *auth.SpiceDBClient,
	releaseID string,
) error {
	err := requireLockedReleaseAction(ctx, tx, checker, releaseID, releaseActionEdit)
	return err
}

// NewTrackAuthority returns the Track authority of File ingest; audit is
// required.
func NewTrackAuthority(audit domainaudit.Appender) *TrackAuthority {
	if audit == nil {
		panic("release track authority audit appender is required")
	}
	return &TrackAuthority{audit: audit}
}

type TrackOriginalAudioInput struct {
	ClientMediaReady      bool
	DurationSeconds       *int
	TrackID               string
	VerifiedFileID        string
	ExpectedCurrentFileID *string
}

type TrackOriginalAudioAttachment struct {
	AlreadyApplied bool
	CurrentFileID  string
	ReleaseID      string
}

// LockExistsWithDB serializes File-owned Track upload-session work with Track
// mutation and deletion while preserving the caller's outer transaction.
func (a *TrackAuthority) LockExistsWithDB(ctx context.Context, tx *gorm.DB, trackID string) error {
	trackID = strings.TrimSpace(trackID)
	if _, err := uuid.Parse(trackID); err != nil {
		return domainerr.InvalidField("track_id", "must be a UUID")
	}
	if err := lockTrackDownloadAudienceSegments(ctx, tx, trackID); err != nil {
		return err
	}
	if err := lockTrackForUploadMutation(tx.WithContext(ctx), trackID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return domainerr.ResourceNotFound("track", trackID)
		}
		return err
	}
	return nil
}

func lockTrackDownloadAudienceSegments(ctx context.Context, tx *gorm.DB, trackID string) error {
	var rows []struct {
		ID string `gorm:"column:id"`
	}
	return tx.WithContext(ctx).
		Table("audience_segment AS segment").
		Select("segment.id").
		Joins("JOIN track_download_audience_segment AS association ON association.audience_segment_id = segment.id").
		Where("association.track_id = ?", trackID).
		Order("segment.id").
		Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate, Table: clause.Table{Name: "segment"}}).
		Find(&rows).Error
}

// AttachOriginalWithDB performs only the Track-owned CAS, processing-state
// transition, and audit append in the caller's transaction. The caller must
// validate and lock the verified File before invoking it.
func (a *TrackAuthority) AttachOriginalWithDB(
	ctx context.Context,
	tx *gorm.DB,
	input TrackOriginalAudioInput,
) (TrackOriginalAudioAttachment, error) {
	input.TrackID = strings.TrimSpace(input.TrackID)
	input.VerifiedFileID = strings.TrimSpace(input.VerifiedFileID)
	if _, err := uuid.Parse(input.TrackID); err != nil {
		return TrackOriginalAudioAttachment{}, domainerr.InvalidField("track_id", "must be a UUID")
	}
	if _, err := uuid.Parse(input.VerifiedFileID); err != nil {
		return TrackOriginalAudioAttachment{}, domainerr.InvalidField("verified_file_id", "must be a UUID")
	}
	if input.ExpectedCurrentFileID != nil {
		expected := strings.TrimSpace(*input.ExpectedCurrentFileID)
		if _, err := uuid.Parse(expected); err != nil {
			return TrackOriginalAudioAttachment{}, domainerr.InvalidField("expected_current_file_id", "must be a UUID when present")
		}
		input.ExpectedCurrentFileID = &expected
	}

	var track model.Track
	if err := tx.WithContext(ctx).Where("id = ?", input.TrackID).Take(&track).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return TrackOriginalAudioAttachment{}, domainerr.ResourceNotFound("track", input.TrackID)
		}
		return TrackOriginalAudioAttachment{}, err
	}
	output := TrackOriginalAudioAttachment{ReleaseID: track.ReleaseID}
	if track.AudioOriginalFileID != nil {
		output.CurrentFileID = *track.AudioOriginalFileID
	}
	if output.CurrentFileID == input.VerifiedFileID {
		output.AlreadyApplied = true
		return output, nil
	}
	if !sameOptionalString(track.AudioOriginalFileID, input.ExpectedCurrentFileID) {
		return TrackOriginalAudioAttachment{}, domainerr.Precondition("Track original audio changed before attachment")
	}
	updates := structured.Fields{"audio_original_file_id": input.VerifiedFileID, "processing_status": string(model.TrackProcessingStatusProcessing), "download_audience": "disabled"}
	if input.ClientMediaReady {
		updates["processing_status"] = string(model.TrackProcessingStatusCompleted)
		updates["duration_seconds"] = input.DurationSeconds
	}
	if err := tx.WithContext(ctx).Model(&track).Updates(updates).Error; err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if err := tx.WithContext(ctx).Where("track_id = ?", track.ID).
		Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if err := domainaudit.AppendSystem(ctx, tx, a.audit, sharedtelemetry.ServiceEditorCollab, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackAudioAuditRecord(m, track.ReleaseID, track.ID, input.VerifiedFileID, sharedtelemetry.AuditCollectionOperationAdded)
	}); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	output.CurrentFileID = input.VerifiedFileID
	return output, nil
}

func lockTrackForUploadMutation(tx *gorm.DB, trackID string) error {
	var track model.Track
	return tx.Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).Select("id").Where("id = ?", trackID).Take(&track).Error
}
