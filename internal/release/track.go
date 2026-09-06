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

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/authzmutation"
	"github.com/echovisionlab/geul-api/internal/domainaudit"
	errs "github.com/echovisionlab/geul-api/internal/errors"
	"github.com/echovisionlab/geul-api/internal/model"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

// ErrTrackUploadSessionsChanged keeps Track deletion behind the same locked
// upload-session fence used by a Release cascade.
var ErrTrackUploadSessionsChanged = errors.New("track upload sessions changed during deletion")

const MaxTrackUploadCleanupPasses = 3

// TrackService implements the TrackService RPC service
type TrackService struct {
	managev1connect.UnimplementedTrackServiceHandler
	db           *gorm.DB
	transcodes   TrackTranscodes
	waveformJobs WaveformJobs
	spiceDB      *auth.SpiceDBClient
	fileService  TrackFileManager
	members      MemberSummaryLoader
	artists      ArtistSummaryLoader
	auditWriter  domainaudit.Appender
}

func NewAuditedTrackService(db *gorm.DB, transcodes TrackTranscodes, waveformJobs WaveformJobs, spiceDB *auth.SpiceDBClient, fileService TrackFileManager, members MemberSummaryLoader, artists ArtistSummaryLoader, auditWriter domainaudit.Appender) *TrackService {
	if auditWriter == nil {
		panic("TrackService: audit writer is required")
	}
	s := NewTrackService(db, transcodes, waveformJobs, spiceDB, fileService, members, artists)
	s.auditWriter = auditWriter
	return s
}

// NewTrackService creates a new TrackService
func NewTrackService(db *gorm.DB, transcodes TrackTranscodes, waveformJobs WaveformJobs, spiceDB *auth.SpiceDBClient, fileService TrackFileManager, members MemberSummaryLoader, artists ArtistSummaryLoader) *TrackService {
	if db == nil {
		panic("db is required")
	}
	if spiceDB == nil {
		panic("spiceDB is required")
	}
	if fileService == nil {
		panic("fileService is required")
	}
	if transcodes == nil {
		panic("transcodes is required")
	}
	if waveformJobs == nil {
		panic("waveformJobs is required")
	}
	if members == nil {
		panic("members is required")
	}
	if artists == nil {
		panic("artists is required")
	}
	return &TrackService{db: db, transcodes: transcodes, waveformJobs: waveformJobs, spiceDB: spiceDB, fileService: fileService, members: members, artists: artists}
}

func (s *TrackService) cleanupTrackUploadSessions(ctx context.Context, trackID string) error {
	if trackID == "" {
		return nil
	}
	return s.fileService.CleanupTrackUploadSessions(ctx, trackID, "Track deleted")
}

func (s *TrackService) deleteTrackWhenNoUploadSessions(
	ctx context.Context,
	trackID string,
) error {
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		if err := lockTrackForUploadMutation(tx, trackID); err != nil {
			return err
		}
		var track model.Track
		if err := tx.First(&track, "id = ?", trackID).Error; err != nil {
			return err
		}
		if err := s.fileService.RequireNoTrackUploadSessionsWithDB(ctx, tx, trackID); err != nil {
			return err
		}
		result := tx.Where("id = ?", trackID).Delete(&model.Track{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		deletePolicy, err := policyv1.Track.DeletePolicy(track.ID)
		if err != nil {
			return err
		}
		touchPolicy, err := policyv1.Track.TouchPolicy(track.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{deletePolicy},
			[]policyv1.RelationshipMutation{touchPolicy},
		); err != nil {
			return err
		}
		if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, track.ID, sharedtelemetry.AuditItemOperationDeleted); err != nil {
			return err
		}
		return nil
	})
	return err
}

// toProto converts a model.Track to protobuf Track
func (s *TrackService) toProto(t *model.Track) *managev1.Track {
	proto := &managev1.Track{
		Id:          t.ID,
		ReleaseId:   t.ReleaseID,
		TrackNumber: int32(t.TrackNumber),
		Title:       t.Title,
		CreatedAt:   timestamppb.New(t.CreatedAt),
	}

	if t.DurationSeconds != nil {
		v := int32(*t.DurationSeconds)
		proto.DurationSeconds = &v
	}
	if t.AudioOriginalFileID != nil {
		proto.AudioOriginalFileId = t.AudioOriginalFileID
	}
	if t.ProcessingStatus != nil {
		proto.ProcessingStatus = t.ProcessingStatus
	}
	if t.Lyrics != nil {
		proto.Lyrics = t.Lyrics
	}

	// Convert metadata to protobuf Struct
	if t.Metadata != nil {
		if metadata, err := structpb.NewStruct(t.Metadata); err == nil {
			proto.Metadata = metadata
		}
	}

	return proto
}

