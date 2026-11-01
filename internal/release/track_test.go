package release_test

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/domainaudit/audittest"
	"github.com/echovisionlab/geul-api/internal/mutation"
	"github.com/echovisionlab/geul-api/internal/release"
	"github.com/echovisionlab/geul-api/internal/release/releasetest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	sharedtelemetry "github.com/echovisionlab/geul-telemetry"
)

func TestCreateTrackNumbersAfterTheLastTrack(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	h.seedTrack(t, value.ID, 4)
	duration := int32(210)
	track, err := h.service.CreateTrack(h.ctx(t), release.CreateTrackCommand{ReleaseID: value.ID, Title: "Next", DurationSeconds: &duration})
	require.NoError(t, err)
	require.Equal(t, 5, track.TrackNumber)
	require.Equal(t, 210, *track.DurationSeconds)
	require.NotNil(t, track.Metadata)
	touch, err := policyv1.Track.TouchPolicy(track.ID)
	require.NoError(t, err)
	require.Equal(t, [][]policyv1.RelationshipMutation{{touch}}, h.checker.Applied())
	require.Equal(t, sharedtelemetry.AuditItemOperationCreated, h.audit.Records()[0].ItemOperation)

	missing := uuid.NewString()
	_, err = h.service.CreateTrack(h.ctx(t), release.CreateTrackCommand{ReleaseID: missing, Title: "Orphan"})
	requireCode(t, err, connect.CodeNotFound, "release not found")
	_, err = h.service.CreateTrack(h.ctx(t), release.CreateTrackCommand{ReleaseID: value.ID})
	requireCode(t, err, connect.CodeInvalidArgument, "title is required")
}

func TestUpdateTrackResetsTheDownloadOfReplacedAudio(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	original := uuid.NewString()
	track := h.seedTrack(t, value.ID, 1, func(track *release.Track) {
		track.AudioOriginalFileID, track.DownloadAudience = &original, "restricted"
	})
	h.store.PutTrackSegments(track.ID, uuid.NewString())

	renamed, err := h.service.UpdateTrack(h.ctx(t), release.UpdateTrackCommand{ID: track.ID, Title: text("Renamed")})
	require.NoError(t, err)
	require.Equal(t, "restricted", renamed.DownloadAudience, "a metadata edit keeps the download policy")
	require.NotEmpty(t, h.store.TrackSegments(track.ID))

	cleared, err := h.service.UpdateTrack(h.ctx(t), release.UpdateTrackCommand{ID: track.ID, ClearAudioOriginal: true})
	require.NoError(t, err)
	require.Nil(t, cleared.AudioOriginalFileID)
	require.Equal(t, release.DownloadAudienceDisabled, cleared.DownloadAudience)
	require.Empty(t, h.store.TrackSegments(track.ID))
	// The rename, then the clear: its reset download policy is a metadata
	// change, and the removed original is a record of its own.
	records := h.audit.Records()
	require.Len(t, records, 3)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[0].ItemOperation)
	require.Equal(t, sharedtelemetry.AuditItemOperationUpdated, records[1].ItemOperation)
	require.Equal(t, original, records[2].FileID)
	require.Equal(t, sharedtelemetry.AuditCollectionOperationRemoved, records[2].CollectionOperation)

	_, err = h.service.UpdateTrack(h.ctx(t), release.UpdateTrackCommand{ID: track.ID, Title: text("Renamed")})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), 3, "an unchanged update appends nothing")
}

// A Track mutation locks the Track before it checks the caller's current
// authority, and a denial changes nothing.
func TestTrackMutationsRecheckAuthorityAfterTheTrackLock(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	track := h.seedTrack(t, value.ID, 1)
	h.checker.Deny(can(t, policyv1.Track.Edit, track.ID), nil)
	_, err := h.service.UpdateTrack(h.ctx(t), release.UpdateTrackCommand{ID: track.ID, Title: text("Denied")})
	requireCode(t, err, connect.CodePermissionDenied, "no permission to edit this track")
	require.Equal(t, []mutation.Step{mutation.StepLock, mutation.StepAccess, mutation.StepRollback}, h.units.Steps())
	stored, _ := h.store.Track(track.ID)
	require.Equal(t, "Track", stored.Title)

	_, err = h.service.UpdateTrack(h.ctx(t), release.UpdateTrackCommand{ID: uuid.NewString(), Title: text("Missing")})
	requireCode(t, err, connect.CodeNotFound, "track not found")
}

