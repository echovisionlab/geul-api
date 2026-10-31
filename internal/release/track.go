// Package release owns Release-domain aggregates extracted from the legacy
// service package. The managed Track aggregate is fully owned here.
package release

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/mutation"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ErrTrackUploadSessionsChanged keeps Track deletion behind the same locked
// upload-session fence used by a Release cascade.
var ErrTrackUploadSessionsChanged = errors.New("track upload sessions changed during deletion")

const MaxTrackUploadCleanupPasses = 3

// ListTracksQuery names the Release whose Tracks are listed.
type ListTracksQuery struct {
	ReleaseID string
}

// CreateTrackCommand appends a Track to a Release.
type CreateTrackCommand struct {
	ReleaseID       string
	Title           string
	DurationSeconds *int32
	Lyrics          *string
}

// UpdateTrackCommand updates Track settings. Nil fields are left alone, an
// empty Title keeps the title, and the Clear flags win over supplied values.
type UpdateTrackCommand struct {
	ID                 string
	TrackNumber        *int32
	Title              *string
	DurationSeconds    *int32
	ProcessingStatus   *string
	Lyrics             *string
	ClearDuration      bool
	ClearAudioOriginal bool
	ClearLyrics        bool
}

// DeleteTrackCommand deletes a Track.
type DeleteTrackCommand struct {
	ID string
}

// SetTrackCreditsCommand applies the desired credits against the observed
// ones.
type SetTrackCreditsCommand struct {
	TrackID  string
	Credits  []*CreditInput
	Observed *Snapshot[*CreditInput]
}

// ReorderTracksCommand orders every Track of one Release.
type ReorderTracksCommand struct {
	TrackIDs []string
}

// ListTracksByRelease lists all tracks for a release with credits for a Site Admin.
func (s *Service) ListTracksByRelease(ctx context.Context, query ListTracksQuery) ([]*managev1.TrackWithCredits, error) {
	if query.ReleaseID == "" {
		return nil, domainerr.Required("release_id")
	}
	if err := requireReleaseAction(ctx, s.spiceDB, query.ReleaseID, releaseActionView); err != nil {
		return nil, err
	}
	return s.releaseTracksWithCredits(ctx, query.ReleaseID)
}

func (s *Service) releaseTracksWithCredits(ctx context.Context, releaseID string) ([]*managev1.TrackWithCredits, error) {
	var tracks []model.Track
	if err := s.db.WithContext(ctx).
		Where("release_id = ?", releaseID).
		Order("track_number ASC").
		Find(&tracks).Error; err != nil {
		return nil, err
	}
	result := make([]*managev1.TrackWithCredits, len(tracks))
	for i := range tracks {
		credits, err := s.getCreditsForTrack(ctx, tracks[i].ID)
		if err != nil {
			return nil, err
		}
		result[i] = &managev1.TrackWithCredits{Track: trackMessage(ctx, &tracks[i]), Credits: credits}
	}
	return result, nil
}

// CreateTrack appends a track to a release (Release managers).
func (s *Service) CreateTrack(ctx context.Context, command CreateTrackCommand) (*managev1.Track, error) {
	if command.ReleaseID == "" {
		return nil, domainerr.Required("release_id")
	}
	if command.Title == "" {
		return nil, domainerr.Required("title")
	}
	track, err := mutation.Run(ctx, s.mutations, mutation.Spec[model.Track]{
		// The Release lock also serializes concurrent track numbering.
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			_, err := lockReleaseForUpdate(ctx, tx, command.ReleaseID)
			return missingAs(err, notFound("release not found"))
		},
		Access: s.releaseAccess(command.ReleaseID, releaseActionManage),
		Change: func(_ context.Context, tx *gorm.DB) (model.Track, error) {
			var maxTrackNumber int
			if err := tx.Raw(`
				SELECT COALESCE(MAX(track_number), 0) FROM track
				WHERE release_id = $1`, command.ReleaseID).Scan(&maxTrackNumber).Error; err != nil {
				return model.Track{}, err
			}
			track := model.Track{
				ReleaseID:   command.ReleaseID,
				TrackNumber: maxTrackNumber + 1,
				Title:       command.Title,
				Metadata:    make(model.TrackMetadata),
				Lyrics:      command.Lyrics,
			}
			if command.DurationSeconds != nil {
				duration := int(*command.DurationSeconds)
				track.DurationSeconds = &duration
			}
			if err := tx.Clauses(clause.Returning{}).Create(&track).Error; err != nil {
				return model.Track{}, err
			}
			return track, nil
		},
		Audit: func(track model.Track) mutation.Audit {
			return releaseTrackAudit(track.ReleaseID, track.ID, sharedtelemetry.AuditItemOperationCreated)
		},
		Relationships: func(track model.Track) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			touchPolicy, err := policyv1.Track.TouchPolicy(track.ID)
			if err != nil {
				return nil, nil, err
			}
			deletePolicy, err := policyv1.Track.DeletePolicy(track.ID)
			return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, err
		},
	})
	if err != nil {
		return nil, err
	}
	return trackMessage(ctx, &track), nil
}

