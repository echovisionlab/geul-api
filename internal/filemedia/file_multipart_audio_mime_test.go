package filemedia

import (
	"encoding/binary"
	"testing"

	"github.com/echovisionlab/geul-api/internal/model"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/stretchr/testify/require"
)

func TestDetectCanonicalMimeRecognizesAIFFAndAIFC(t *testing.T) {
	t.Parallel()

	allowed := buildAllowedMimeSet([]string{"audio/aiff", "audio/x-aiff"})
	tests := map[string][]byte{
		"classic AIFF":          buildTestAIFF(t, "AIFF", buildClassicAIFFCOMM()),
		"real AIFC fl32 prefix": buildRealAIFCFloat32Prefix(),
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, "audio/aiff", detectCanonicalMime(body, allowed))
		})
	}
}

func TestDetectCanonicalMimeRejectsMalformedAIFF(t *testing.T) {
	t.Parallel()

	allowed := buildAllowedMimeSet([]string{"audio/aiff"})

	truncatedAIFCCOMM := make([]byte, 18)
	bodyWithTruncatedCOMM := buildTestAIFF(t, "AIFC", truncatedAIFCCOMM)

	invalidFORMSize := buildTestAIFF(t, "AIFF", buildClassicAIFFCOMM())
	binary.BigEndian.PutUint32(invalidFORMSize[4:8], 4)

	missingCOMM := buildTestAIFFChunked(t, "AIFC", []testAIFFChunk{{
		id:   "FVER",
		data: []byte{0xa2, 0x80, 0x51, 0x40},
	}})
	zeroedCOMM := buildTestAIFF(t, "AIFF", make([]byte, 18))

	for name, body := range map[string][]byte{
		"truncated AIFC COMM": bodyWithTruncatedCOMM,
		"invalid FORM size":   invalidFORMSize,
		"missing COMM":        missingCOMM,
		"zeroed COMM":         zeroedCOMM,
		"signature only":      []byte("FORM\x00\x00\x00\x04AIFC"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, detectAIFFMime(body, allowed))
		})
	}
}

func TestDetectCanonicalMimeRecognizesFLACStreamInfo(t *testing.T) {
	t.Parallel()

	body := buildTestFLAC()
	allowed := buildAllowedMimeSet([]string{"audio/flac"})
	require.Equal(t, "audio/flac", detectCanonicalMime(body, allowed))

	require.Empty(t, detectFLACMime(body[:8], allowed))

	wrongFirstBlock := append([]byte(nil), body...)
	wrongFirstBlock[4] = 0x84
	require.Empty(t, detectFLACMime(wrongFirstBlock, allowed))

	wrongStreamInfoLength := append([]byte(nil), body...)
	wrongStreamInfoLength[7] = 33
	require.Empty(t, detectFLACMime(wrongStreamInfoLength, allowed))

	zeroedStreamInfo := append([]byte(nil), body...)
	clear(zeroedStreamInfo[8:42])
	require.Empty(t, detectFLACMime(zeroedStreamInfo, allowed))

	unknownMinimumFrameSize := append([]byte(nil), body...)
	copy(unknownMinimumFrameSize[15:18], []byte{0, 4, 0})
	require.Equal(t, "audio/flac", detectFLACMime(unknownMinimumFrameSize, allowed))

	unknownMaximumFrameSize := append([]byte(nil), body...)
	copy(unknownMaximumFrameSize[12:15], []byte{0, 4, 0})
	require.Equal(t, "audio/flac", detectFLACMime(unknownMaximumFrameSize, allowed))

	descendingKnownFrameSizes := append([]byte(nil), body...)
	copy(descendingKnownFrameSizes[12:15], []byte{0, 8, 0})
	copy(descendingKnownFrameSizes[15:18], []byte{0, 4, 0})
	require.Empty(t, detectFLACMime(descendingKnownFrameSizes, allowed))
}

func TestDetectCanonicalMimeRecognizesConsecutiveAACADTSFrames(t *testing.T) {
	t.Parallel()

	allowed := buildAllowedMimeSet([]string{"audio/aac"})
	first := buildTestADTSFrame(32, 4)
	second := buildTestADTSFrame(32, 4)
	body := append(first, second...)

	require.Equal(t, "audio/aac", detectCanonicalMime(body, allowed))
	require.Empty(t, detectAACMime(first, allowed))

	mismatchedRate := append(append([]byte(nil), first...), buildTestADTSFrame(32, 3)...)
	require.Empty(t, detectAACMime(mismatchedRate, allowed))

	truncatedSecondFrame := body[:len(body)-1]
	require.Empty(t, detectAACMime(truncatedSecondFrame, allowed))

	falsePositiveSync := append([]byte(nil), body...)
	falsePositiveSync[4] = 0xff
	falsePositiveSync[5] = 0xff
	require.Empty(t, detectAACMime(falsePositiveSync, allowed))
}

