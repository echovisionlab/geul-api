package filemedia

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestValidateMultipartCompletionMimeRejectsMediaContainerCorrections(t *testing.T) {
	t.Parallel()

	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
	} {
		t.Run(uploadType.String(), func(t *testing.T) {
			t.Parallel()

			_, err := validateMultipartCompletionMime(
				"audio/mpeg",
				"audio/aiff",
				model.DefaultUploadConfigs[uploadType].PermittedMimeTypes,
			)
			require.Error(t, err)
		})
	}
}

func TestValidateMultipartCompletionMimeRejectsDetectedVideoForAudioUpload(t *testing.T) {
	t.Parallel()

	for _, uploadType := range []managev1.UploadType{
		managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO,
		managev1.UploadType_UPLOAD_TYPE_TRACK_AUDIO,
	} {
		t.Run(uploadType.String(), func(t *testing.T) {
			t.Parallel()

			_, err := validateMultipartCompletionMime(
				"audio/mp4",
				"video/mp4",
				model.DefaultUploadConfigs[uploadType].PermittedMimeTypes,
			)
			require.Error(t, err)
		})
	}
}

func TestExpectedMultipartPartSizeUsesSessionChunkBoundaries(t *testing.T) {
	t.Parallel()

	const chunk = int64(64 * 1024 * 1024)
	const fileSize = chunk*2 + 123

	require.Equal(t, chunk, expectedMultipartPartSize(fileSize, chunk, 1))
	require.Equal(t, chunk, expectedMultipartPartSize(fileSize, chunk, 2))
	require.Equal(t, int64(123), expectedMultipartPartSize(fileSize, chunk, 3))
	require.Equal(t, int64(0), expectedMultipartPartSize(fileSize, chunk, 4))
}

func TestValidateMultipartPartContentLengthUsesStoredSessionChunkSize(t *testing.T) {
	t.Parallel()

	session := model.UploadSession{
		FileSize:  12,
		ChunkSize: 5,
	}

	require.NoError(t, validateMultipartPartContentLength(session, 1, 5))
	require.NoError(t, validateMultipartPartContentLength(session, 2, 5))
	require.NoError(t, validateMultipartPartContentLength(session, 3, 2))
	require.Error(t, validateMultipartPartContentLength(session, 3, 5))
	require.Error(t, validateMultipartPartContentLength(session, 4, 1))
}

func TestReadMultipartSniffPrefixReplaysShortBody(t *testing.T) {
	t.Parallel()

	source := "prefix-and-the-rest"
	prefix, uploadBody, err := readMultipartSniffPrefix(strings.NewReader(source), int64(len(source)))
	require.NoError(t, err)
	require.Equal(t, source, string(prefix))

	replayed, err := io.ReadAll(uploadBody)
	require.NoError(t, err)
	require.Equal(t, source, string(replayed))
}

func TestReadMultipartSniffPrefixReplaysPrefixBeforeRemainingBody(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("a", multipartSniffBytes+17)
	prefix, uploadBody, err := readMultipartSniffPrefix(strings.NewReader(source), int64(len(source)))
	require.NoError(t, err)
	require.Len(t, prefix, multipartSniffBytes)

	replayed, err := io.ReadAll(uploadBody)
	require.NoError(t, err)
	require.Equal(t, source, string(replayed))
}

func TestReadMultipartUploadBodyReadsExactLength(t *testing.T) {
	t.Parallel()

	body, err := readMultipartUploadBody(strings.NewReader("abcdef"), 6)
	require.NoError(t, err)
	require.Equal(t, []byte("abcdef"), body)
}

func TestReadMultipartUploadBodyRejectsShortBody(t *testing.T) {
	t.Parallel()

	_, err := readMultipartUploadBody(strings.NewReader("abc"), 6)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}

func TestDetectCanonicalMimeDetectsValidGLB(t *testing.T) {
	t.Parallel()

	body := buildTestGLB([]byte(`{"asset":{"version":"2.0"}}`), 0)

	allowedSet := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH].PermittedMimeTypes,
	)

	require.Equal(t, "model/gltf-binary", detectCanonicalMime(body, allowedSet))
	require.NoError(t, validateGLBUploadSize(body, int64(len(body))))
}

