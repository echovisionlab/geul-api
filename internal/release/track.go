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

	"github.com/echovisionlab/geul-api/internal/domainerr"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	"github.com/echovisionlab/geul-api/internal/memberref"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/google/uuid"

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

// TrackCreditView is a Track credit with its Artist's name and slug and its
// Member's summary.
type TrackCreditView struct {
	CreditRow
	ArtistName *string
	ArtistSlug *string
	Member     *memberref.Summary
}

// TrackWithCredits is a Track with its credits in order.
type TrackWithCredits struct {
	Track   Track
	Credits []TrackCreditView
}

// ListTracksByRelease lists all tracks for a release with credits for a Site Admin.
func (s *Service) ListTracksByRelease(ctx context.Context, query ListTracksQuery) ([]TrackWithCredits, error) {
	if query.ReleaseID == "" {
		return nil, domainerr.Required("release_id")
	}
	if err := requireReleaseAction(ctx, s.spiceDB, query.ReleaseID, releaseActionView); err != nil {
		return nil, err
	}
	return s.releaseTracksWithCredits(ctx, query.ReleaseID)
}

func (s *Service) releaseTracksWithCredits(ctx context.Context, releaseID string) ([]TrackWithCredits, error) {
	var tracks []Track
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		tracks, err = store.Tracks(ctx, releaseID)
		return err
	}); err != nil {
		return nil, err
	}
	result := make([]TrackWithCredits, len(tracks))
	for i := range tracks {
		credits, err := s.getCreditsForTrack(ctx, tracks[i].ID)
		if err != nil {
			return nil, err
		}
		result[i] = TrackWithCredits{Track: tracks[i], Credits: credits}
	}
	return result, nil
}

// CreateTrack appends a track to a release (Release managers).
func (s *Service) CreateTrack(ctx context.Context, command CreateTrackCommand) (Track, error) {
	if command.ReleaseID == "" {
		return Track{}, domainerr.Required("release_id")
	}
	if command.Title == "" {
		return Track{}, domainerr.Required("title")
	}
	track, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[Track, Store]{
		// The Release lock also serializes concurrent track numbering.
		Lock: func(ctx context.Context, store Store) error {
			_, err := store.LockRelease(ctx, command.ReleaseID, UpdateLock)
			return releaseMissingAs(err, notFound("release not found"))
		},
		Access: s.releaseAccess(command.ReleaseID, releaseActionManage),
		Change: func(ctx context.Context, store Store) (Track, error) {
			trackNumber, err := store.NextTrackNumber(ctx, command.ReleaseID)
			if err != nil {
				return Track{}, err
			}
			track := Track{
				ReleaseID:   command.ReleaseID,
				TrackNumber: trackNumber,
				Title:       command.Title,
				Metadata:    map[string]any{},
				Lyrics:      command.Lyrics,
			}
			if command.DurationSeconds != nil {
				duration := int(*command.DurationSeconds)
				track.DurationSeconds = &duration
			}
			if err := store.InsertTrack(ctx, &track); err != nil {
				return Track{}, err
			}
			return track, nil
		},
		Audit: func(track Track) mutation.Audit {
			return releaseTrackAudit(track.ReleaseID, track.ID, sharedtelemetry.AuditItemOperationCreated)
		},
		Relationships: func(track Track) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
			touchPolicy, err := policyv1.Track.TouchPolicy(track.ID)
			if err != nil {
				return nil, nil, err
			}
			deletePolicy, err := policyv1.Track.DeletePolicy(track.ID)
			return []policyv1.RelationshipMutation{touchPolicy}, []policyv1.RelationshipMutation{deletePolicy}, err
		},
	})
	if err != nil {
		return Track{}, err
	}
	return track, nil
}

// trackUpdate is a Track before and after an update.
type trackUpdate struct {
	before, after Track
}

func (u trackUpdate) changed() bool {
	return !reflect.DeepEqual(u.before, u.after)
}

// UpdateTrack updates an existing track (Track editors).
func (s *Service) UpdateTrack(ctx context.Context, command UpdateTrackCommand) (Track, error) {
	if command.ID == "" {
		return Track{}, domainerr.Required("id")
	}
	var locked Track
	update, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[trackUpdate, Store]{
		Lock: func(ctx context.Context, store Store) error {
			if err := store.LockTrackDownloadCriteriaSet(ctx, command.ID); err != nil {
				return err
			}
			return lockTrackRow(ctx, store, command.ID, &locked)
		},
		Access: s.trackAccess(command.ID, trackActionEdit),
		Change: func(ctx context.Context, store Store) (trackUpdate, error) {
			// A concurrent update is serialized by the lock; apply the request to its current row.
			update := trackUpdate{before: locked, after: command.apply(locked)}
			if !update.changed() {
				return update, nil
			}
			if err := saveTrackUpdate(ctx, store, &update); err != nil {
				return trackUpdate{}, err
			}
			return update, nil
		},
		Audit: trackUpdateAudit,
	})
	if err != nil {
		return Track{}, err
	}
	return update.after, nil
}

