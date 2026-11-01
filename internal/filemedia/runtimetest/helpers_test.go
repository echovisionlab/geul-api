//go:build integration

package runtimetest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/echovisionlab/geul-api/internal/auth"
	mediaauth "github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1/managev1connect"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	eventpkg "github.com/echovisionlab/geul-event-contracts/go/event"
)

// The File upload protocol's part and prefix sizes, as the API process uses
// them.
const (
	chunkSize           = 10 * 1024 * 1024
	multipartSniffBytes = 64 * 1024
)

type multipartPresignResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expiresAt"`
}

// clientMediaArtifactPolicy is the MIME type and derivative the API accepts
// for each client media bundle artifact.
func clientMediaArtifactPolicy(t *testing.T, name, kind string) (string, managev1.FileDerivativeType) {
	t.Helper()
	switch {
	case name == "spectrogram.png" && kind == "audio":
		return "image/png", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_SPECTROGRAM
	case name == "waveform.json" && kind == "audio":
		return "application/json", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_WAVEFORM
	case name == "thumbnail.webp" && kind == "video":
		return "image/webp", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_THUMBNAIL
	case filepath.Ext(name) == ".m3u8":
		return "application/vnd.apple.mpegurl", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS
	case filepath.Ext(name) == ".ts":
		return "video/mp2t", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_HLS
	}
	require.FailNow(t, "unsupported client media artifact", name)
	return "", managev1.FileDerivativeType_FILE_DERIVATIVE_TYPE_UNSPECIFIED
}

// clientMediaTestObjects is a minimal valid client media bundle of kind: a
// six-second HLS rendition and the kind's preview artifacts.
func clientMediaTestObjects(t *testing.T, kind string) map[string][]byte {
	t.Helper()
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
	return objects
}

func uploadMultipartPart(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
) uploadPartResponse {
	t.Helper()
	return uploadMultipartPartWithCorrelation(t, baseURL, user, fileID, uploadID, partNumber, body, "")
}

func uploadMultipartBody(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	session *managev1.InitiateMultipartUploadResponse,
	body []byte,
) {
	t.Helper()

	chunkSize := int(session.GetChunkSize())
	require.Positive(t, chunkSize)
	totalParts := int(session.GetTotalParts())
	require.Positive(t, totalParts)
	require.Equal(t, expectedUploadPartCount(len(body), chunkSize), totalParts)

	for partNumber := 1; partNumber <= totalParts; partNumber++ {
		partBody := multipartPartBody(t, session, body, partNumber)
		response := uploadMultipartPart(
			t,
			baseURL,
			user,
			session.GetFileId(),
			session.GetUploadId(),
			partNumber,
			partBody,
		)
		require.NotEmpty(t, response.ETag)
	}
}

func multipartPartBody(
	t *testing.T,
	session *managev1.InitiateMultipartUploadResponse,
	body []byte,
	partNumber int,
) []byte {
	t.Helper()

	chunkSize := int(session.GetChunkSize())
	require.Positive(t, chunkSize)
	totalParts := int(session.GetTotalParts())
	require.Positive(t, totalParts)
	require.GreaterOrEqual(t, partNumber, 1)
	require.LessOrEqual(t, partNumber, totalParts)
	require.Equal(t, expectedUploadPartCount(len(body), chunkSize), totalParts)

	start := (partNumber - 1) * chunkSize
	require.Less(t, start, len(body))
	end := start + chunkSize
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}

func expectedUploadPartCount(bodySize int, chunkSize int) int {
	if bodySize <= 0 {
		return 1
	}
	return (bodySize + chunkSize - 1) / chunkSize
}

func waitForRuntimeMediaDelivery(
	t *testing.T,
	fileClient managev1connect.FileServiceClient,
	user *testutil.OryUser,
	fileID string,
	ready func(*commonv1.MediaDelivery) bool,
) *commonv1.MediaDelivery {
	t.Helper()

	var last *commonv1.MediaDelivery
	testutil.WaitForFileProcessingComplete(t, 3*time.Minute, func() (bool, string, error) {
		getReq := connect.NewRequest(&managev1.GetMediaDeliveryRequest{FileId: fileID})
		setAuthHeaders(getReq.Header(), user)

		getResp, err := fileClient.GetMediaDelivery(context.Background(), getReq)
		if err != nil {
			return false, "", err
		}
		last = getResp.Msg.GetDelivery()
		if last == nil {
			return false, "delivery is nil", nil
		}

		msg := fmt.Sprintf(
			"processing status=%s percentage=%v playback=%q thumbnail=%q spectrogram=%q waveform=%q",
			last.GetProcessingStatus().String(),
			last.ProcessingPercentage,
			last.GetPlayback().GetUrl(),
			last.GetThumbnail().GetUrl(),
			last.GetSpectrogram().GetUrl(),
			last.GetWaveform().GetUrl(),
		)

		return ready(last), msg, nil
	})
	require.NotNil(t, last)
	return last
}