func TestDeleteTrack(t *testing.T) {
	t.Run("deletes, cancels its work and removes its policy", func(t *testing.T) {
		h := newHarness(t)
		value := h.seedRelease(t, "Album")
		file := uuid.NewString()
		track := h.seedTrack(t, value.ID, 1, func(track *release.Track) { track.AudioOriginalFileID = &file })
		require.NoError(t, h.service.DeleteTrack(h.ctx(t), release.DeleteTrackCommand{ID: track.ID}))
		_, exists := h.store.Track(track.ID)
		require.False(t, exists)
		require.Equal(t, [][]string{{track.ID}}, h.jobs.waveforms)
		require.Equal(t, []*string{&file}, h.jobs.files[track.ID])
		deleted, err := policyv1.Track.DeletePolicy(track.ID)
		require.NoError(t, err)
		require.Equal(t, [][]policyv1.RelationshipMutation{{deleted}}, h.checker.Applied())
		require.Equal(t, sharedtelemetry.AuditItemOperationDeleted, h.audit.Records()[0].ItemOperation)
	})
	t.Run("stops before any write while an upload is finalizing", func(t *testing.T) {
		h := newHarness(t)
		value := h.seedRelease(t, "Album")
		track := h.seedTrack(t, value.ID, 1)
		h.uploads.cleanup = func(string) error { return release.ErrTrackUploadSessionNotAbortable }
		err := h.service.DeleteTrack(h.ctx(t), release.DeleteTrackCommand{ID: track.ID})
		requireCode(t, err, connect.CodeFailedPrecondition, "track audio upload is finalizing; retry its completion before deleting the track")
		require.Equal(t, []string{track.ID}, h.uploads.cleanedTracks())
		_, exists := h.store.Track(track.ID)
		require.True(t, exists)
		require.Empty(t, h.checker.Applied())
	})
	t.Run("gives up when an upload keeps reappearing", func(t *testing.T) {
		h := newHarness(t)
		value := h.seedRelease(t, "Album")
		track := h.seedTrack(t, value.ID, 1)
		h.store.SetUploadSession(track.ID, true)
		err := h.service.DeleteTrack(h.ctx(t), release.DeleteTrackCommand{ID: track.ID})
		requireCode(t, err, connect.CodeFailedPrecondition, "track audio uploads changed during deletion; retry the request")
		require.Len(t, h.uploads.cleanedTracks(), release.MaxTrackUploadCleanupPasses)
		_, exists := h.store.Track(track.ID)
		require.True(t, exists)
	})
	t.Run("checks delete authority before cleanup", func(t *testing.T) {
		h := newHarness(t)
		value := h.seedRelease(t, "Album")
		track := h.seedTrack(t, value.ID, 1)
		h.checker.Deny(can(t, policyv1.Track.Delete, track.ID), nil)
		err := h.service.DeleteTrack(h.ctx(t), release.DeleteTrackCommand{ID: track.ID})
		requireCode(t, err, connect.CodePermissionDenied, "no permission to delete this track")
		require.Empty(t, h.uploads.cleanedTracks())
	})
}

func TestReorderTracks(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	other := h.seedRelease(t, "Other")
	first, second := h.seedTrack(t, value.ID, 1), h.seedTrack(t, value.ID, 2)
	foreign := h.seedTrack(t, other.ID, 1)
	ctx := h.ctx(t)

	_, err := h.service.ReorderTracks(ctx, release.ReorderTracksCommand{})
	requireCode(t, err, connect.CodeInvalidArgument, "track_ids is required")
	_, err = h.service.ReorderTracks(ctx, release.ReorderTracksCommand{TrackIDs: []string{first.ID, "not-a-uuid"}})
	requireCode(t, err, connect.CodeInvalidArgument, "track_id: invalid: not-a-uuid")
	_, err = h.service.ReorderTracks(ctx, release.ReorderTracksCommand{TrackIDs: []string{first.ID, foreign.ID}})
	requireCode(t, err, connect.CodeInvalidArgument, "track_ids: all must belong to the same release")
	_, err = h.service.ReorderTracks(ctx, release.ReorderTracksCommand{TrackIDs: []string{first.ID}})
	requireCode(t, err, connect.CodeInvalidArgument, "track_ids: all tracks in the release must be included for reordering")

	tracks, err := h.service.ReorderTracks(ctx, release.ReorderTracksCommand{TrackIDs: []string{second.ID, first.ID}})
	require.NoError(t, err)
	require.Equal(t, []string{second.ID, first.ID}, []string{tracks[0].Track.ID, tracks[1].Track.ID})
	require.Equal(t, []sharedtelemetry.AuditAction{sharedtelemetry.AuditReleaseUpdated}, auditActions(h.audit))

	_, err = h.service.ReorderTracks(ctx, release.ReorderTracksCommand{TrackIDs: []string{second.ID, first.ID}})
	require.NoError(t, err)
	require.Len(t, h.audit.Records(), 1, "an unchanged order appends nothing")
}

