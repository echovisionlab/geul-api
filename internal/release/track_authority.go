package release

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/structured"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
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
	_, err := requireLockedReleaseAction(ctx, tx, checker, releaseID, releaseActionView)
	return err
}

func (*PolicyAuthority) RequireLockedEdit(
	ctx context.Context,
	tx *gorm.DB,
	checker *auth.SpiceDBClient,
	releaseID string,
) error {
	_, err := requireLockedReleaseAction(ctx, tx, checker, releaseID, releaseActionEdit)
	return err
}

func NewTrackAuthority(audit domainaudit.Appender) *TrackAuthority {
	return &TrackAuthority{audit: audit}
}

type TrackOriginalAudioInput struct {
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
		return errs.InvalidArgument("track_id", "must be a UUID")
	}
	if err := lockTrackDownloadAudienceSegments(ctx, tx, trackID); err != nil {
		return err
	}
	if err := lockTrackForUploadMutation(tx.WithContext(ctx), trackID); err != nil {
		if err == gorm.ErrRecordNotFound {
			return errs.NotFound("track", trackID)
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
		Clauses(clause.Locking{Strength: "UPDATE", Table: clause.Table{Name: "segment"}}).
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
		return TrackOriginalAudioAttachment{}, errs.InvalidArgument("track_id", "must be a UUID")
	}
	if _, err := uuid.Parse(input.VerifiedFileID); err != nil {
		return TrackOriginalAudioAttachment{}, errs.InvalidArgument("verified_file_id", "must be a UUID")
	}
	if input.ExpectedCurrentFileID != nil {
		expected := strings.TrimSpace(*input.ExpectedCurrentFileID)
		if _, err := uuid.Parse(expected); err != nil {
			return TrackOriginalAudioAttachment{}, errs.InvalidArgument("expected_current_file_id", "must be a UUID when present")
		}
		input.ExpectedCurrentFileID = &expected
	}

	var track model.Track
	if err := tx.WithContext(ctx).Where("id = ?", input.TrackID).Take(&track).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return TrackOriginalAudioAttachment{}, errs.NotFound("track", input.TrackID)
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
		return TrackOriginalAudioAttachment{}, errs.FailedPrecondition("Track original audio changed before attachment")
	}
	if err := tx.WithContext(ctx).Model(&track).Updates(structured.Fields{
		"audio_original_file_id": input.VerifiedFileID,
		"processing_status":      managev1.TrackProcessingStatus_TRACK_PROCESSING_STATUS_PROCESSING.String(),
		"download_audience":      "disabled",
	}).Error; err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if err := tx.WithContext(ctx).Where("track_id = ?", track.ID).
		Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if a.audit != nil {
		if err := domainaudit.AppendSystem(ctx, tx, a.audit, sharedtelemetry.ServiceEditorCollab, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackAudioAuditRecord(m, track.ReleaseID, track.ID, input.VerifiedFileID, sharedtelemetry.AuditCollectionOperationAdded)
		}); err != nil {
			return TrackOriginalAudioAttachment{}, err
		}
	}
	output.CurrentFileID = input.VerifiedFileID
	return output, nil
}

func lockTrackForUploadMutation(tx *gorm.DB, trackID string) error {
	var track model.Track
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id").Where("id = ?", trackID).Take(&track).Error
}