func completeRuntimeEditorMediaUploadAndWait(
	t *testing.T,
	stack *testutil.RuntimeStack,
	fileClient managev1connect.FileServiceClient,
	manager *testutil.OryUser,
	uploadType managev1.UploadType,
	mimeType string,
	fileName string,
	body []byte,
	ready func(*commonv1.MediaDelivery) bool,
) (string, *commonv1.MediaDelivery) {
	t.Helper()

	fileLastModified := time.Now().UnixMilli()
	initReq := connect.NewRequest(&managev1.InitiateMultipartUploadRequest{
		UploadType:       uploadType,
		FileSize:         int64(len(body)),
		MimeType:         mimeType,
		FileName:         fileName,
		FileLastModified: &fileLastModified,
	})
	setAuthHeaders(initReq.Header(), manager)

	initResp, err := fileClient.InitiateMultipartUpload(context.Background(), initReq)
	require.NoError(t, err)

	uploadMultipartBody(t, stack.BackendURL, manager, initResp.Msg, body)
	var bundleID *string
	var clientObjects map[string][]byte
	if uploadType == managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO {
		preparedID, objects := prepareRuntimeClientVideoBundle(t, stack, fileClient, manager, initResp.Msg, body)
		bundleID, clientObjects = &preparedID, objects
	}
	completeReq := connect.NewRequest(&managev1.CompleteMultipartUploadRequest{
		FileId:              initResp.Msg.FileId,
		UploadId:            initResp.Msg.UploadId,
		CorrelationId:       runtimePtr(uuid.NewString()),
		ClientMediaBundleId: bundleID,
	})
	setAuthHeaders(completeReq.Header(), manager)

	completeResp, err := fileClient.CompleteMultipartUpload(context.Background(), completeReq)
	require.NoError(t, err)
	require.Equal(t, initResp.Msg.FileId, completeResp.Msg.FileId)
	require.Equal(t, initResp.Msg.Extension, completeResp.Msg.GetDelivery().GetExtension())
	requireCanonicalInlineRef(t, completeResp.Msg.GetDelivery().GetInline(), initResp.Msg.FileId, initResp.Msg.Extension, mimeType)
	requireCanonicalDownloadRef(t, completeResp.Msg.GetDelivery().GetDownload(), initResp.Msg.FileId, initResp.Msg.Extension, mimeType)
	stored := requireRuntimeCanonicalFileRecord(t, stack.DB, initResp.Msg.FileId, initResp.Msg.Extension, mimeType, body)
	require.Empty(t, stored.SHA256)
	object, err := runtimeS3Client(t, stack).GetObject(t.Context(), &s3.GetObjectInput{Bucket: aws.String(stack.S3MediaBucket), Key: aws.String(runtimeMediaObjectKey(t, initResp.Msg.FileId, initResp.Msg.Extension))})
	require.NoError(t, err)
	canonical, err := io.ReadAll(object.Body)
	require.NoError(t, object.Body.Close())
	require.NoError(t, err)
	require.Equal(t, body, canonical, "canonical object must preserve the client-uploaded bytes")
	if bundleID != nil {
		require.Equal(t, bundleID, stored.ClientMediaBundleID)
	}

	delivery := waitForRuntimeMediaDelivery(t, fileClient, manager, initResp.Msg.FileId, ready)
	if clientObjects != nil {
		playlistURL, err := url.Parse(delivery.GetPlayback().GetUrl())
		require.NoError(t, err)
		require.Equal(t, clientObjects["master.m3u8"], getRuntimePublicMediaBytes(t, playlistURL.String()))
		for name, expected := range clientObjects {
			if strings.HasSuffix(name, ".ts") {
				segmentURL := playlistURL.ResolveReference(&url.URL{Path: name})
				require.Equal(t, expected, getRuntimePublicMediaBytes(t, segmentURL.String()), "public HLS must preserve each verified segment")
			}
		}
		require.Equal(t, clientObjects["thumbnail.webp"], getRuntimePublicMediaBytes(t, delivery.GetThumbnail().GetUrl()))
	}
	return initResp.Msg.FileId, delivery
}

