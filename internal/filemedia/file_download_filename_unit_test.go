package filemedia

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeNewDownloadFilenameUnit(t *testing.T) {
	t.Parallel()

	got, err := normalizeNewDownloadFilename("  간월재.wav  ")
	require.NoError(t, err)
	assert.Equal(t, "간월재.wav", got)

	for _, value := range []string{
		"",
		" ",
		".",
		"..",
		"folder/file.wav",
		`folder\file.wav`,
		"line\nbreak.wav",
		strings.Repeat("x", maxDownloadFilenameBytes+1),
		string([]byte{0xff}),
	} {
		_, err := normalizeNewDownloadFilename(value)
		assert.Error(t, err, value)
	}
}

func TestCanonicalDownloadFilenameFallsBackForHistoricalInvalidValuesUnit(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	invalid := "../source.wav"
	assert.Equal(
		t,
		"download-"+fileID+".wav",
		CanonicalDownloadFilename(&invalid, fileID, "wav"),
	)
	valid := " source.wav "
	assert.Equal(t, "source.wav", CanonicalDownloadFilename(&valid, fileID, "wav"))
}

func TestCanonicalRemoteImportFilenameNormalizesExtensionAndEscapedSeparatorsUnit(t *testing.T) {
	t.Parallel()

	fileID := uuid.NewString()
	assert.Equal(
		t,
		"cover.webp",
		canonicalRemoteImportFilename("cover.jpg", fileID, "image/webp"),
	)
	assert.Equal(
		t,
		"download-"+fileID+".wav",
		canonicalRemoteImportFilename("folder/file.aif", fileID, "audio/wav"),
	)
}