// trackUpdate is a Track before and after an update.
type trackUpdate struct {
	before, after model.Track
}

func (u trackUpdate) changed() bool {
	return !reflect.DeepEqual(u.before, u.after)
}

// UpdateTrack updates an existing track (Track editors).
func (s *Service) UpdateTrack(ctx context.Context, command UpdateTrackCommand) (*managev1.Track, error) {
	if command.ID == "" {
		return nil, domainerr.Required("id")
	}
	var locked model.Track
	update, err := mutation.Run(ctx, s.mutations, mutation.Spec[trackUpdate]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if err := lockTrackDownloadAudienceSegments(ctx, tx, command.ID); err != nil {
				return err
			}
			return lockTrackRow(ctx, tx, command.ID, &locked)
		},
		Access: s.trackAccess(command.ID, trackActionEdit),
		Change: func(ctx context.Context, tx *gorm.DB) (trackUpdate, error) {
			// A concurrent update is serialized by the lock; apply the request to its current row.
			update := trackUpdate{before: locked, after: command.apply(locked)}
			if !update.changed() {
				return update, nil
			}
			if err := saveTrackUpdate(tx, &update); err != nil {
				return trackUpdate{}, err
			}
			return update, nil
		},
		Audit: trackUpdateAudit,
	})
	if err != nil {
		return nil, err
	}
	return trackMessage(ctx, &update.after), nil
}

func (command UpdateTrackCommand) apply(track model.Track) model.Track {
	if command.TrackNumber != nil {
		track.TrackNumber = int(*command.TrackNumber)
	}
	if command.Title != nil && *command.Title != "" {
		track.Title = *command.Title
	}
	if command.DurationSeconds != nil {
		duration := int(*command.DurationSeconds)
		track.DurationSeconds = &duration
	}
	if command.ClearDuration {
		track.DurationSeconds = nil
	}
	if command.ClearAudioOriginal {
		track.AudioOriginalFileID = nil
	}
	if command.ProcessingStatus != nil {
		track.ProcessingStatus = command.ProcessingStatus
	}
	if command.Lyrics != nil {
		track.Lyrics = command.Lyrics
	}
	if command.ClearLyrics {
		track.Lyrics = nil
	}
	return track
}

// saveTrackUpdate stores the updated row. Another original audio file is
// a different download, so its audience starts disabled again.
func saveTrackUpdate(tx *gorm.DB, update *trackUpdate) error {
	if err := tx.Save(&update.after).Error; err != nil {
		return err
	}
	if sameOptionalString(update.before.AudioOriginalFileID, update.after.AudioOriginalFileID) {
		return nil
	}
	if err := tx.Model(&model.Track{}).Where("id = ?", update.after.ID).
		Update("download_audience", "disabled").Error; err != nil {
		return err
	}
	if err := tx.Where("track_id = ?", update.after.ID).
		Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
		return err
	}
	update.after.DownloadAudience = "disabled"
	return nil
}