// These fixtures produce real client artifacts before upload. They exercise the
// browser bundle protocol, not the browser's WebCodecs implementation or server transcoding.
func runRuntimeClientMediaCommand(t *testing.T, name string, args ...string) []byte {
	t.Helper()
	_, err := exec.LookPath(name)
	require.NoError(t, err, "%s is required for client media runtime fixtures", name)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	require.NoError(t, err, "%s output: %s", name, output)
	return output
}

func prepareRuntimeClientVideoBundle(
	t *testing.T,
	stack *testutil.RuntimeStack,
	client managev1connect.FileServiceClient,
	manager *testutil.OryUser,
	upload *managev1.InitiateMultipartUploadResponse,
	original []byte,
) (string, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "original.mp4")
	require.NoError(t, os.WriteFile(input, original, 0600))
	runRuntimeClientMediaCommand(t, "ffmpeg", "-y", "-i", input, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-force_key_frames", "expr:gte(t,n_forced*6)", "-f", "hls", "-hls_time", "6", "-hls_playlist_type", "vod", "-hls_segment_filename", filepath.Join(dir, "segment_%03d.ts"), filepath.Join(dir, "master.m3u8"))
	thumbnailPNG := filepath.Join(dir, "thumbnail.png")
	runRuntimeClientMediaCommand(t, "ffmpeg", "-y", "-i", input, "-frames:v", "1", thumbnailPNG)
	runRuntimeClientMediaCommand(t, "cwebp", "-q", "90", thumbnailPNG, "-o", filepath.Join(dir, "thumbnail.webp"))
	require.NoError(t, os.Remove(thumbnailPNG))
	paths, err := filepath.Glob(filepath.Join(dir, "*"))
	require.NoError(t, err)
	objects := make(map[string][]byte)
	for _, path := range paths {
		name := filepath.Base(path)
		if name == "original.mp4" {
			continue
		}
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		objects[name] = body
	}
	durationSeconds := 0.0
	// The plan describes the sealed rendition rather than the original
	// container's duration; native verification compares these EXTINF totals.
	for _, line := range strings.Split(string(objects["master.m3u8"]), "\n") {
		if strings.HasPrefix(line, "#EXTINF:") {
			duration, err := strconv.ParseFloat(strings.SplitN(strings.TrimPrefix(line, "#EXTINF:"), ",", 2)[0], 64)
			require.NoError(t, err)
			durationSeconds += duration
		}
	}
	require.Greater(t, durationSeconds, float64(0))
	return uploadRuntimeClientMediaBundle(t, client, stack.BackendURL, manager, upload, "video", durationSeconds, objects), objects
}

// Both successful and failed-completion fixtures stage bundles through the
// authenticated native HTTP boundary, rather than inserting trusted DB receipts.
func uploadRuntimeClientMediaBundle(
	t *testing.T,
	fileClient managev1connect.FileServiceClient,
	backendURL string,
	user *testutil.OryUser,
	upload *managev1.InitiateMultipartUploadResponse,
	kind string,
	durationSeconds float64,
	objects map[string][]byte,
) string {
	t.Helper()
	mediaKind := managev1.ClientMediaKind_CLIENT_MEDIA_KIND_VIDEO
	if kind == "audio" {
		mediaKind = managev1.ClientMediaKind_CLIENT_MEDIA_KIND_AUDIO
	}
	request := &managev1.PrepareClientMediaUploadRequest{
		FileId: upload.FileId, UploadId: upload.UploadId,
		Kind: mediaKind, DurationSeconds: durationSeconds,
	}
	for _, name := range slices.Sorted(maps.Keys(objects)) {
		mimeType, derivative := clientMediaArtifactPolicy(t, name, kind)
		hash := sha256.Sum256(objects[name])
		request.Artifacts = append(request.Artifacts, &managev1.ClientMediaUploadArtifact{
			Path: name, MimeType: mimeType, Size: int64(len(objects[name])),
			Sha256: hex.EncodeToString(hash[:]), DerivativeType: derivative,
		})
	}
	prepare := connect.NewRequest(request)
	setAuthHeaders(prepare.Header(), user)
	response, err := fileClient.PrepareClientMediaUpload(t.Context(), prepare)
	require.NoError(t, err)
	require.NotEmpty(t, response.Msg.BundleId)
	client := &http.Client{Timeout: 30 * time.Second}
	for _, artifact := range request.Artifacts {
		query := url.Values{"fileId": {upload.FileId}, "uploadId": {upload.UploadId}, "bundleId": {response.Msg.BundleId}, "path": {artifact.Path}}
		put, err := http.NewRequestWithContext(t.Context(), http.MethodPut, backendURL+"/upload/media-artifact?"+query.Encode(), bytes.NewReader(objects[artifact.Path]))
		require.NoError(t, err)
		setAuthHeaders(put.Header, user)
		put.Header.Set("Content-Type", artifact.MimeType)
		result, err := client.Do(put)
		require.NoError(t, err)
		responseBody, err := io.ReadAll(result.Body)
		require.NoError(t, result.Body.Close())
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, result.StatusCode, string(responseBody))
	}
	return response.Msg.BundleId
}