// creditToProto converts a model.TrackCreditWithInfo to protobuf TrackCredit
func (s *TrackService) creditToProto(c *model.TrackCreditWithInfo, summaries map[string]*commonv1.MemberSummary) *managev1.TrackCredit {
	proto := &managev1.TrackCredit{
		Id:         c.ID,
		CreditType: c.CreditType,
		SortOrder:  int32(c.SortOrder),
	}

	if c.ArtistID != nil {
		proto.ArtistId = c.ArtistID
	}
	if c.ArtistName != nil {
		proto.ArtistName = c.ArtistName
	}
	if c.ArtistSlug != nil {
		proto.ArtistSlug = c.ArtistSlug
	}
	if c.MemberID != nil {
		proto.MemberId = c.MemberID
		proto.Member = summaries[*c.MemberID]
	}
	if c.CreditedName != nil {
		proto.CreditedName = c.CreditedName
	}
	if c.CreditRole != nil {
		proto.CreditRole = c.CreditRole
	}

	return proto
}

// getCreditsForTrack retrieves credits with joined info for a track
func (s *TrackService) getCreditsForTrack(ctx context.Context, trackID string) ([]*managev1.TrackCredit, error) {
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
		result[i] = s.creditToProto(&credits[i], summaries)
	}

	return result, nil
}

// ListTracksByRelease lists all tracks for a release with credits for a Site Admin.
func (s *TrackService) ListTracksByRelease(
	ctx context.Context,
	req *connect.Request[managev1.ListTracksByReleaseRequest],
) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	if req.Msg.ReleaseId == "" {
		return nil, errs.Required("release_id")
	}
	if err := requireReleaseAction(ctx, s.spiceDB, req.Msg.ReleaseId, releaseActionView); err != nil {
		return nil, err
	}

	var tracks []model.Track
	if err := s.db.WithContext(ctx).
		Where("release_id = ?", req.Msg.ReleaseId).
		Order("track_number ASC").
		Find(&tracks).Error; err != nil {
		return nil, errs.Internal(err)
	}

	result := make([]*managev1.TrackWithCredits, len(tracks))
	for i := range tracks {
		credits, err := s.getCreditsForTrack(ctx, tracks[i].ID)
		if err != nil {
			return nil, errs.Internal(err)
		}
		result[i] = &managev1.TrackWithCredits{
			Track:   s.toProtoForViewer(ctx, &tracks[i]),
			Credits: credits,
		}
	}

	return connect.NewResponse(&managev1.ListTracksByReleaseResponse{
		Tracks: result,
	}), nil
}

// CreateTrack creates a new track (Site Admin only).
func (s *TrackService) CreateTrack(
	ctx context.Context,
	req *connect.Request[managev1.CreateTrackRequest],
) (*connect.Response[managev1.Track], error) {
	if req.Msg.ReleaseId == "" {
		return nil, errs.Required("release_id")
	}
	if req.Msg.Title == "" {
		return nil, errs.Required("title")
	}

	// Verify release exists first (before permission check to maintain consistent error ordering)
	var releaseCount int64
	if err := s.db.WithContext(ctx).Table("release").Where("id = ?", req.Msg.ReleaseId).Count(&releaseCount).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if releaseCount == 0 {
		return nil, errs.NotFoundMsg("release not found")
	}

	var track model.Track

	// Use transaction with row-level lock to prevent race condition
	_, err := authzmutation.Execute(ctx, s.db, s.spiceDB, func(
		tx *gorm.DB,
		write authzmutation.WriteRelationships,
	) error {
		// Lock the release row to serialize concurrent track insertions.
		if _, err := lockReleaseForUpdate(ctx, tx, req.Msg.ReleaseId); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, req.Msg.ReleaseId, releaseActionManage); err != nil {
			return err
		}
		// Get max track_number (release is locked, so safe from race conditions)
		var maxTrackNumber int
		if err := tx.Raw(`
			SELECT COALESCE(MAX(track_number), 0) FROM track
			WHERE release_id = $1`, req.Msg.ReleaseId).Scan(&maxTrackNumber).Error; err != nil {
			return err
		}

		track = model.Track{
			ReleaseID:   req.Msg.ReleaseId,
			TrackNumber: maxTrackNumber + 1,
			Title:       req.Msg.Title,
			Metadata:    make(model.TrackMetadata),
		}

		if req.Msg.DurationSeconds != nil {
			v := int(*req.Msg.DurationSeconds)
			track.DurationSeconds = &v
		}
		if req.Msg.Lyrics != nil {
			track.Lyrics = req.Msg.Lyrics
		}
		if err := tx.Clauses(clause.Returning{}).Create(&track).Error; err != nil {
			return err
		}
		touchPolicy, err := policyv1.Track.TouchPolicy(track.ID)
		if err != nil {
			return err
		}
		deletePolicy, err := policyv1.Track.DeletePolicy(track.ID)
		if err != nil {
			return err
		}
		if err := write(
			[]policyv1.RelationshipMutation{touchPolicy},
			[]policyv1.RelationshipMutation{deletePolicy},
		); err != nil {
			return err
		}
		if err := s.appendReleaseTrackAudit(ctx, tx, track.ReleaseID, track.ID, sharedtelemetry.AuditItemOperationCreated); err != nil {
			return err
		}
		return nil
	})

	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}
	return connect.NewResponse(s.toProto(&track)), nil
}