func TestDetectCanonicalMimeRecognizesStructuredAACADIF(t *testing.T) {
	t.Parallel()

	allowed := buildAllowedMimeSet([]string{"audio/aac"})
	body := buildTestADIF()
	require.Equal(t, "audio/aac", detectCanonicalMime(body, allowed))

	require.Empty(t, detectAACMime([]byte("ADIF"), allowed))
	require.Empty(t, detectAACMime([]byte("ADIF\x00\x00\x00\x00\x00\x00\x00\x00"), allowed))
	require.Empty(t, detectAACMime(body[:len(body)-1], allowed))
}

func TestDetectCanonicalMimeClassifiesWebMByTrackType(t *testing.T) {
	t.Parallel()

	allWebM := buildAllowedMimeSet([]string{"audio/webm", "video/webm"})
	audioOnly := buildTestWebM(ebmlTrackTypeAudio)
	videoOnly := buildTestWebM(ebmlTrackTypeVideo)
	audioAndVideo := buildTestWebM(ebmlTrackTypeAudio, ebmlTrackTypeVideo)

	require.Equal(t, "audio/webm", detectCanonicalMime(audioOnly, allWebM))
	require.Equal(t, "video/webm", detectCanonicalMime(videoOnly, allWebM))
	require.Equal(t, "video/webm", detectCanonicalMime(audioAndVideo, allWebM))

	require.Empty(t, detectCanonicalMime(videoOnly, buildAllowedMimeSet([]string{"audio/webm"})))
	require.Empty(t, detectCanonicalMime(audioOnly, buildAllowedMimeSet([]string{"video/webm"})))
}

func TestDetectCanonicalMimeRejectsUnclassifiedWebM(t *testing.T) {
	t.Parallel()

	bodyWithoutTracks := buildTestWebM()
	require.Empty(t, detectCanonicalMime(bodyWithoutTracks, buildAllowedMimeSet([]string{"audio/webm"})))
	require.Equal(t, "video/webm", detectCanonicalMime(bodyWithoutTracks, buildAllowedMimeSet([]string{"video/webm"})))

	truncatedTracks := buildTestWebM(ebmlTrackTypeAudio)
	truncatedTracks = truncatedTracks[:len(truncatedTracks)-1]
	require.Empty(t, detectCanonicalMime(truncatedTracks, buildAllowedMimeSet([]string{"audio/webm"})))
	require.Equal(t, "video/webm", detectCanonicalMime(truncatedTracks, buildAllowedMimeSet([]string{"video/webm"})))
}

func TestDetectCanonicalMimeKeepsMatroskaFallbackSeparateFromWebM(t *testing.T) {
	t.Parallel()

	matroska := buildTestEBMLContainer("matroska", nil)
	allowed := buildAllowedMimeSet([]string{"video/x-matroska", "video/webm"})
	require.Equal(t, "video/x-matroska", detectCanonicalMime(matroska, allowed))
}

func TestAudioContainerDetectorsRespectAllowedSet(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		body     []byte
		expected string
		detect   func([]byte, map[string]struct{}) string
	}{
		"AIFF": {
			body:     buildTestAIFF(t, "AIFF", buildClassicAIFFCOMM()),
			expected: "audio/aiff",
			detect:   detectAIFFMime,
		},
		"FLAC": {
			body:     buildTestFLAC(),
			expected: "audio/flac",
			detect:   detectFLACMime,
		},
		"AAC": {
			body:     append(buildTestADTSFrame(32, 4), buildTestADTSFrame(32, 4)...),
			expected: "audio/aac",
			detect:   detectAACMime,
		},
		"WebM": {
			body:     buildTestWebM(ebmlTrackTypeAudio),
			expected: "audio/webm",
			detect:   detectMatroskaMime,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Empty(t, test.detect(test.body, map[string]struct{}{}))
			require.Empty(t, test.detect(test.body, buildAllowedMimeSet([]string{"audio/mpeg"})))
			require.Equal(t, test.expected, detectCanonicalMime(test.body, buildAllowedMimeSet([]string{test.expected})))
		})
	}
}