func getRuntimePublicMediaBytes(t *testing.T, mediaURL string) []byte {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, mediaURL, nil)
	require.NoError(t, err)
	// Public assets and HLS must remain fetchable without a user session.
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NotEmpty(t, body)
	return body
}

func uploadMultipartPartWithCorrelation(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	body []byte,
	correlationID string,
) uploadPartResponse {
	t.Helper()

	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if correlationID != "" {
		query.Set("correlationId", correlationID)
	}
	client := &http.Client{Timeout: 30 * time.Second}

	if partNumber == 1 {
		prefixQuery := url.Values{}
		prefixQuery.Set("fileId", fileID)
		prefixQuery.Set("uploadId", uploadID)
		if correlationID != "" {
			prefixQuery.Set("correlationId", correlationID)
		}
		prefixSize := min(len(body), multipartSniffBytes)
		prefixReq, err := http.NewRequest(
			http.MethodPost,
			fmt.Sprintf("%s/upload/prefix?%s", baseURL, prefixQuery.Encode()),
			bytes.NewReader(body[:prefixSize]),
		)
		require.NoError(t, err)
		setAuthHeaders(prefixReq.Header, user)
		prefixReq.ContentLength = int64(prefixSize)
		prefixResp, err := client.Do(prefixReq)
		require.NoError(t, err)
		defer prefixResp.Body.Close()
		prefixResponseBody, err := io.ReadAll(prefixResp.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, prefixResp.StatusCode, string(prefixResponseBody))
	}

	presignReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/presign?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(presignReq.Header, user)
	presignResp, err := client.Do(presignReq)
	require.NoError(t, err)
	defer presignResp.Body.Close()
	require.Equal(t, http.StatusOK, presignResp.StatusCode)
	var presigned multipartPresignResponse
	require.NoError(t, json.NewDecoder(presignResp.Body).Decode(&presigned))
	require.NotEmpty(t, presigned.URL)

	putReq, err := http.NewRequest(http.MethodPut, presigned.URL, bytes.NewReader(body))
	require.NoError(t, err)
	putReq.ContentLength = int64(len(body))
	putResp, err := client.Do(putReq)
	require.NoError(t, err)
	defer putResp.Body.Close()
	putResponseBody, err := io.ReadAll(putResp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, putResp.StatusCode, string(putResponseBody))

	confirmReq, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/confirm?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(confirmReq.Header, user)
	confirmResp, err := client.Do(confirmReq)
	require.NoError(t, err)
	defer confirmResp.Body.Close()
	require.Equal(t, http.StatusOK, confirmResp.StatusCode)

	var payload uploadPartResponse
	require.NoError(t, json.NewDecoder(confirmResp.Body).Decode(&payload))
	require.NotEmpty(t, payload.ETag)
	return payload
}

func requestMultipartPartPresign(
	t *testing.T,
	baseURL string,
	user *testutil.OryUser,
	fileID string,
	uploadID string,
	partNumber int,
	correlationID string,
) multipartPresignResponse {
	t.Helper()
	query := url.Values{}
	query.Set("fileId", fileID)
	query.Set("uploadId", uploadID)
	query.Set("partNumber", fmt.Sprintf("%d", partNumber))
	if correlationID != "" {
		query.Set("correlationId", correlationID)
	}
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/upload/part/presign?%s", baseURL, query.Encode()), nil)
	require.NoError(t, err)
	testutil.ApplyAuthHeaders(req.Header, user)
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload multipartPresignResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload
}

func newFileIngestSignalReceiver(t *testing.T, dsn string) *fileIngestSignalReceiver {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	require.NoError(t, err)
	_, err = conn.Exec(t.Context(), `LISTEN "file.ingest"`)
	require.NoError(t, err)
	receiver := &fileIngestSignalReceiver{conn: conn}
	t.Cleanup(func() {
		_ = receiver.conn.Close(context.Background())
	})
	return receiver
}