func TestDetectCanonicalMimeRejectsCorruptGLB(t *testing.T) {
	t.Parallel()

	allowedSet := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH].PermittedMimeTypes,
	)

	require.NotEqual(t, "model/gltf-binary", detectCanonicalMime([]byte(strings.Repeat("x", 64)), allowedSet))
}

func TestDetectCanonicalMimeRecognizesConsecutiveMPEGLayerIIIFrames(t *testing.T) {
	t.Parallel()

	const frameLength = 417
	body := make([]byte, frameLength*2)
	binary.BigEndian.PutUint32(body[0:4], 0xfffb9064)
	binary.BigEndian.PutUint32(body[frameLength:frameLength+4], 0xfffb9064)
	allowedSet := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO].PermittedMimeTypes,
	)

	require.Equal(t, "audio/mpeg", detectCanonicalMime(body, allowedSet))
	require.Empty(t, detectMPEGAudioMime(body[:frameLength], allowedSet))
}

func TestDetectCanonicalMimeRecognizesRepositoryMP3Fixture(t *testing.T) {
	t.Parallel()

	body, err := os.ReadFile("../testutil/testdata/media/release-audio-001.mp3")
	require.NoError(t, err)
	allowedSet := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO].PermittedMimeTypes,
	)

	require.Equal(t, "audio/mpeg", detectCanonicalMime(body[:multipartSniffBytes], allowedSet))
}

func TestDetectCanonicalMimeRejectsGLBWithInvalidJSONChunk(t *testing.T) {
	t.Parallel()

	body := buildTestGLB([]byte(`not-json`), 0)
	allowedSet := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_MESH].PermittedMimeTypes,
	)

	require.NotEqual(t, "model/gltf-binary", detectCanonicalMime(body, allowedSet))
}

func TestValidateGLBUploadSizeRejectsDeclaredLengthMismatch(t *testing.T) {
	t.Parallel()

	body := buildTestGLB([]byte(`{"asset":{"version":"2.0"}}`), 1024)

	require.Error(t, validateGLBUploadSize(body, int64(len(body))))
	require.NoError(t, validateGLBUploadSize(body, 1024))
}

func TestValidateMultipartCompletionMimeKeepsStrictPolicyForNonMediaUploads(t *testing.T) {
	t.Parallel()

	_, err := validateMultipartCompletionMime(
		"image/jpeg",
		"image/png",
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_FEATURED_IMAGE].PermittedMimeTypes,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "Uploaded file type does not match this field")
}

func TestValidateMultipartCompletionMimeCanonicalizesFaviconICOAliases(t *testing.T) {
	t.Parallel()
	allowed := model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_SITE_FAVICON].PermittedMimeTypes
	for _, declared := range []string{"image/x-icon", "image/vnd.microsoft.icon"} {
		for _, detected := range []string{"image/x-icon", "image/vnd.microsoft.icon"} {
			mimeType, err := validateMultipartCompletionMime(declared, detected, allowed)
			require.NoError(t, err)
			require.Equal(t, "image/x-icon", mimeType)
		}
	}
	detected := detectCanonicalMime(faviconTestICO(t, 16, 32, 48), buildAllowedMimeSet(allowed))
	require.Equal(t, "image/x-icon", detected)
}

func buildTestGLB(jsonChunk []byte, declaredLength uint32) []byte {
	jsonChunk = append([]byte(nil), jsonChunk...)
	for len(jsonChunk)%4 != 0 {
		jsonChunk = append(jsonChunk, ' ')
	}
	body := make([]byte, 20+len(jsonChunk))
	copy(body[:4], []byte("glTF"))
	binary.LittleEndian.PutUint32(body[4:8], 2)
	if declaredLength == 0 {
		declaredLength = uint32(len(body))
	}
	binary.LittleEndian.PutUint32(body[8:12], declaredLength)
	binary.LittleEndian.PutUint32(body[12:16], uint32(len(jsonChunk)))
	copy(body[16:20], []byte("JSON"))
	copy(body[20:], jsonChunk)
	return body
}