// UpdateTrack updates an existing track (Site Admin only).
func (s *TrackService) UpdateTrack(
	ctx context.Context,
	req *connect.Request[managev1.UpdateTrackRequest],
) (*connect.Response[managev1.Track], error) {
	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	var track model.Track
	if err := s.db.WithContext(ctx).Where("id = ?", req.Msg.Id).First(&track).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("track not found")
		}
		return nil, errs.Internal(err)
	}

	// Update fields if provided
	next := track
	if req.Msg.TrackNumber != nil {
		next.TrackNumber = int(*req.Msg.TrackNumber)
	}
	if req.Msg.Title != nil && *req.Msg.Title != "" {
		next.Title = *req.Msg.Title
	}
	if req.Msg.DurationSeconds != nil {
		v := int(*req.Msg.DurationSeconds)
		next.DurationSeconds = &v
	}
	if req.Msg.ClearDuration {
		next.DurationSeconds = nil
	}
	if req.Msg.ClearAudioOriginal {
		next.AudioOriginalFileID = nil
	}
	if req.Msg.ProcessingStatus != nil {
		next.ProcessingStatus = req.Msg.ProcessingStatus
	}
	if req.Msg.Lyrics != nil {
		next.Lyrics = req.Msg.Lyrics
	}
	if req.Msg.ClearLyrics {
		next.Lyrics = nil
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTrackDownloadAudienceSegments(ctx, tx, track.ID); err != nil {
			return err
		}
		var locked model.Track
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", track.ID).Error; err != nil {
			return err
		}
		if err := requireActiveTrackAction(ctx, tx, s.spiceDB, track.ID, trackActionEdit); err != nil {
			return err
		}
		// A concurrent update is serialized by the lock; apply the request to its current row.
		next = locked
		if req.Msg.TrackNumber != nil {
			next.TrackNumber = int(*req.Msg.TrackNumber)
		}
		if req.Msg.Title != nil && *req.Msg.Title != "" {
			next.Title = *req.Msg.Title
		}
		if req.Msg.DurationSeconds != nil {
			v := int(*req.Msg.DurationSeconds)
			next.DurationSeconds = &v
		}
		if req.Msg.ClearDuration {
			next.DurationSeconds = nil
		}
		if req.Msg.ClearAudioOriginal {
			next.AudioOriginalFileID = nil
		}
		if req.Msg.ProcessingStatus != nil {
			next.ProcessingStatus = req.Msg.ProcessingStatus
		}
		if req.Msg.Lyrics != nil {
			next.Lyrics = req.Msg.Lyrics
		}
		if req.Msg.ClearLyrics {
			next.Lyrics = nil
		}
		if reflect.DeepEqual(locked, next) {
			track = locked
			return nil
		}
		oldFileID := locked.AudioOriginalFileID
		if err := tx.Save(&next).Error; err != nil {
			return err
		}
		if !sameOptionalString(oldFileID, next.AudioOriginalFileID) {
			if err := tx.Model(&model.Track{}).Where("id = ?", next.ID).
				Update("download_audience", "disabled").Error; err != nil {
				return err
			}
			if err := tx.Where("track_id = ?", next.ID).
				Delete(&model.TrackDownloadAudienceSegment{}).Error; err != nil {
				return err
			}
			next.DownloadAudience = "disabled"
		}
		track = next
		metadataBefore, metadataAfter := locked, next
		metadataBefore.AudioOriginalFileID, metadataAfter.AudioOriginalFileID = nil, nil
		if !reflect.DeepEqual(metadataBefore, metadataAfter) {
			if err := s.appendReleaseTrackAudit(ctx, tx, next.ReleaseID, next.ID, sharedtelemetry.AuditItemOperationUpdated); err != nil {
				return err
			}
		}
		if oldFileID != nil && next.AudioOriginalFileID == nil {
			return s.appendReleaseTrackAudioAudit(ctx, tx, next.ReleaseID, next.ID, *oldFileID, sharedtelemetry.AuditCollectionOperationRemoved)
		}
		return nil
	}); err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(s.toProto(&track)), nil
}

