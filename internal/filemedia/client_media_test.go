package filemedia

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	intrav1 "github.com/echovisionlab/geul-event-contracts/gen/api/intra/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func clientMediaTestBundle(t *testing.T, kind string) (model.UploadSession, map[string][]byte) {
	t.Helper()
	bundleID := uuid.NewString()
	session := model.UploadSession{FileID: uuid.NewString(), UploadID: "upload-client", RequestedMime: kind + "/mp4", UploadType: managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE.String(), ClientMediaBundleID: &bundleID}
	segment := make([]byte, 188*3)
	for i := 0; i < len(segment); i += 188 {
		segment[i] = 0x47
	}
	objects := map[string][]byte{"master.m3u8": []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nsegment.ts\n#EXT-X-ENDLIST\n"), "segment.ts": segment}
	if kind == "audio" {
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 1600, 224))))
		objects["spectrogram.png"] = buf.Bytes()
		channel := make([]float64, 2048)
		channel[0] = -0.75
		waveform, err := json.Marshal([][]float64{channel})
		require.NoError(t, err)
		objects["waveform.json"] = waveform
	} else {
		objects["thumbnail.webp"] = append([]byte("RIFFxxxxWEBP"), make([]byte, 20)...)
	}
	plan := clientMediaPlan{MemberID: uuid.NewString(), Version: 1, Kind: kind, DurationSeconds: 6}
	for name, body := range objects {
		mime, deriv, err := clientMediaArtifactPolicy(name, kind)
		require.NoError(t, err)
		hash := sha256.Sum256(body)
		plan.Artifacts = append(plan.Artifacts, clientMediaArtifact{Path: name, MimeType: mime, Size: int64(len(body)), SHA256: hex.EncodeToString(hash[:]), DerivativeType: int32(deriv)})
	}
	normalizeClientMediaPlan(&plan)
	encoded, err := json.Marshal(plan)
	require.NoError(t, err)
	manifest := string(encoded)
	session.ClientMediaManifest = &manifest
	return session, objects
}

func TestClientMediaPlanRejectsMissingTamperedAndUnsafeArtifacts(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "audio")
	plan, err := decodeClientMediaPlan(session)
	require.NoError(t, err)
	for _, test := range []struct {
		name   string
		mutate func(*clientMediaPlan)
	}{
		{"path traversal", func(p *clientMediaPlan) { p.Artifacts[0].Path = "../master.m3u8" }},
		{"duplicate", func(p *clientMediaPlan) { p.Artifacts = append(p.Artifacts, p.Artifacts[0]) }},
		{"wrong hash", func(p *clientMediaPlan) { p.Artifacts[0].SHA256 = "abcd" }},
		{"oversize", func(p *clientMediaPlan) { p.Artifacts[0].Size = maxClientMediaArtifactSize + 1 }},
		{"missing waveform", func(p *clientMediaPlan) {
			for i, a := range p.Artifacts {
				if a.Path == "waveform.json" {
					p.Artifacts = append(p.Artifacts[:i], p.Artifacts[i+1:]...)
					break
				}
			}
		}},
		{"wrong MIME", func(p *clientMediaPlan) { p.Artifacts[0].MimeType = "text/html" }},
		{"wrong kind", func(p *clientMediaPlan) { p.Kind = "video" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := plan
			changed.Artifacts = append([]clientMediaArtifact(nil), plan.Artifacts...)
			test.mutate(&changed)
			require.Error(t, validateClientMediaPlan(changed, session))
		})
	}
	require.Error(t, clientMediaBundleMatches(session, ""))
	require.Error(t, clientMediaBundleMatches(session, uuid.NewString()))
	require.NoError(t, clientMediaBundleMatches(session, *session.ClientMediaBundleID))
}
func TestClientMediaWaveformPreservesSignedPeaks(t *testing.T) {
	channel := make([]float64, 2048)
	channel[0] = -1
	channel[1] = 1
	data, err := json.Marshal([][]float64{channel})
	require.NoError(t, err)
	require.NoError(t, validateClientWaveform(data))
	channel[0] = -1.001
	data, err = json.Marshal([][]float64{channel})
	require.NoError(t, err)
	require.Error(t, validateClientWaveform(data))
	require.Error(t, validateClientWaveform([]byte(`[[0.1]]`)))
	require.Error(t, validateClientWaveform([]byte("[["+strings.Repeat("null,", 2047)+"null]]")))
}

