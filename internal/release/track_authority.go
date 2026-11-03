package release

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	"github.com/echovisionlab/geul-api/internal/domainerr"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// RequireLockedView and RequireLockedEdit lock the Release root and the
// active principal in the joined transaction, then check exactly one
// object permission, for an exact Track download policy consumer.
func RequireLockedView(ctx context.Context, store Store, lock func(*auth.UserInfo) (bool, error), checker CollaborationPermissionChecker, releaseID string) error {
	return requireLockedReleaseAction(ctx, store, lock, checker, releaseID, releaseActionView)
}

func RequireLockedEdit(ctx context.Context, store Store, lock func(*auth.UserInfo) (bool, error), checker CollaborationPermissionChecker, releaseID string) error {
	return requireLockedReleaseAction(ctx, store, lock, checker, releaseID, releaseActionEdit)
}

// TrackOriginalAudioInput is the verified original audio a File ingest
// attaches to a Track.
type TrackOriginalAudioInput struct {
	ClientMediaReady      bool
	DurationSeconds       *int
	TrackID               string
	VerifiedFileID        string
	ExpectedCurrentFileID *string
}

// TrackOriginalAudioAttachment is the Track's original audio after an
// attachment.
type TrackOriginalAudioAttachment struct {
	AlreadyApplied bool
	CurrentFileID  string
	ReleaseID      string
}

// LockTrackForUpload serializes File-owned Track upload-session work with
// Track mutation and deletion in the File ingest's transaction.
func LockTrackForUpload(ctx context.Context, store Store, trackID string) error {
	trackID = strings.TrimSpace(trackID)
	if _, err := uuid.Parse(trackID); err != nil {
		return domainerr.InvalidField("track_id", "must be a UUID")
	}
	if err := store.LockTrackDownloadCriteriaSet(ctx, trackID); err != nil {
		return err
	}
	if err := store.LockTrackID(ctx, trackID); err != nil {
		return missingAs(err, ErrTrackNotFound, domainerr.ResourceNotFound("track", trackID))
	}
	return nil
}

// AttachTrackOriginalAudio performs only the Track-owned CAS,
// processing-state transition, and audit append in the File ingest's
// transaction. The caller must validate and lock the verified File before
// invoking it.
func AttachTrackOriginalAudio(
	ctx context.Context,
	joined Joined,
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

	track, err := joined.Store.FindTrack(ctx, input.TrackID)
	if err != nil {
		return TrackOriginalAudioAttachment{}, missingAs(err, ErrTrackNotFound, domainerr.ResourceNotFound("track", input.TrackID))
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
	if err := joined.Store.AttachTrackAudio(ctx, track.ID, TrackAudio{
		FileID: input.VerifiedFileID, Completed: input.ClientMediaReady, DurationSeconds: input.DurationSeconds,
	}); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	if err := domainaudit.AppendSystem(ctx, nil, joined.Audit, sharedtelemetry.ServiceEditorCollab, sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackAudioAuditRecord(m, track.ReleaseID, track.ID, input.VerifiedFileID, sharedtelemetry.AuditCollectionOperationAdded)
	}); err != nil {
		return TrackOriginalAudioAttachment{}, err
	}
	output.CurrentFileID = input.VerifiedFileID
	return output, nil
}