// DeleteTrack deletes a track (Site Admin only).
func (s *TrackService) DeleteTrack(
	ctx context.Context,
	req *connect.Request[managev1.DeleteTrackRequest],
) (*connect.Response[managev1.DeleteResponse], error) {
	if req.Msg.Id == "" {
		return nil, errs.Required("id")
	}

	// First fetch the track to get the release_id for permission check
	var track model.Track
	if err := s.db.WithContext(ctx).Where("id = ?", req.Msg.Id).First(&track).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("track not found")
		}
		return nil, errs.Internal(err)
	}

	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var locked model.Track
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).First(&locked, "id = ?", track.ID).Error; err != nil {
			return err
		}
		return requireActiveTrackAction(ctx, tx, s.spiceDB, track.ID, trackActionDelete)
	}); err != nil {
		return nil, err
	}
	deleted := false
	for range MaxTrackUploadCleanupPasses {
		if err := s.cleanupTrackUploadSessions(ctx, track.ID); err != nil {
			if errors.Is(err, ErrTrackUploadSessionNotAbortable) {
				return nil, errs.FailedPrecondition("track audio upload is finalizing; retry its completion before deleting the track")
			}
			return nil, errs.Internal(fmt.Errorf("cleanup track audio uploads: %w", err))
		}
		err := s.deleteTrackWhenNoUploadSessions(ctx, track.ID)
		if err != nil {
			if errors.Is(err, ErrTrackUploadSessionsChanged) {
				continue
			}
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, errs.NotFoundMsg("track not found")
			}
			if connect.CodeOf(err) != connect.CodeUnknown {
				return nil, err
			}
			return nil, errs.Internal(err)
		}
		deleted = true
		break
	}
	if !deleted {
		return nil, errs.FailedPrecondition("track audio uploads changed during deletion; retry the request")
	}
	if err := s.waveformJobs.CancelTracks(
		ctx,
		[]string{track.ID},
		managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
	); err != nil {
		slog.Warn("Failed to cancel waveform jobs for deleted track", "trackId", track.ID, "error", err)
	}
	s.transcodes.CancelFiles(
		ctx,
		track.ID,
		managev1.TranscodeCancelReason_TRANSCODE_CANCEL_REASON_USER_DELETED,
		track.AudioOriginalFileID,
	)
	return connect.NewResponse(&managev1.DeleteResponse{
		Success: true,
	}), nil
}

// SetTrackCredits replaces all credits for a track (Site Admin only).
func (s *TrackService) SetTrackCredits(
	ctx context.Context,
	req *connect.Request[managev1.SetTrackCreditsRequest],
) (*connect.Response[managev1.TrackWithCredits], error) {
	if req.Msg.TrackId == "" {
		return nil, errs.Required("track_id")
	}

	// Verify track exists
	var track model.Track
	if err := s.db.WithContext(ctx).Where("id = ?", req.Msg.TrackId).First(&track).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("track not found")
		}
		return nil, errs.Internal(err)
	}

	if err := s.validateTrackCreditArtists(ctx, req.Msg.Credits); err != nil {
		return nil, err
	}
	err := s.replaceTrackCredits(ctx, req.Msg.TrackId, req.Msg.Credits)
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	// Fetch updated credits
	credits, err := s.getCreditsForTrack(ctx, req.Msg.TrackId)
	if err != nil {
		return nil, errs.Internal(err)
	}

	return connect.NewResponse(&managev1.TrackWithCredits{
		Track:   s.toProto(&track),
		Credits: credits,
	}), nil
}