func TestEditorAudioAllowedMimeTypesDetectNewlyCoveredContainers(t *testing.T) {
	t.Parallel()

	allowed := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO].PermittedMimeTypes,
	)
	tests := map[string]struct {
		body     []byte
		expected string
	}{
		"AIFF": {body: buildTestAIFF(t, "AIFF", buildClassicAIFFCOMM()), expected: "audio/aiff"},
		"AIFC fl32": {
			body:     buildRealAIFCFloat32Prefix(),
			expected: "audio/aiff",
		},
		"FLAC":       {body: buildTestFLAC(), expected: "audio/flac"},
		"AAC ADTS":   {body: append(buildTestADTSFrame(32, 4), buildTestADTSFrame(32, 4)...), expected: "audio/aac"},
		"AAC ADIF":   {body: buildTestADIF(), expected: "audio/aac"},
		"audio WebM": {body: buildTestWebM(ebmlTrackTypeAudio), expected: "audio/webm"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, test.expected, detectCanonicalMime(test.body, allowed))
		})
	}
}

func TestWebMTrackPolicyUsesUploadTypeAllowedSet(t *testing.T) {
	t.Parallel()

	audioAllowed := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO].PermittedMimeTypes,
	)
	videoAllowed := buildAllowedMimeSet(
		model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_VIDEO].PermittedMimeTypes,
	)

	audioWebM := buildTestWebM(ebmlTrackTypeAudio)
	videoWebM := buildTestWebM(ebmlTrackTypeAudio, ebmlTrackTypeVideo)
	require.Equal(t, "audio/webm", detectCanonicalMime(audioWebM, audioAllowed))
	require.Empty(t, detectCanonicalMime(videoWebM, audioAllowed))
	require.Empty(t, detectCanonicalMime(audioWebM, videoAllowed))
	require.Equal(t, "video/webm", detectCanonicalMime(videoWebM, videoAllowed))
}

func TestValidateMultipartCompletionMimeCanonicalizesAIFFAlias(t *testing.T) {
	t.Parallel()

	allowed := model.DefaultUploadConfigs[managev1.UploadType_UPLOAD_TYPE_EDITOR_AUDIO].PermittedMimeTypes
	mimeType, err := validateMultipartCompletionMime("audio/x-aiff", "audio/aiff", allowed)
	require.NoError(t, err)
	require.Equal(t, "audio/aiff", mimeType)
}

type testAIFFChunk struct {
	id   string
	data []byte
}

func buildTestAIFF(t *testing.T, formType string, comm []byte) []byte {
	chunks := []testAIFFChunk{}
	if formType == "AIFC" {
		chunks = append(chunks, testAIFFChunk{
			id:   "FVER",
			data: []byte{0xa2, 0x80, 0x51, 0x40},
		})
	}
	chunks = append(chunks, testAIFFChunk{id: "COMM", data: comm})
	return buildTestAIFFChunked(t, formType, chunks)
}

func buildTestAIFFChunked(t *testing.T, formType string, chunks []testAIFFChunk) []byte {
	t.Helper()
	require.Len(t, formType, 4)

	body := make([]byte, 12)
	copy(body[:4], "FORM")
	copy(body[8:12], formType)
	for _, chunk := range chunks {
		require.Len(t, chunk.id, 4)
		header := make([]byte, 8)
		copy(header[:4], chunk.id)
		binary.BigEndian.PutUint32(header[4:8], uint32(len(chunk.data)))
		body = append(body, header...)
		body = append(body, chunk.data...)
		if len(chunk.data)%2 != 0 {
			body = append(body, 0)
		}
	}
	binary.BigEndian.PutUint32(body[4:8], uint32(len(body)-8))
	return body
}

func buildClassicAIFFCOMM() []byte {
	comm := make([]byte, 18)
	binary.BigEndian.PutUint16(comm[0:2], 1)
	binary.BigEndian.PutUint32(comm[2:6], 100)
	binary.BigEndian.PutUint16(comm[6:8], 24)
	copy(comm[8:18], []byte{0x40, 0x0e, 0xbb, 0x80})
	return comm
}

