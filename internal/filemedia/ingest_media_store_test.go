package filemedia_test

import (
	"context"
	"errors"
	"testing"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/filemedia"
	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/filemedia/filemediatest"
	"github.com/echovisionlab/geul-api/internal/mediaasset"
	"github.com/echovisionlab/geul-api/internal/mutation/mutationtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestFileProcessingAllocatesStablePlainCommandsAndRetriesSameOutputs(t *testing.T) {
	for _, kind := range []string{"audio", "video"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			id := uuid.NewString()
			s.Files[id] = filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: id, Name: "source", Extension: new("mp4"), MimeType: new(kind + "/mp4"), FileSize: new(int64(64))}}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			require.NoError(t, filemedia.ExportTriggerFileProcessing(service, t.Context(), id))
			require.NoError(t, filemedia.ExportTriggerFileProcessing(service, t.Context(), id))
			require.Len(t, s.IngestCommands, 2)
			require.Equal(t, s.IngestCommands[0], s.IngestCommands[1])
			command := s.IngestCommands[0]
			require.Equal(t, id, command.FileID)
			require.Equal(t, id, command.EntityID)
			require.Equal(t, "media/"+id+".mp4", command.Source.ObjectKey)
			require.NotEmpty(t, command.EventID)
			require.Len(t, s.Generations, 1)
			require.Len(t, s.AssetIDs, 1)
			if kind == "audio" {
				require.Equal(t, domain.IngestTranscodeAudio, command.Kind)
				require.Equal(t, "image/png", command.ArtifactOutput.MimeType)
			} else {
				require.Equal(t, domain.IngestTranscodeVideo, command.Kind)
				require.Equal(t, "image/webp", command.ArtifactOutput.MimeType)
			}
		})
	}
}

func TestFileProcessingRefusesMissingDeletingAndInvalidSourcesWithoutOutputs(t *testing.T) {
	for _, state := range []string{"missing", "deleting", "invalid source", "publisher"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			s := filemediatest.NewLibraryStore()
			id := uuid.NewString()
			file := filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: id, Name: "source", Extension: new("mp4"), MimeType: new("audio/mp4"), FileSize: new(int64(64))}}
			if state == "deleting" {
				file.PendingDeletion = true
			}
			if state == "invalid source" {
				file.Row.Extension = new("../mp4")
			}
			if state == "publisher" {
				s.IngestTranscodeError = errors.New("publisher unavailable")
			}
			if state != "missing" {
				s.Files[id] = file
			}
			service := filemedia.ExportSessionService(mutationtest.New[filemedia.Store](s))
			require.Error(t, filemedia.ExportTriggerFileProcessing(service, t.Context(), id))
			require.Empty(t, s.IngestCommands)
			require.Empty(t, s.Generations)
			require.Empty(t, s.AssetIDs)
		})
	}
}

func TestClientMediaRegistrationCompletesReadyOutputsWithoutEncodingCommands(t *testing.T) {
	for _, kind := range []string{"audio", "video"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			f := newLibraryFixture(t)
			row, _, _ := filemedia.ExportClientBundleFixture(t, kind)
			f.store.Files[row.FileID] = filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: row.FileID, Name: "source", Extension: new("mp4"), MimeType: new(row.RequestedMime), FileSize: new(int64(64))}}
			filemedia.ExportConfigureClientMediaStorage(f.files, filemedia.ExportClientMediaStorage(t, row))
			allow := func(context.Context, filemedia.Store, func(*auth.UserInfo) (bool, error)) error { return nil }
			require.NoError(t, filemedia.ExportRegisterClientMedia(f.files, f.ctx, row, allow))
			require.Equal(t, row.ClientMediaBundleID, f.store.Files[row.FileID].ClientMediaBundleID)
			require.EqualValues(t, 6, *f.store.Files[row.FileID].Row.DurationSeconds)
			delivery := filemedia.ExportDeliveryService(mutationtest.New[filemedia.Store](f.store))
			response, err := filemedia.ExportGetFileURLs(delivery, f.ctx, row.FileID)
			require.NoError(t, err)
			require.Equal(t, row.ClientMediaBundleID, response.ClientMediaBundleID)
			require.NotEmpty(t, response.Delivery.Playback)
			require.NoError(t, filemedia.ExportTriggerFileProcessing(f.files, f.ctx, row.FileID))
			require.Empty(t, f.store.IngestCommands)
			require.NoError(t, filemedia.ExportRegisterClientMedia(f.files, f.ctx, row, allow))
			for _, g := range f.store.Generations {
				require.Equal(t, mediaasset.GenerationReady, g.Status)
			}
			for id := range f.store.AssetIDs {
				a, err := f.store.MediaAssets.Asset(f.ctx, id)
				require.NoError(t, err)
				require.Equal(t, mediaasset.AssetReady, a.Status)
			}
			rows := f.store.Derivatives[row.FileID]
			for i, derivative := range rows {
				if derivative.MediaGenerationID != nil {
					f.store.Derivatives[row.FileID] = append(rows[:i], rows[i+1:]...)
					break
				}
			}
			require.Error(t, filemedia.ExportTriggerFileProcessing(f.files, f.ctx, row.FileID))
			require.Empty(t, f.store.IngestCommands)
		})
	}
}