func (command UpdateTrackCommand) apply(track Track) Track {
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
// a different download, so its access starts disabled again.
func saveTrackUpdate(ctx context.Context, store Store, update *trackUpdate) error {
	if err := store.SaveTrack(ctx, update.after); err != nil {
		return err
	}
	if sameOptionalString(update.before.AudioOriginalFileID, update.after.AudioOriginalFileID) {
		return nil
	}
	if err := store.DisableTrackDownload(ctx, update.after.ID); err != nil {
		return err
	}
	update.after.DownloadAccess = DownloadAccessDisabled
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
	return mutation.Audits(records...)
}

// DeleteTrack deletes a track (Track editors with delete permission).
func (s *Service) DeleteTrack(ctx context.Context, command DeleteTrackCommand) error {
	if command.ID == "" {
		return domainerr.Required("id")
	}
	// Upload cleanup aborts uploads outside any transaction, so the caller's
	// authority is decided under the Track lock before it runs.
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[struct{}, Store]{
		Lock: func(ctx context.Context, store Store) error {
			return lockTrackRow(ctx, store, command.ID, nil)
		},
		Access: s.trackAccess(command.ID, trackActionDelete),
		Change: func(context.Context, Store) (struct{}, error) { return struct{}{}, nil },
		Audit:  func(struct{}) mutation.Audit { return mutation.Unchanged() },
	}); err != nil {
		return err
	}
	deleted, err := s.deleteTrackAfterUploadCleanup(ctx, command.ID)
	if err != nil {
		return err
	}
	if err := s.waveformJobs.CancelDeletedTracks(ctx, []string{deleted.ID}); err != nil {
		slog.ErrorContext(ctx, "Failed to cancel waveform jobs for deleted track", "trackId", deleted.ID, failurecause.Attr(err))
	}
	s.transcodes.CancelDeletedTrackFiles(ctx, deleted.ID, deleted.AudioOriginalFileID)
	return nil
}

// deleteTrackAfterUploadCleanup cleans up the Track's audio uploads and
// deletes it, cleaning up again when an upload started in between.
func (s *Service) deleteTrackAfterUploadCleanup(ctx context.Context, trackID string) (Track, error) {
	var uploadsChanged error
	for range MaxTrackUploadCleanupPasses {
		if err := s.trackFiles.CleanupTrackUploadSessions(ctx, trackID, "Track deleted"); err != nil {
			if errors.Is(err, ErrTrackUploadSessionNotAbortable) {
				return Track{}, domainerr.Precondition("track audio upload is finalizing; retry its completion before deleting the track").WithCause(err)
			}
			return Track{}, fmt.Errorf("cleanup track audio uploads: %w", err)
		}
		deleted, err := s.deleteTrackWhenNoUploadSessions(ctx, trackID)
		if errors.Is(err, ErrTrackUploadSessionsChanged) {
			uploadsChanged = err
			continue
		}
		return deleted, missingAs(err, ErrTrackNotFound, trackNotFound())
	}
	return Track{}, domainerr.Precondition("track audio uploads changed during deletion; retry the request").WithCause(uploadsChanged)
}