// trackUpdateAudit records a metadata change and a removed original audio
// file separately.
func trackUpdateAudit(update trackUpdate) mutation.Audit {
	if !update.changed() {
		return mutation.Unchanged()
	}
	var records []mutation.Audit
	metadataBefore, metadataAfter := update.before, update.after
	metadataBefore.AudioOriginalFileID, metadataAfter.AudioOriginalFileID = nil, nil
	if !reflect.DeepEqual(metadataBefore, metadataAfter) {
		records = append(records, releaseTrackAudit(update.after.ReleaseID, update.after.ID, sharedtelemetry.AuditItemOperationUpdated))
	}
	if removed := update.before.AudioOriginalFileID; removed != nil && update.after.AudioOriginalFileID == nil {
		records = append(records, mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
			return sharedtelemetry.NewReleaseTrackAudioAuditRecord(m, update.after.ReleaseID, update.after.ID, *removed, sharedtelemetry.AuditCollectionOperationRemoved)
		}))
	}
	if len(records) == 0 {
		return mutation.Unchanged()
	}
	return mutation.All(records...)
}

// DeleteTrack deletes a track (Track editors with delete permission).
func (s *Service) DeleteTrack(ctx context.Context, command DeleteTrackCommand) error {
	if command.ID == "" {
		return domainerr.Required("id")
	}
	// Upload cleanup aborts uploads outside any transaction, so the caller's
	// authority is decided under the Track lock before it runs.
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[struct{}]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			return lockTrackRow(ctx, tx, command.ID, nil)
		},
		Access: s.trackAccess(command.ID, trackActionDelete),
		Change: func(context.Context, *gorm.DB) (struct{}, error) { return struct{}{}, nil },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	}); err != nil {
		return err
	}
	deleted, err := s.deleteTrackAfterUploadCleanup(ctx, command.ID)
	if err != nil {
		return err
	}
	if err := s.waveformJobs.CancelTracks(
		ctx,
		[]string{deleted.ID},
		managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
	); err != nil {
		slog.ErrorContext(ctx, "Failed to cancel waveform jobs for deleted track", "trackId", deleted.ID, failurecause.Attr(err))
	}
	s.transcodes.CancelFiles(
		ctx,
		deleted.ID,
		managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
		deleted.AudioOriginalFileID,
	)
	return nil
}

// deleteTrackAfterUploadCleanup cleans up the Track's audio uploads and
// deletes it, cleaning up again when an upload started in between.
func (s *Service) deleteTrackAfterUploadCleanup(ctx context.Context, trackID string) (model.Track, error) {
	var uploadsChanged error
	for range MaxTrackUploadCleanupPasses {
		if err := s.trackFiles.CleanupTrackUploadSessions(ctx, trackID, "Track deleted"); err != nil {
			if errors.Is(err, ErrTrackUploadSessionNotAbortable) {
				return model.Track{}, domainerr.Precondition("track audio upload is finalizing; retry its completion before deleting the track").WithCause(err)
			}
			return model.Track{}, fmt.Errorf("cleanup track audio uploads: %w", err)
		}
		deleted, err := s.deleteTrackWhenNoUploadSessions(ctx, trackID)
		if errors.Is(err, ErrTrackUploadSessionsChanged) {
			uploadsChanged = err
			continue
		}
		return deleted, missingAs(err, trackNotFound())
	}
	return model.Track{}, domainerr.Precondition("track audio uploads changed during deletion; retry the request").WithCause(uploadsChanged)
}

func (s *Service) deleteTrackWhenNoUploadSessions(ctx context.Context, trackID string) (model.Track, error) {
	var track model.Track
	return mutation.Run(ctx, s.mutations, mutation.Spec[model.Track]{
		Lock: func(ctx context.Context, tx *gorm.DB) error {
			if err := lockTrackForUploadMutation(tx, trackID); err != nil {
				return err
			}
			return tx.First(&track, "id = ?", trackID).Error
		},
		Access: s.trackAccess(trackID, trackActionDelete),
		Change: func(ctx context.Context, tx *gorm.DB) (model.Track, error) {
			if err := s.trackFiles.RequireNoTrackUploadSessionsWithDB(ctx, tx, trackID); err != nil {
				return model.Track{}, err
			}
			result := tx.Where("id = ?", trackID).Delete(&model.Track{})
			if result.Error != nil {
				return model.Track{}, result.Error
			}
			if result.RowsAffected != 1 {
				return model.Track{}, gorm.ErrRecordNotFound
			}
			return track, nil
		},
		Audit: func(track model.Track) mutation.Audit {
			return releaseTrackAudit(track.ReleaseID, track.ID, sharedtelemetry.AuditItemOperationDeleted)
		},
		Relationships: func(track model.Track) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			deletePolicy, err := policyv1.Track.DeletePolicy(track.ID)
			if err != nil {
				return nil, nil, err
			}
			touchPolicy, err := policyv1.Track.TouchPolicy(track.ID)
			return []policyv1.RelationshipMutation{deletePolicy}, []policyv1.RelationshipMutation{touchPolicy}, err
		},
	})
}