func TestSetTrackCredits(t *testing.T) {
	newFixture := func(t *testing.T) (*harness, release.Track, release.TrackCredit) {
		h := newHarness(t)
		value := h.seedRelease(t, "Album")
		track := h.seedTrack(t, value.ID, 1)
		credit := release.TrackCredit{ID: uuid.NewString(), TrackID: track.ID, CreditedName: text("A"), CreditRole: text("mixer")}
		h.store.AddTrackCredit(credit)
		return h, track, credit
	}
	t.Run("requires the observed snapshot", func(t *testing.T) {
		h, track, _ := newFixture(t)
		_, err := h.service.SetTrackCredits(h.ctx(t), release.SetTrackCreditsCommand{TrackID: track.ID})
		requireCode(t, err, connect.CodeInvalidArgument, "observed: is required")
	})
	t.Run("keeps a credit a peer added and audits each change", func(t *testing.T) {
		h, track, credit := newFixture(t)
		peer := release.TrackCredit{ID: uuid.NewString(), TrackID: track.ID, CreditedName: text("Peer"), SortOrder: 1}
		h.store.AddTrackCredit(peer)
		observed := []*release.CreditInput{{ID: &credit.ID, CreditedName: text("A"), CreditRole: text("mixer")}}
		result, err := h.service.SetTrackCredits(h.ctx(t), release.SetTrackCreditsCommand{
			TrackID: track.ID, Observed: &release.Snapshot[*release.CreditInput]{Items: observed},
			Credits: []*release.CreditInput{
				{ID: &credit.ID, CreditedName: text("A"), CreditRole: text("producer")},
				{CreditedName: text("Added")},
			},
		})
		require.NoError(t, err)
		require.Equal(t, track.ID, result.Track.ID)
		stored := h.store.StoredTrackCredits(track.ID)
		require.Len(t, stored, 3)
		require.Equal(t, "producer", *stored[0].CreditRole)
		require.Equal(t, peer.ID, stored[1].ID, "a credit the client did not observe keeps its place")
		require.Equal(t, "Added", *stored[2].CreditedName)
		var operations []sharedtelemetry.AuditItemOperation
		for _, record := range h.audit.Records() {
			operations = append(operations, record.ItemOperation)
		}
		require.Equal(t, []sharedtelemetry.AuditItemOperation{sharedtelemetry.AuditItemOperationUpdated, sharedtelemetry.AuditItemOperationCreated}, operations)
	})
	t.Run("rejects an Artist that does not exist", func(t *testing.T) {
		h, track, _ := newFixture(t)
		missing := uuid.NewString()
		_, err := h.service.SetTrackCredits(h.ctx(t), release.SetTrackCreditsCommand{
			TrackID: track.ID, Observed: &release.Snapshot[*release.CreditInput]{},
			Credits: []*release.CreditInput{{ArtistID: &missing}},
		})
		requireCode(t, err, connect.CodeInvalidArgument, "credit[0].artist_id: not found: "+missing)
	})
	t.Run("rejects a Member that is not live", func(t *testing.T) {
		h, track, _ := newFixture(t)
		missing := uuid.NewString()
		_, err := h.service.SetTrackCredits(h.ctx(t), release.SetTrackCreditsCommand{
			TrackID: track.ID, Observed: &release.Snapshot[*release.CreditInput]{},
			Credits: []*release.CreditInput{{MemberID: &missing}},
		})
		requireCode(t, err, connect.CodeInvalidArgument, "credit[0].member_id: not found: "+missing)
		require.Equal(t, mutation.StepRollback, lastStep(t, h.units))
	})
	t.Run("answers a missing track", func(t *testing.T) {
		h, _, _ := newFixture(t)
		_, err := h.service.SetTrackCredits(h.ctx(t), release.SetTrackCreditsCommand{TrackID: uuid.NewString()})
		requireCode(t, err, connect.CodeNotFound, "track not found")
	})
}

func TestListTracksByReleaseReadsCreditsInOrder(t *testing.T) {
	h := newHarness(t)
	value := h.seedRelease(t, "Album")
	artistID := uuid.NewString()
	h.store.Artists[artistID] = release.Summary{Title: "Artist", Slug: "artist"}
	track := h.seedTrack(t, value.ID, 1)
	h.store.AddTrackCredit(release.TrackCredit{ID: uuid.NewString(), TrackID: track.ID, ArtistID: &artistID})
	tracks, err := h.service.ListTracksByRelease(h.ctx(t), release.ListTracksQuery{ReleaseID: value.ID})
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	require.Equal(t, "artist", tracks[0].Credits[0].CreditType)
	require.Equal(t, "Artist", *tracks[0].Credits[0].ArtistName)

	h.checker.Deny(can(t, policyv1.Release.View, value.ID), nil)
	_, err = h.service.ListTracksByRelease(h.ctx(t), release.ListTracksQuery{ReleaseID: value.ID})
	requireCode(t, err, connect.CodeNotFound, "release not found: "+value.ID)
}