func (s *Service) deleteTrackWhenNoUploadSessions(ctx context.Context, trackID string) (Track, error) {
	var track Track
	return mutation.Run(ctx, s.mutations, mutation.StoreSpec[Track, Store]{
		Lock: func(ctx context.Context, store Store) error {
			if err := store.LockTrackID(ctx, trackID); err != nil {
				return err
			}
			var err error
			track, err = store.FindTrack(ctx, trackID)
			return err
		},
		Access: s.trackAccess(trackID, trackActionDelete),
		Change: func(ctx context.Context, store Store) (Track, error) {
			if err := store.RequireNoTrackUploadSessions(ctx, trackID); err != nil {
				return Track{}, err
			}
			if err := store.DeleteTrack(ctx, trackID); err != nil {
				return Track{}, err
			}
			return track, nil
		},
		Audit: func(track Track) mutation.Audit {
			return releaseTrackAudit(track.ReleaseID, track.ID, sharedtelemetry.AuditItemOperationDeleted)
		},
		Relationships: func(track Track) ([]policyv1.RelationshipMutation, []policyv1.RelationshipMutation, error) {
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
func (s *Service) ReorderTracks(ctx context.Context, command ReorderTracksCommand) ([]TrackWithCredits, error) {
	releaseID, err := s.trackOrderRelease(ctx, command.TrackIDs)
	if err != nil {
		return nil, err
	}
	if _, err := mutation.Run(ctx, s.mutations, mutation.StoreSpec[bool, Store]{
		Lock:   lockReleaseRow(releaseID, nil),
		Access: s.releaseAccess(releaseID, releaseActionManage),
		Change: func(ctx context.Context, store Store) (bool, error) {
			return reorderReleaseTracks(ctx, store, releaseID, command.TrackIDs)
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
	var releaseID string
	err := s.units.Read(ctx, func(store Store) error {
		firstTrack, err := store.FindTrack(ctx, trackIDs[0])
		if err != nil {
			return missingAs(err, ErrTrackNotFound, trackNotFound())
		}
		// Validate all track IDs are valid UUIDs to prevent injection
		for _, trackID := range trackIDs {
			if _, err := uuid.Parse(trackID); err != nil {
				return domainerr.InvalidField("track_id", fmt.Sprintf("invalid: %s", trackID))
			}
		}
		trackCount, err := store.CountTracksOf(ctx, firstTrack.ReleaseID, trackIDs)
		if err != nil {
			return err
		}
		if int(trackCount) != len(trackIDs) {
			return domainerr.InvalidField("track_ids", "all must belong to the same release")
		}
		// A partial reorder would leave the omitted Tracks' numbers ambiguous.
		totalTracksInRelease, err := store.CountReleaseTracks(ctx, firstTrack.ReleaseID)
		if err != nil {
			return err
		}
		if int(totalTracksInRelease) != len(trackIDs) {
			return domainerr.InvalidField("track_ids", "all tracks in the release must be included for reordering")
		}
		releaseID = firstTrack.ReleaseID
		return nil
	})
	return releaseID, err
}

// reorderReleaseTracks renumbers the Tracks in two phases, negative and then
// positive, so the unique track number never collides midway.
func reorderReleaseTracks(ctx context.Context, store Store, releaseID string, trackIDs []string) (bool, error) {
	currentIDs, err := store.TrackOrder(ctx, releaseID)
	if err != nil {
		return false, err
	}
	if slices.Equal(currentIDs, trackIDs) {
		return false, nil
	}
	return true, store.RenumberTracks(ctx, releaseID, trackIDs)
}

func releaseTrackAudit(releaseID, itemID string, op sharedtelemetry.AuditItemOperation) mutation.Audit {
	return mutation.RequestAudit(sharedtelemetry.AuditReleaseUpdated, func(m sharedtelemetry.AuditMetadata) (sharedtelemetry.AuditRecord, error) {
		return sharedtelemetry.NewReleaseTrackAuditRecord(m, releaseID, itemID, op)
	})
}

// getCreditsForTrack retrieves credits with joined info for a track
func (s *Service) getCreditsForTrack(ctx context.Context, trackID string) ([]TrackCreditView, error) {
	var credits []CreditRow
	if err := s.units.Read(ctx, func(store Store) error {
		var err error
		credits, err = store.TrackCreditRows(ctx, trackID)
		return err
	}); err != nil {
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
	artists, err := s.readArtistSummaries(ctx, artistIDs)
	if err != nil {
		return nil, err
	}
	result := make([]TrackCreditView, len(credits))
	for i := range credits {
		result[i] = TrackCreditView{CreditRow: credits[i]}
		if credits[i].ArtistID != nil {
			if summary, ok := artists[*credits[i].ArtistID]; ok {
				result[i].ArtistName = &summary.Title
				result[i].ArtistSlug = &summary.Slug
			}
		}
		if credits[i].MemberID != nil {
			result[i].Member = summaries[*credits[i].MemberID]
		}
	}
	return result, nil
}

// readArtistSummaries reads the Artist summaries in their own read
// snapshot.
func (s *Service) readArtistSummaries(ctx context.Context, artistIDs []string) (map[string]ArtistSummary, error) {
	var summaries map[string]ArtistSummary
	err := s.units.Read(ctx, func(store Store) error {
		var err error
		summaries, err = store.ArtistSummaries(ctx, artistIDs)
		return err
	})
	return summaries, err
}