// ReorderTracks reorders tracks by updating their track_number (Site Admin only).
func (s *TrackService) ReorderTracks(
	ctx context.Context,
	req *connect.Request[managev1.ReorderTracksRequest],
) (*connect.Response[managev1.ListTracksByReleaseResponse], error) {
	if len(req.Msg.TrackIds) == 0 {
		return nil, errs.Required("track_ids")
	}

	// Get release_id from first track
	var firstTrack model.Track
	if err := s.db.WithContext(ctx).Where("id = ?", req.Msg.TrackIds[0]).First(&firstTrack).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errs.NotFoundMsg("track not found")
		}
		return nil, errs.Internal(err)
	}

	// Validate all track IDs are valid UUIDs to prevent injection
	for _, trackID := range req.Msg.TrackIds {
		if _, err := uuid.Parse(trackID); err != nil {
			return nil, errs.InvalidArgument("track_id", fmt.Sprintf("invalid: %s", trackID))
		}
	}

	// Verify all track IDs belong to the same release
	var trackCount int64
	if err := s.db.WithContext(ctx).
		Model(&model.Track{}).
		Where("id IN ? AND release_id = ?", req.Msg.TrackIds, firstTrack.ReleaseID).
		Count(&trackCount).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if int(trackCount) != len(req.Msg.TrackIds) {
		return nil, errs.InvalidArgument("track_ids", "all must belong to the same release")
	}

	// Verify ALL tracks in the release are included (no partial reorder)
	var totalTracksInRelease int64
	if err := s.db.WithContext(ctx).
		Model(&model.Track{}).
		Where("release_id = ?", firstTrack.ReleaseID).
		Count(&totalTracksInRelease).Error; err != nil {
		return nil, errs.Internal(err)
	}
	if int(totalTracksInRelease) != len(req.Msg.TrackIds) {
		return nil, errs.InvalidArgument("track_ids", "all tracks in the release must be included for reordering")
	}

	// Two-phase update to avoid unique constraint violations:
	// 1. Set all to negative values
	// 2. Flip to positive values
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockReleaseForUpdate(ctx, tx, firstTrack.ReleaseID); err != nil {
			return err
		}
		if err := requireActiveReleaseAction(ctx, tx, s.spiceDB, firstTrack.ReleaseID, releaseActionManage); err != nil {
			return err
		}
		var currentIDs []string
		if err := tx.Model(&model.Track{}).Where("release_id = ?", firstTrack.ReleaseID).Order("track_number ASC").Pluck("id", &currentIDs).Error; err != nil {
			return err
		}
		if slices.Equal(currentIDs, req.Msg.TrackIds) {
			return nil
		}
		// Phase 1: Set to negative values using parameterized query
		// We iterate through track IDs and update each one with its new position
		for i, trackID := range req.Msg.TrackIds {
			newOrder := -(i + 1) // Negative to avoid constraint violations
			if err := tx.Exec("UPDATE track SET track_number = ? WHERE id = ? AND release_id = ?",
				newOrder, trackID, firstTrack.ReleaseID).Error; err != nil {
				return err
			}
		}

		// Phase 2: Flip negative to positive for this release only
		if err := tx.Exec("UPDATE track SET track_number = -track_number WHERE track_number < 0 AND release_id = ?",
			firstTrack.ReleaseID).Error; err != nil {
			return err
		}

		return s.appendReleaseTrackOrderAudit(ctx, tx, firstTrack.ReleaseID, req.Msg.TrackIds)
	})

	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, errs.Internal(err)
	}

	// Fetch updated tracks
	var tracks []model.Track
	if err := s.db.WithContext(ctx).
		Where("release_id = ?", firstTrack.ReleaseID).
		Order("track_number ASC").
		Find(&tracks).Error; err != nil {
		return nil, errs.Internal(err)
	}

	result := make([]*managev1.TrackWithCredits, len(tracks))
	for i := range tracks {
		credits, err := s.getCreditsForTrack(ctx, tracks[i].ID)
		if err != nil {
			return nil, errs.Internal(err)
		}
		result[i] = &managev1.TrackWithCredits{
			Track:   s.toProto(&tracks[i]),
			Credits: credits,
		}
	}

	return connect.NewResponse(&managev1.ListTracksByReleaseResponse{
		Tracks: result,
	}), nil
}

// toProtoForViewer converts a model.Track to protobuf Track.
func (s *TrackService) toProtoForViewer(ctx context.Context, t *model.Track) *managev1.Track {
	proto := &managev1.Track{
		Id:          t.ID,
		ReleaseId:   t.ReleaseID,
		TrackNumber: int32(t.TrackNumber),
		Title:       t.Title,
		CreatedAt:   timestamppb.New(t.CreatedAt),
	}

	if t.DurationSeconds != nil {
		v := int32(*t.DurationSeconds)
		proto.DurationSeconds = &v
	}

	if t.AudioOriginalFileID != nil {
		proto.AudioOriginalFileId = t.AudioOriginalFileID
	}

	if t.ProcessingStatus != nil {
		proto.ProcessingStatus = t.ProcessingStatus
	}
	if t.Lyrics != nil {
		proto.Lyrics = t.Lyrics
	}

	// Convert metadata to protobuf Struct
	if t.Metadata != nil {
		if metadata, err := structpb.NewStruct(t.Metadata); err == nil {
			proto.Metadata = metadata
		}
	}

	return proto
}