// ReorderTracks orders every track of a release (Release managers).
func (s *Service) ReorderTracks(ctx context.Context, command ReorderTracksCommand) ([]*managev1.TrackWithCredits, error) {
	releaseID, err := s.trackOrderRelease(ctx, command.TrackIDs)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.Spec[bool]{
		Lock:   lockReleaseRow(releaseID, nil),
		Access: s.releaseAccess(releaseID, releaseActionManage),
		Change: func(_ context.Context, tx *gorm.DB) (bool, error) {
			return reorderReleaseTracks(tx, releaseID, command.TrackIDs)
		},
		Audit: func(changed bool) mutation.Audit {
			if !changed {
				return mutation.Unchanged()
			}
			return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
				return sharedtelemetry.NewReleaseTrackOrderAuditRecord(m, releaseID, command.TrackIDs)
			})
		},
	}); err != nil {
		return nil, err
	}
	return s.releaseTracksWithCredits(ctx, releaseID)
}

// trackOrderRelease checks that trackIDs are every Track of one Release, in
// some order, and returns that Release.
func (s *Service) trackOrderRelease(ctx context.Context, trackIDs []string) (string, error) {
	if len(trackIDs) == 0 {
		return "", domainerr.Required("track_ids")
	}
	var firstTrack model.Track
	if err := s.db.WithContext(ctx).Where("id = ?", trackIDs[0]).First(&firstTrack).Error; err != nil {
		return "", missingAs(err, trackNotFound())
	}
	// Validate all track IDs are valid UUIDs to prevent injection
	for _, trackID := range trackIDs {
		if _, err := uuid.Parse(trackID); err != nil {
			return "", domainerr.InvalidField("track_id", fmt.Sprintf("invalid: %s", trackID))
		}
	}
	var trackCount int64
	if err := s.db.WithContext(ctx).
		Model(&model.Track{}).
		Where("id IN ? AND release_id = ?", trackIDs, firstTrack.ReleaseID).
		Count(&trackCount).Error; err != nil {
		return "", err
	}
	if int(trackCount) != len(trackIDs) {
		return "", domainerr.InvalidField("track_ids", "all must belong to the same release")
	}
	// A partial reorder would leave the omitted Tracks' numbers ambiguous.
	var totalTracksInRelease int64
	if err := s.db.WithContext(ctx).
		Model(&model.Track{}).
		Where("release_id = ?", firstTrack.ReleaseID).
		Count(&totalTracksInRelease).Error; err != nil {
		return "", err
	}
	if int(totalTracksInRelease) != len(trackIDs) {
		return "", domainerr.InvalidField("track_ids", "all tracks in the release must be included for reordering")
	}
	return firstTrack.ReleaseID, nil
}

// reorderReleaseTracks renumbers the Tracks in two phases, negative and then
// positive, so the unique track number never collides midway.
func reorderReleaseTracks(tx *gorm.DB, releaseID string, trackIDs []string) (bool, error) {
	var currentIDs []string
	if err := tx.Model(&model.Track{}).Where("release_id = ?", releaseID).Order("track_number ASC").Pluck("id", &currentIDs).Error; err != nil {
		return false, err
	}
	if slices.Equal(currentIDs, trackIDs) {
		return false, nil
	}
	for i, trackID := range trackIDs {
		if err := tx.Exec("UPDATE track SET track_number = ? WHERE id = ? AND release_id = ?",
			-(i + 1), trackID, releaseID).Error; err != nil {
			return false, err
		}
	}
	if err := tx.Exec("UPDATE track SET track_number = -track_number WHERE track_number < 0 AND release_id = ?",
		releaseID).Error; err != nil {
		return false, err
	}
	return true, nil
}