func newClientMediaStorageService(t *testing.T, session model.UploadSession, objects map[string][]byte) *FileService {
	t.Helper()
	stored := map[string][]byte{}
	mimes := map[string]string{}
	plan, err := decodeClientMediaPlan(session)
	require.NoError(t, err)
	for _, a := range plan.Artifacts {
		if _, ok := objects[a.Path]; !ok {
			continue
		}
		key := clientMediaStagingKey(*session.ClientMediaBundleID, a.Path)
		stored[key] = objects[a.Path]
		mimes[key] = a.MimeType
	}
	return newObjectVerificationService(t, func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/media-bucket/")
		switch r.Method {
		case http.MethodGet:
			body, ok := stored[key]
			if !ok {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
				return
			}
			w.Header().Set("Content-Type", mimes[key])
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			_, _ = w.Write(body)
		case http.MethodPut:
			source, err := url.PathUnescape(r.Header.Get("X-Amz-Copy-Source"))
			require.NoError(t, err)
			source = strings.TrimPrefix(source, "media-bucket/")
			body, ok := stored[source]
			require.True(t, ok)
			stored[key] = append([]byte(nil), body...)
			mimes[key] = mimes[source]
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"copied"</ETag></CopyObjectResult>`))
		case http.MethodDelete:
			delete(stored, key)
			delete(mimes, key)
			w.WriteHeader(204)
		default:
			http.Error(w, "unexpected", 400)
		}
	})
}

func TestClientMediaVerifiedAudioVideoRegisterWithoutEncodingJobs(t *testing.T) {
	for _, kind := range []string{"audio", "video"} {
		t.Run(kind, func(t *testing.T) {
			session, objects := clientMediaTestBundle(t, kind)
			svc := newClientMediaStorageService(t, session, objects)
			db := newServiceUnitDB(t)
			svc.db = db
			require.NoError(t, db.Exec(`CREATE TABLE file_derivative (id TEXT PRIMARY KEY,file_id TEXT,type TEXT,asset_id TEXT,media_generation_id TEXT,created_at DATETIME,UNIQUE(file_id,type))`).Error)
			require.NoError(t, db.Create(&model.File{ID: session.FileID, FileName: "source", Extension: "mp4", MimeType: session.RequestedMime, FileSize: 100}).Error)
			publisher := &recordingFileTranscoderPublisher{}
			svc.publisher = publisher
			completion := &multipartCompletion{session: session}
			require.NoError(t, svc.registerVerifiedClientMediaBundle(t.Context(), completion, func(context.Context, *gorm.DB) error { return nil }))
			require.NoError(t, svc.triggerFileScopedProcessingIfNeeded(t.Context(), session.FileID))
			require.Empty(t, publisher.audioJobs)
			require.Empty(t, publisher.videoJobs)
			require.Empty(t, publisher.registeredAudioJobs)
			require.Empty(t, publisher.registeredVideoJobs)
			var file model.File
			require.NoError(t, db.Where("id = ?", session.FileID).Take(&file).Error)
			require.Equal(t, session.ClientMediaBundleID, file.ClientMediaBundleID)
			require.Equal(t, 6, *file.DurationSeconds)
			svc.mediaDomain = "media.example.test"
			svc.cdnDomain = "cdn.example.test"
			svc.mediaSecret = "test-secret"
			svc.downloadTTL = time.Minute
			response, err := svc.getFileUrlsForID(t.Context(), session.FileID)
			require.NoError(t, err)
			require.Equal(t, file.ClientMediaBundleID, response.ClientMediaBundleId)
			require.True(t, manageDeliveryResponseMatchesFile(response, file), "single-file receipt must survive final authorization fence")

			ready, err := clientMediaFileReady(t.Context(), db, file)
			require.NoError(t, err)
			require.True(t, ready)
			// Staging disappears after completion. Registration retry cannot write accepted READY bytes again.
			require.NoError(t, svc.cleanupClientMediaStaging(t.Context(), session))
			require.NoError(t, svc.registerVerifiedClientMediaBundle(t.Context(), completion, func(context.Context, *gorm.DB) error { return nil }))
			require.NoError(t, db.Where("file_id = ? AND type = ?", file.ID, managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS.String()).Delete(&model.FileDerivative{}).Error)
			require.Error(t, svc.triggerFileScopedProcessingIfNeeded(t.Context(), file.ID))
			require.Empty(t, publisher.audioJobs)
			require.Empty(t, publisher.videoJobs)
		})
	}
}

func TestClientMediaStoredBundleRejectsTamperingMissingAndBadPlaylist(t *testing.T) {
	for _, mode := range []string{"hash", "size", "missing", "external", "bad duration", "bad TS"} {
		t.Run(mode, func(t *testing.T) {
			session, objects := clientMediaTestBundle(t, "audio")
			plan, err := decodeClientMediaPlan(session)
			require.NoError(t, err)
			switch mode {
			case "hash":
				objects["segment.ts"][1] = 0xff
			case "size":
				objects["segment.ts"] = objects["segment.ts"][:188]
			case "missing":
				delete(objects, "waveform.json")
			case "external":
				objects["master.m3u8"] = []byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nhttps://host/segment.ts\n#EXT-X-ENDLIST\n")
			case "bad duration":
				plan.DurationSeconds = 60
			case "bad TS":
				objects["segment.ts"][188] = 0
			}
			if mode == "external" {
				for i, a := range plan.Artifacts {
					if a.Path == "master.m3u8" {
						body := objects[a.Path]
						hash := sha256.Sum256(body)
						plan.Artifacts[i].Size = int64(len(body))
						plan.Artifacts[i].SHA256 = hex.EncodeToString(hash[:])
					}
				}
			}
			if mode == "bad TS" {
				for i, a := range plan.Artifacts {
					if a.Path == "segment.ts" {
						hash := sha256.Sum256(objects[a.Path])
						plan.Artifacts[i].SHA256 = hex.EncodeToString(hash[:])
					}
				}
			}
			encoded, err := json.Marshal(plan)
			require.NoError(t, err)
			manifest := string(encoded)
			session.ClientMediaManifest = &manifest
			svc := newClientMediaStorageService(t, session, objects)
			_, err = svc.verifyClientMediaBundle(context.Background(), session)
			require.Error(t, err)
			classified := clientMediaCompletionError(err)
			if mode == "missing" {
				require.Equal(t, connect.CodeUnavailable, connect.CodeOf(classified))
				require.ErrorContains(t, classified, "CLIENT_MEDIA_ARTIFACTS_MISSING:")
			} else {
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(classified))
			}
		})
	}
}

func TestClientMediaPreparedMemberCannotBeReplacedByAnotherAuthorizedMember(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "audio")
	// Membership denial occurs before any role/domain lookup, so equal global roles cannot bypass the pin.
	err := (&FileService{}).checkPartUploadPermission(t.Context(), uuid.NewString(), session)
	require.ErrorContains(t, err, "belongs to another member")
	_, err = (&FileService{}).PrepareClientMediaUpload(t.Context(), nil)
	require.Error(t, err)
}

type clientMediaTrackAttachment struct{ input TrackOriginalAudioInput }

func (a *clientMediaTrackAttachment) LockExistsWithDB(context.Context, *gorm.DB, string) error {
	return nil
}
func (a *clientMediaTrackAttachment) AttachOriginalWithDB(_ context.Context, _ *gorm.DB, input TrackOriginalAudioInput) (TrackOriginalAudioAttachment, error) {
	a.input = input
	return TrackOriginalAudioAttachment{CurrentFileID: input.VerifiedFileID, ReleaseID: uuid.NewString()}, nil
}

func TestClientMediaTrackFinalizerUsesCompletedBundleWithoutJobs(t *testing.T) {
	session, objects := clientMediaTestBundle(t, "audio")
	svc := newClientMediaStorageService(t, session, objects)
	db := newServiceUnitDB(t)
	svc.db = db
	require.NoError(t, db.Exec(`CREATE TABLE file_derivative (id TEXT PRIMARY KEY,file_id TEXT,type TEXT,asset_id TEXT,media_generation_id TEXT,created_at DATETIME,UNIQUE(file_id,type))`).Error)
	attemptID := uuid.NewString()
	trackID := uuid.NewString()
	session.EntityID = trackID
	session.AttemptID = &attemptID
	session.UploadType = managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO.String()
	entityType := managev1.TranscodeEntityType_TRANSCODE_ENTITY_TYPE_TRACK.String()
	session.EntityType = &entityType
	require.NoError(t, db.Create(&model.File{ID: session.FileID, FileName: "source", Extension: "mp4", MimeType: session.RequestedMime, FileSize: 100, IngestAttemptID: &attemptID}).Error)
	require.NoError(t, db.Create(&model.FileIngestBinding{FileID: session.FileID, UploadType: session.UploadType, EntityID: trackID, EntityType: &entityType, CreatedAt: time.Now()}).Error)
	require.NoError(t, svc.registerVerifiedClientMediaBundle(t.Context(), &multipartCompletion{session: session}, func(context.Context, *gorm.DB) error { return nil }))
	publisher := &recordingFileTranscoderPublisher{}
	svc.publisher = publisher
	attachment := &clientMediaTrackAttachment{}
	svc.trackAttachment = attachment
	_, err := svc.attachTrackOriginalAudio(t.Context(), &intrav1.AttachTrackOriginalAudioRequest{TrackId: trackID, VerifiedFileId: session.FileID, IngestAttemptId: attemptID})
	require.NoError(t, err)
	require.True(t, attachment.input.ClientMediaReady)
	require.Equal(t, 6, *attachment.input.DurationSeconds)
	require.Empty(t, publisher.audioJobs)
	require.Empty(t, publisher.registeredAudioJobs)
}

func TestClientMediaAuthorityRevocationCannotMakeCopiedArtifactsReady(t *testing.T) {
	session, objects := clientMediaTestBundle(t, "audio")
	svc := newClientMediaStorageService(t, session, objects)
	db := newServiceUnitDB(t)
	svc.db = db
	require.NoError(t, db.Exec(`CREATE TABLE file_derivative (id TEXT PRIMARY KEY,file_id TEXT,type TEXT,asset_id TEXT,media_generation_id TEXT,created_at DATETIME,UNIQUE(file_id,type))`).Error)
	require.NoError(t, db.Create(&model.File{ID: session.FileID, FileName: "source", Extension: "mp4", MimeType: session.RequestedMime, FileSize: 100}).Error)
	revoked := fmt.Errorf("membership revoked")
	authorityChecks := 0
	err := svc.registerVerifiedClientMediaBundle(t.Context(), &multipartCompletion{session: session}, func(context.Context, *gorm.DB) error {
		authorityChecks++
		if authorityChecks == 2 {
			return revoked
		}
		return nil
	})
	require.Equal(t, 2, authorityChecks, "authority must be checked again after canonical copies")
	require.ErrorIs(t, err, revoked)
	var count int64
	require.NoError(t, db.Model(&model.FileDerivative{}).Count(&count).Error)
	require.Zero(t, count)
	var file model.File
	require.NoError(t, db.Where("id = ?", session.FileID).Take(&file).Error)
	require.Nil(t, file.ClientMediaBundleID)
}

func TestClientMediaReadySignalHasCompleteOutputsAndNoCommands(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "audio")
	publisher := &capturingAsyncPublisher{}
	svc := &FileService{asyncPublisher: publisher}
	require.NoError(t, svc.publishClientMediaReady(t.Context(), &multipartCompletion{session: session}))
	require.Equal(t, 1, publisher.rawPublishCalls)
	require.Zero(t, publisher.confirmCalls)
	require.Len(t, publisher.messages, 1)
	var event managev1.MediaProcessingLifecycleEvent
	require.NoError(t, proto.Unmarshal(publisher.messages[0].body, &event))
	require.NotEmpty(t, event.GetOutputs().GetHlsGenerationId())
	require.NotEmpty(t, event.GetOutputs().GetWaveformAssetId())
	require.NotEmpty(t, event.GetOutputs().GetSpectrogramAssetId())
	require.EqualValues(t, 6, event.GetOutputs().GetDurationSeconds())
}

func TestClientMediaFinalizingRepairNeverReopensRegisteredBundle(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "audio")
	svc := &FileService{db: newServiceUnitDB(t)}
	session.Status = model.UploadSessionStatusUploading
	allowed, err := svc.clientMediaArtifactWritable(t.Context(), session)
	require.NoError(t, err)
	require.True(t, allowed)
	session.Status = model.UploadSessionStatusFinalizing
	allowed, err = svc.clientMediaArtifactWritable(t.Context(), session)
	require.NoError(t, err)
	require.True(t, allowed)
	file := model.File{ID: session.FileID, FileName: "source", Extension: "mp4", MimeType: session.RequestedMime, FileSize: 100}
	require.NoError(t, svc.db.Create(&file).Error)
	allowed, err = svc.clientMediaArtifactWritable(t.Context(), session)
	require.NoError(t, err)
	require.True(t, allowed)
	require.NoError(t, svc.db.Model(&model.File{}).Where("id = ?", file.ID).Update("client_media_bundle_id", *session.ClientMediaBundleID).Error)
	allowed, err = svc.clientMediaArtifactWritable(t.Context(), session)
	require.NoError(t, err)
	require.False(t, allowed)
	session.Status = model.UploadSessionStatusAborted
	allowed, err = svc.clientMediaArtifactWritable(t.Context(), session)
	require.NoError(t, err)
	require.False(t, allowed)
}

func TestDirectMediaCompletionRequiresBrowserBundle(t *testing.T) {
	for _, uploadType := range []managev1.UploadType{managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT, managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO, managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO, managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO} {
		for _, mime := range []string{"audio/mpeg", "video/mp4"} {
			t.Run(uploadType.String()+mime, func(t *testing.T) {
				session := model.UploadSession{}
				err := requireDirectClientMediaBundle(uploadType, mime, session, "")
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
				require.Contains(t, err.Error(), "clientMediaBundleRequired")
				nonce := uuid.NewString()
				session.ClientMediaBundleID = &nonce
				require.Error(t, requireDirectClientMediaBundle(uploadType, mime, session, nonce), "nonce without immutable manifest is insufficient")
				kind := strings.SplitN(mime, "/", 2)[0]
				session, _ = clientMediaTestBundle(t, kind)
				session.UploadType = uploadType.String()
				nonce = *session.ClientMediaBundleID
				require.NoError(t, requireDirectClientMediaBundle(uploadType, mime, session, nonce))
				otherKind := "video/mp4"
				if kind == "video" {
					otherKind = "audio/mp4"
				}
				require.ErrorContains(t, requireDirectClientMediaBundle(uploadType, otherKind, session, nonce), "differs from verified original")
				require.Error(t, requireDirectClientMediaBundle(uploadType, mime, session, uuid.NewString()))
			})
		}
	}
	require.NoError(t, requireDirectClientMediaBundle(managev1.UploadType_UPLOAD_TYPE_GENERAL_FILE, "application/pdf", model.UploadSession{}, ""))
	require.NoError(t, requireDirectClientMediaBundle(managev1.UploadType_UPLOAD_TYPE_EDITOR_ATTACHMENT, "application/pdf", model.UploadSession{}, ""))
	require.NoError(t, requireDirectClientMediaBundle(managev1.UploadType_UPLOAD_TYPE_EDITOR_IMAGE, "image/png", model.UploadSession{}, ""))
	service := &FileService{}
	_, err := service.CompleteMultipartUpload(context.Background(), nil)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

func TestClientMediaPlanBoundsAggregatePlaylistMemory(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "audio")
	plan, err := decodeClientMediaPlan(session)
	require.NoError(t, err)
	playlist := clientMediaArtifact{MimeType: "application/vnd.apple.mpegurl", Size: 1 << 20, SHA256: strings.Repeat("0", 64), DerivativeType: int32(managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS)}
	for i := 0; i < 9; i++ {
		playlist.Path = fmt.Sprintf("variant%d.m3u8", i)
		plan.Artifacts = append(plan.Artifacts, playlist)
	}
	require.ErrorContains(t, validateClientMediaPlan(plan, session), "playlist budget")
	plan.Artifacts = plan.Artifacts[:len(plan.Artifacts)-9]
	playlist.Size = 1
	for i := 0; i < 16; i++ {
		playlist.Path = fmt.Sprintf("variant%d.m3u8", i)
		plan.Artifacts = append(plan.Artifacts, playlist)
	}
	require.ErrorContains(t, validateClientMediaPlan(plan, session), "playlist budget")
}

func TestClientMediaArtifactMIMEIsCaseInsensitive(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "video")
	plan, err := decodeClientMediaPlan(session)
	require.NoError(t, err)
	for i := range plan.Artifacts {
		if strings.HasSuffix(plan.Artifacts[i].Path, ".ts") {
			plan.Artifacts[i].MimeType = "video/MP2T"
		}
	}
	require.NoError(t, validateClientMediaPlan(plan, session))
	for i := range plan.Artifacts {
		if strings.HasSuffix(plan.Artifacts[i].Path, ".ts") {
			plan.Artifacts[i].MimeType = "audio/mp2t"
		}
	}
	require.Error(t, validateClientMediaPlan(plan, session))
}

func TestClientMediaPreparedPlanIsImmutable(t *testing.T) {
	session, _ := clientMediaTestBundle(t, "audio")
	plan, err := decodeClientMediaPlan(session)
	require.NoError(t, err)
	ctx := auth.WithUser(t.Context(), &auth.UserInfo{MemberID: auth.MemberID(plan.MemberID), Authenticated: true})
	request := &managev1.PrepareClientMediaUploadRequest{
		FileId: session.FileID, UploadId: session.UploadID,
		Kind: managev1.ClientMediaKind_CLIENT_MEDIA_KIND_AUDIO, DurationSeconds: plan.DurationSeconds,
	}
	for _, artifact := range plan.Artifacts {
		request.Artifacts = append(request.Artifacts, &managev1.ClientMediaUploadArtifact{
			Path: artifact.Path, MimeType: artifact.MimeType, Size: artifact.Size,
			Sha256: artifact.SHA256, DerivativeType: managev1.FileDerivativeType(artifact.DerivativeType),
		})
	}
	service := &FileService{}
	bundleID, err := service.prepareMediaBundle(ctx, session, request)
	require.NoError(t, err)
	require.Equal(t, *session.ClientMediaBundleID, bundleID)
	// Ordering and MIME casing are equivalent metadata, not a new plan.
	for left, right := 0, len(request.Artifacts)-1; left < right; left, right = left+1, right-1 {
		request.Artifacts[left], request.Artifacts[right] = request.Artifacts[right], request.Artifacts[left]
	}
	request.Artifacts[0].MimeType = strings.ToUpper(request.Artifacts[0].MimeType)
	bundleID, err = service.prepareMediaBundle(ctx, session, request)
	require.NoError(t, err)
	require.Equal(t, *session.ClientMediaBundleID, bundleID)
	for _, test := range []struct {
		name   string
		mutate func(*managev1.PrepareClientMediaUploadRequest)
	}{
		{"duration", func(r *managev1.PrepareClientMediaUploadRequest) { r.DurationSeconds++ }},
		{"size", func(r *managev1.PrepareClientMediaUploadRequest) { r.Artifacts[0].Size++ }},
		{"digest", func(r *managev1.PrepareClientMediaUploadRequest) { r.Artifacts[0].Sha256 = strings.Repeat("0", 64) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := proto.Clone(request).(*managev1.PrepareClientMediaUploadRequest)
			test.mutate(changed)
			_, err := service.prepareMediaBundle(ctx, session, changed)
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.ErrorContains(t, err, "conflicts with existing bundle")
		})
	}
}