func TestClientMediaAuthorityLossAfterCopyKeepsCleanupAllocationsUnready(t *testing.T) {
	f := newLibraryFixture(t)
	row, _, _ := filemedia.ExportClientBundleFixture(t, "audio")
	f.store.Files[row.FileID] = filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: row.FileID, Name: "source", Extension: new("mp4"), MimeType: new(row.RequestedMime), FileSize: new(int64(64))}}
	filemedia.ExportConfigureClientMediaStorage(f.files, filemedia.ExportClientMediaStorage(t, row))
	calls := 0
	denied := errors.New("authority revoked")
	err := filemedia.ExportRegisterClientMedia(f.files, f.ctx, row, func(context.Context, filemedia.Store, func(*auth.UserInfo) (bool, error)) error {
		calls++
		if calls > 1 {
			return denied
		}
		return nil
	})
	require.ErrorIs(t, err, denied)
	require.Equal(t, 2, calls)
	require.Nil(t, f.store.Files[row.FileID].ClientMediaBundleID)
	require.Empty(t, f.store.Derivatives[row.FileID])
	require.NotEmpty(t, f.store.Generations)
	for _, g := range f.store.Generations {
		require.Equal(t, mediaasset.GenerationAllocated, g.Status)
	}
	for id := range f.store.AssetIDs {
		a, readErr := f.store.MediaAssets.Asset(f.ctx, id)
		require.NoError(t, readErr)
		require.Equal(t, mediaasset.AssetAllocated, a.Status)
	}
}

func TestTrackAudioAttachmentAndQueuedOutputsRollbackOnQueueFailure(t *testing.T) {
	f := newLibraryFixture(t)
	file, track, release, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	f.store.UploadTracks = map[string]bool{track: true}
	f.store.TrackReleases[track] = release
	f.store.Files[file] = filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: file, Name: "source", Extension: new("mp3"), MimeType: new("audio/mpeg"), FileSize: new(int64(64))}, IngestAttemptID: &attempt}
	f.store.IngestBindings[file] = domain.IngestBinding{FileID: file, UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityID: track, EntityType: new("TRANSCODE_ENTITY_TYPE_TRACK")}
	f.store.IngestTranscodeError = errors.New("queue unavailable")
	_, err := f.files.AttachTrackOriginalAudio(f.ctx, filemedia.AttachTrackOriginalAudioCommand{TrackID: track, VerifiedFileID: file, IngestAttemptID: attempt})
	require.ErrorIs(t, err, f.store.IngestTranscodeError)
	require.Empty(t, f.store.TrackOriginals)
	require.Empty(t, f.store.Generations)
	require.Empty(t, f.store.AssetIDs)
	require.Empty(t, f.store.IngestCommands)
}

func TestTrackAudioAttachmentUsesCompletedClientBundleWithoutEncoding(t *testing.T) {
	f := newLibraryFixture(t)
	row, _, _ := filemedia.ExportClientBundleFixture(t, "audio")
	track, release, attempt := uuid.NewString(), uuid.NewString(), uuid.NewString()
	f.store.UploadTracks = map[string]bool{track: true}
	f.store.TrackReleases[track] = release
	f.store.Files[row.FileID] = filemediatest.CatalogFileRecord{Row: domain.CatalogRow{ID: row.FileID, Name: "source", Extension: new("mp4"), MimeType: new(row.RequestedMime), FileSize: new(int64(64))}, IngestAttemptID: &attempt}
	f.store.IngestBindings[row.FileID] = domain.IngestBinding{FileID: row.FileID, UploadType: "UPLOAD_TYPE_TRACK_AUDIO", EntityID: track, EntityType: new("TRANSCODE_ENTITY_TYPE_TRACK")}
	filemedia.ExportConfigureClientMediaStorage(f.files, filemedia.ExportClientMediaStorage(t, row))
	require.NoError(t, filemedia.ExportRegisterClientMedia(f.files, f.ctx, row, func(context.Context, filemedia.Store, func(*auth.UserInfo) (bool, error)) error { return nil }))
	result, err := f.files.AttachTrackOriginalAudio(f.ctx, filemedia.AttachTrackOriginalAudioCommand{TrackID: track, VerifiedFileID: row.FileID, IngestAttemptID: attempt})
	require.NoError(t, err)
	require.Equal(t, row.FileID, result.CurrentFileID)
	require.Equal(t, release, result.ReleaseID)
	require.Equal(t, row.FileID, f.store.TrackOriginals[track])
	require.Empty(t, f.store.IngestCommands)
}