// lockTrackRow locks the Track FOR UPDATE, answering not found before
// authorization when it is missing. into, when set, receives the row.
func lockTrackRow(ctx context.Context, tx *gorm.DB, trackID string, into *model.Track) error {
	var locked model.Track
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).First(&locked, "id = ?", trackID).Error; err != nil {
		return missingAs(err, trackNotFound())
	}
	if into != nil {
		*into = locked
	}
	return nil
}

func releaseTrackAudit(releaseID, itemID string, op sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackAuditRecord(m, releaseID, itemID, op)
	})
}

// trackMessage converts a model.Track to protobuf Track.
func trackMessage(ctx context.Context, t *model.Track) *managev1.Track {
	message := &managev1.Track{
		Id:                  t.ID,
		ReleaseId:           t.ReleaseID,
		TrackNumber:         int32(t.TrackNumber),
		Title:               t.Title,
		CreatedAt:           timestamppb.New(t.CreatedAt),
		AudioOriginalFileId: t.AudioOriginalFileID,
		ProcessingStatus:    t.ProcessingStatus,
		Lyrics:              t.Lyrics,
	}
	if t.DurationSeconds != nil {
		duration := int32(*t.DurationSeconds)
		message.DurationSeconds = &duration
	}
	if t.Metadata != nil {
		if metadata, err := structpb.NewStruct(t.Metadata); err == nil {
			message.Metadata = metadata
		} else {
			slog.ErrorContext(ctx, "Track metadata could not be converted; omitting it from the response", "domain", "release", "operation", "convert_track_metadata", failurecause.Attr(err), "track_id", t.ID)
		}
	}
	return message
}

// creditToProto converts a model.TrackCreditWithInfo to protobuf TrackCredit
func creditToProto(c *model.TrackCreditWithInfo, summaries map[string]*commonv1.MemberSummary) *managev1.TrackCredit {
	credit := &managev1.TrackCredit{
		Id:           c.ID,
		CreditType:   c.CreditType,
		SortOrder:    int32(c.SortOrder),
		ArtistId:     c.ArtistID,
		ArtistName:   c.ArtistName,
		ArtistSlug:   c.ArtistSlug,
		MemberId:     c.MemberID,
		CreditedName: c.CreditedName,
		CreditRole:   c.CreditRole,
	}
	if c.MemberID != nil {
		credit.Member = summaries[*c.MemberID]
	}
	return credit
}

// getCreditsForTrack retrieves credits with joined info for a track
func (s *Service) getCreditsForTrack(ctx context.Context, trackID string) ([]*managev1.TrackCredit, error) {
	var credits []model.TrackCreditWithInfo
	query := `
		SELECT
			tc.id,
			CASE
				WHEN tc.artist_id IS NOT NULL THEN 'artist'
				WHEN tc.member_id IS NOT NULL THEN 'member'
				ELSE 'text'
			END as credit_type,
			tc.artist_id,
			tc.member_id,
			tc.credited_name,
			tc.credit_role,
			tc.sort_order
		FROM track_credit tc
		WHERE tc.track_id = ?
		ORDER BY tc.sort_order ASC, tc.created_at ASC
	`
	if err := s.db.WithContext(ctx).Raw(query, trackID).Scan(&credits).Error; err != nil {
		return nil, err
	}

	memberIDs := make([]string, 0, len(credits))
	artistIDs := make([]string, 0, len(credits))
	for i := range credits {
		if credits[i].MemberID != nil {
			memberIDs = append(memberIDs, *credits[i].MemberID)
		}
		if credits[i].ArtistID != nil {
			artistIDs = append(artistIDs, *credits[i].ArtistID)
		}
	}
	summaries, err := s.members.LoadMemberSummaries(ctx, memberIDs)
	if err != nil {
		return nil, err
	}
	artists, err := s.artists.LoadArtistSummaries(ctx, artistIDs)
	if err != nil {
		return nil, err
	}
	result := make([]*managev1.TrackCredit, len(credits))
	for i := range credits {
		if credits[i].ArtistID != nil {
			if summary, ok := artists[*credits[i].ArtistID]; ok {
				credits[i].ArtistName = &summary.Title
				credits[i].ArtistSlug = &summary.Slug
			}
		}
		result[i] = creditToProto(&credits[i], summaries)
	}
	return result, nil
}