func waitForFileIngestProtoEvent[T proto.Message](
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	newEvent func() T,
	match func(T) bool,
) T {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	wantedType := string(newEvent().ProtoReflect().Descriptor().FullName())

	for {
		notification, err := receiver.conn.WaitForNotification(ctx)
		if err != nil {
			t.Fatalf("timed out waiting for matching file ingest event: %v", err)
			var zero T
			return zero
		}
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
		if envelope.MessageType != wantedType {
			continue
		}
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := newEvent()
		require.NoError(t, proto.Unmarshal(body, event))
		if match(event) {
			return event
		}
	}
}

func waitForFileIngestUploadEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	timeout time.Duration,
	match func(*managev1.FileIngestUploadEvent) bool,
) *managev1.FileIngestUploadEvent {
	t.Helper()
	return waitForFileIngestProtoEvent(
		t,
		receiver,
		timeout,
		func() *managev1.FileIngestUploadEvent { return &managev1.FileIngestUploadEvent{} },
		match,
	)
}

func requireNoFileIngestFailedEvent(
	t *testing.T,
	receiver *fileIngestSignalReceiver,
	duration time.Duration,
	match func(*managev1.FileIngestFailedEvent) bool,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(t.Context(), duration)
	defer cancel()
	wantedType := string((&managev1.FileIngestFailedEvent{}).ProtoReflect().Descriptor().FullName())

	for {
		notification, err := receiver.conn.WaitForNotification(ctx)
		if errors.Is(err, context.DeadlineExceeded) {
			return
		}
		require.NoError(t, err)
		var envelope eventpkg.Envelope
		require.NoError(t, json.Unmarshal([]byte(notification.Payload), &envelope))
		if envelope.MessageType != wantedType {
			continue
		}
		body, err := envelope.Payload()
		require.NoError(t, err)
		event := &managev1.FileIngestFailedEvent{}
		require.NoError(t, proto.Unmarshal(body, event))
		require.Falsef(t, match(event), "unexpected file ingest failed event: %v", event)
	}
}

func setAuthHeaders(header http.Header, user *testutil.OryUser) {
	testutil.ApplyAuthHeaders(header, user)
}

func runtimePtr[T interface{}](value T) *T {
	return &value
}

func runtimeTestFileName(base string) string {
	return uuid.NewString() + "-" + base
}

func runtimeDeref[T interface{}](value *T) T {
	var zero T
	if value == nil {
		return zero
	}
	return *value
}

func countUploadSessions(t *testing.T, db *gorm.DB, uploadID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("upload_session").Where("upload_id = ?", uploadID).Count(&count).Error)
	return count
}

func countUploadParts(t *testing.T, db *gorm.DB, uploadID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("upload_part").Where("upload_id = ?", uploadID).Count(&count).Error)
	return count
}

func loadUploadPartInfosForTest(t *testing.T, db *gorm.DB, uploadID string) []*managev1.UploadPartInfo {
	t.Helper()

	var parts []model.UploadPart
	require.NoError(t, db.Where("upload_id = ?", uploadID).Order("part_number ASC").Find(&parts).Error)
	infos := make([]*managev1.UploadPartInfo, 0, len(parts))
	for _, part := range parts {
		infos = append(infos, &managev1.UploadPartInfo{PartNumber: part.PartNumber, Etag: part.ETag})
	}
	return infos
}

func requireUploadSessionStatus(t *testing.T, db *gorm.DB, uploadID string, expected model.UploadSessionStatus) {
	t.Helper()

	var session model.UploadSession
	require.NoError(t, db.Where("upload_id = ?", uploadID).First(&session).Error)
	require.Equal(t, expected, session.Status)
}

func countFilesByID(t *testing.T, db *gorm.DB, fileID string) int64 {
	t.Helper()

	var count int64
	require.NoError(t, db.Table("file").Where("id = ?", fileID).Count(&count).Error)
	return count
}

func requireRuntimeCanonicalFileRecord(
	t *testing.T,
	db *gorm.DB,
	fileID string,
	extension string,
	mimeType string,
	body []byte,
) model.File {
	t.Helper()

	var file model.File
	require.NoError(t, db.Where("id = ?", fileID).Take(&file).Error)
	require.Equal(t, extension, file.Extension)
	require.Equal(t, mimeType, file.MimeType)
	require.EqualValues(t, len(body), file.FileSize)
	return file
}