func buildRealAIFCFloat32Prefix() []byte {
	body := make([]byte, multipartSniffBytes)
	copy(body[:4], "FORM")
	binary.BigEndian.PutUint32(body[4:8], 0xb58e0034)
	copy(body[8:12], "AIFC")
	copy(body[12:16], "COMM")
	binary.BigEndian.PutUint32(body[16:20], 24)
	binary.BigEndian.PutUint16(body[20:22], 1)
	binary.BigEndian.PutUint32(body[22:26], 0x2d638000)
	binary.BigEndian.PutUint16(body[26:28], 32)
	copy(body[28:38], []byte{0x40, 0x10, 0xbb, 0x80, 0, 0, 0, 0, 0, 0})
	copy(body[38:42], "fl32")
	copy(body[44:48], "SSND")
	binary.BigEndian.PutUint32(body[48:52], 0xb58e0008)
	return body
}

func buildTestFLAC() []byte {
	body := make([]byte, 42)
	copy(body[:4], "fLaC")
	body[4] = 0x80
	body[7] = 34
	streamInfo := body[8:42]
	binary.BigEndian.PutUint16(streamInfo[0:2], 4096)
	binary.BigEndian.PutUint16(streamInfo[2:4], 4096)
	sampleDescription := uint64(48000)<<44 | uint64(1)<<41 | uint64(23)<<36 | 1000
	binary.BigEndian.PutUint64(streamInfo[10:18], sampleDescription)
	return body
}

func buildTestADTSFrame(frameLength int, sampleRateIndex byte) []byte {
	body := make([]byte, frameLength)
	body[0] = 0xff
	body[1] = 0xf1
	body[2] = 0x40 | sampleRateIndex<<2
	body[3] = 0x80 | byte(frameLength>>11)
	body[4] = byte(frameLength >> 3)
	body[5] = byte(frameLength&7)<<5 | 0x1f
	body[6] = 0xfc
	return body
}

type testBitWriter struct {
	data   []byte
	bitPos int
}

func (w *testBitWriter) write(value uint64, count int) {
	for bit := count - 1; bit >= 0; bit-- {
		if w.bitPos%8 == 0 {
			w.data = append(w.data, 0)
		}
		if value&(uint64(1)<<bit) != 0 {
			w.data[len(w.data)-1] |= 1 << (7 - w.bitPos%8)
		}
		w.bitPos++
	}
}

func (w *testBitWriter) align() {
	if remainder := w.bitPos % 8; remainder != 0 {
		w.write(0, 8-remainder)
	}
}

func buildTestADIF() []byte {
	writer := testBitWriter{}
	writer.write(0, 1)
	writer.write(0, 1)
	writer.write(0, 1)
	writer.write(1, 1)
	writer.write(128000, 23)
	writer.write(0, 4)

	writer.write(0, 4)
	writer.write(1, 2)
	writer.write(4, 4)
	writer.write(1, 4)
	writer.write(0, 4)
	writer.write(0, 4)
	writer.write(0, 2)
	writer.write(0, 3)
	writer.write(0, 4)
	writer.write(0, 1)
	writer.write(0, 1)
	writer.write(0, 1)
	writer.write(1, 1)
	writer.write(0, 4)
	writer.align()
	writer.write(0, 8)
	writer.write(0x55, 8)

	return append([]byte("ADIF"), writer.data...)
}

func buildTestWebM(trackTypes ...uint64) []byte {
	trackEntries := []byte{}
	for _, trackType := range trackTypes {
		entryData := append(testEBMLElement([]byte{0xd7}, []byte{0x01}), testEBMLElement([]byte{0x83}, []byte{byte(trackType)})...)
		trackEntries = append(trackEntries, testEBMLElement([]byte{0xae}, entryData)...)
	}

	tracks := testEBMLElement([]byte{0x16, 0x54, 0xae, 0x6b}, trackEntries)
	segmentData := append(testEBMLElement([]byte{0xec}, []byte{0xaa, 0xbb}), tracks...)
	return buildTestEBMLContainer("webm", segmentData)
}

func buildTestEBMLContainer(docType string, segmentData []byte) []byte {
	docTypeElement := testEBMLElement([]byte{0x42, 0x82}, []byte(docType))
	header := testEBMLElement([]byte{0x1a, 0x45, 0xdf, 0xa3}, docTypeElement)
	segment := testEBMLElement([]byte{0x18, 0x53, 0x80, 0x67}, segmentData)
	return append(header, segment...)
}

func testEBMLElement(id []byte, data []byte) []byte {
	if len(data) >= 127 {
		panic("test EBML fixture only supports one-byte sizes")
	}
	body := append([]byte(nil), id...)
	body = append(body, 0x80|byte(len(data)))
	return append(body, data...)
}