// joined is a File ingest's transaction over the fake store.
func joined(store *releasetest.Store, recorder *audittest.Recorder) release.Joined {
	return release.Joined{Store: store, Audit: recorder}
}

func TestAttachTrackOriginalAudio(t *testing.T) {
	newTrack := func(t *testing.T) (*releasetest.Store, release.Track, string) {
		store := releasetest.NewStore()
		original := uuid.NewString()
		track := release.Track{ID: uuid.NewString(), ReleaseID: uuid.NewString(), TrackNumber: 1, AudioOriginalFileID: &original, DownloadAudience: "public"}
		store.PutTrack(track)
		return store, track, original
	}
	t.Run("client media stores its duration behind the current file", func(t *testing.T) {
		store, track, original := newTrack(t)
		recorder := &audittest.Recorder{}
		replacement, wrong, duration := uuid.NewString(), uuid.NewString(), 77
		_, err := release.AttachTrackOriginalAudio(t.Context(), joined(store, recorder), release.TrackOriginalAudioInput{
			TrackID: track.ID, VerifiedFileID: replacement, ExpectedCurrentFileID: &wrong, ClientMediaReady: true, DurationSeconds: &duration,
		})
		requireCode(t, err, connect.CodeFailedPrecondition, "Track original audio changed before attachment")
		attachment, err := release.AttachTrackOriginalAudio(t.Context(), joined(store, recorder), release.TrackOriginalAudioInput{
			TrackID: track.ID, VerifiedFileID: replacement, ExpectedCurrentFileID: &original, ClientMediaReady: true, DurationSeconds: &duration,
		})
		require.NoError(t, err)
		require.Equal(t, release.TrackOriginalAudioAttachment{CurrentFileID: replacement, ReleaseID: track.ReleaseID}, attachment)
		stored, _ := store.Track(track.ID)
		require.Equal(t, string(release.TrackProcessingCompleted), *stored.ProcessingStatus)
		require.Equal(t, duration, *stored.DurationSeconds)
		require.Equal(t, release.DownloadAudienceDisabled, stored.DownloadAudience)
		records := recorder.Records()
		require.Len(t, records, 1)
		require.Equal(t, sharedtelemetry.ActorKindSystem, records[0].Kind)
		require.Equal(t, replacement, records[0].FileID)
	})
	t.Run("the same file keeps the download policy", func(t *testing.T) {
		store, track, original := newTrack(t)
		store.PutTrackSegments(track.ID, uuid.NewString())
		recorder := &audittest.Recorder{}
		attachment, err := release.AttachTrackOriginalAudio(t.Context(), joined(store, recorder), release.TrackOriginalAudioInput{
			TrackID: track.ID, VerifiedFileID: original,
		})
		require.NoError(t, err)
		require.True(t, attachment.AlreadyApplied)
		stored, _ := store.Track(track.ID)
		require.Equal(t, "public", stored.DownloadAudience)
		require.NotEmpty(t, store.TrackSegments(track.ID))
		require.Empty(t, recorder.Records())
	})
	t.Run("rejects malformed identities and a missing track", func(t *testing.T) {
		store, track, _ := newTrack(t)
		_, err := release.AttachTrackOriginalAudio(t.Context(), joined(store, &audittest.Recorder{}), release.TrackOriginalAudioInput{TrackID: "nope", VerifiedFileID: uuid.NewString()})
		requireCode(t, err, connect.CodeInvalidArgument, "track_id: must be a UUID")
		_, err = release.AttachTrackOriginalAudio(t.Context(), joined(store, &audittest.Recorder{}), release.TrackOriginalAudioInput{TrackID: track.ID, VerifiedFileID: "nope"})
		requireCode(t, err, connect.CodeInvalidArgument, "verified_file_id: must be a UUID")
		missing := uuid.NewString()
		_, err = release.AttachTrackOriginalAudio(t.Context(), joined(store, &audittest.Recorder{}), release.TrackOriginalAudioInput{TrackID: missing, VerifiedFileID: uuid.NewString()})
		requireCode(t, err, connect.CodeNotFound, "track not found: "+missing)
		requireCode(t, release.LockTrackForUpload(t.Context(), store, missing), connect.CodeNotFound, "track not found: "+missing)
		requireCode(t, release.LockTrackForUpload(t.Context(), store, "nope"), connect.CodeInvalidArgument, "track_id: must be a UUID")
		require.NoError(t, release.LockTrackForUpload(t.Context(), store, track.ID))
	})
}