func abortMultipartInStorage(
	t *testing.T,
	stack *testutil.RuntimeStack,
	fileKey string,
	uploadID string,
) {
	t.Helper()

	s3Client := runtimeS3Client(t, stack)

	_, err := s3Client.AbortMultipartUpload(context.Background(), &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(stack.S3MediaBucket),
		Key:      aws.String(fileKey),
		UploadId: aws.String(uploadID),
	})
	require.NoError(t, err)
}

func runtimeMediaObjectKey(t *testing.T, fileID string, extension string) string {
	t.Helper()
	key, err := mediaauth.MediaObjectKey(fileID, extension)
	require.NoError(t, err)
	return key
}

func runtimeS3Client(t *testing.T, stack *testutil.RuntimeStack) *s3.Client {
	t.Helper()

	awsCfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(stack.S3Region),
		awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(stack.S3AccessKeyID, stack.S3SecretAccessKey, ""),
		),
	)
	require.NoError(t, err)

	s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(stack.S3Endpoint)
		o.UsePathStyle = stack.S3ForcePathStyle
	})
	return s3Client
}

type uploadPartResponse struct {
	ETag string `json:"etag"`
}

type fileIngestSignalReceiver struct {
	conn *pgx.Conn
}

func requireCanonicalDownloadRef(
	t *testing.T,
	ref *commonv1.ExpiringMediaRef,
	fileID string,
	extension string,
	mimeType string,
) {
	t.Helper()
	require.NotNil(t, ref)
	require.Equal(t, fileID, ref.GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_DOWNLOAD, ref.GetPurpose())
	require.Equal(t, extension, ref.GetExtension())
	require.Equal(t, mimeType, ref.GetMimeType())
	require.NotEmpty(t, ref.GetUrl())
	require.NotNil(t, ref.GetExpiresAt())
	require.True(t, ref.GetExpiresAt().AsTime().After(time.Now()))
}

func requireCanonicalInlineRef(
	t *testing.T,
	ref *commonv1.ExpiringMediaRef,
	fileID string,
	extension string,
	mimeType string,
) {
	t.Helper()
	require.NotNil(t, ref)
	require.Equal(t, fileID, ref.GetFileId())
	require.Equal(t, commonv1.MediaDeliveryPurpose_MEDIA_DELIVERY_PURPOSE_INLINE, ref.GetPurpose())
	require.Equal(t, extension, ref.GetExtension())
	require.Equal(t, mimeType, ref.GetMimeType())
	require.NotEmpty(t, ref.GetUrl())
	require.NotNil(t, ref.GetExpiresAt())
	require.True(t, ref.GetExpiresAt().AsTime().After(time.Now()))
}

func seedHardCutPageFixture(t *testing.T, db *gorm.DB) string {
	t.Helper()
	pageID := uuid.NewString()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(
		`INSERT INTO content_document (id, profile) VALUES (?, 'page')`,
		documentID,
	).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO page (id, content_document_id, status, show_title, created_at, updated_at)
		VALUES (?, ?, 'PAGE_STATUS_DRAFT', TRUE, NOW(), NOW())
	`, pageID, documentID).Error)
	return pageID
}

func attachInternalResourcePolicy(t *testing.T, spiceDB *auth.SpiceDBClient, resourceID string) {
	t.Helper()
	mutation, err := policyv1.Page.TouchPolicy(resourceID)
	require.NoError(t, err)
	_, err = spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func seedMeshOptimizationPageFileLink(t *testing.T, db *gorm.DB, pageID string, fileID string) {
	t.Helper()
	documentID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_document (id, profile, revision)
		VALUES (?, 'page', ?)
	`, documentID, uuid.NewString()).Error)
	require.NoError(t, db.Exec(`
		UPDATE page SET content_document_id = ? WHERE id = ?
	`, documentID, pageID).Error)
	blockID := uuid.NewString()
	require.NoError(t, db.Exec(`
		INSERT INTO content_block (
			id, document_id, parent_block_id, container_slot, position, kind, shared_data
		) VALUES (?, ?, NULL, 'body', 0, 'file', '{}'::jsonb)
	`, blockID, documentID).Error)
	require.NoError(t, db.Exec(`
		INSERT INTO content_block_attachment (block_id, reference_path, selector_kind, file_id)
		VALUES (?, 'file', 'active', ?)
	`, blockID, fileID).Error)
}
