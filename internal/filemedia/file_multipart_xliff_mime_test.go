package filemedia

import (
	"testing"

	"github.com/echovisionlab/geul-api/internal/filemedia/domain"
	"github.com/echovisionlab/geul-api/internal/mediamime"
	"github.com/stretchr/testify/require"
)

func TestGeneralFileAcceptsXLIFFButNotOtherXML(t *testing.T) {
	t.Parallel()

	permitted := domain.DefaultUploadConfigs["UPLOAD_TYPE_GENERAL_FILE"].PermittedMimeTypes
	allowed := buildAllowedMimeSet(permitted)
	xliff := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<xliff xmlns="urn:oasis:names:tc:xliff:document:2.2" version="2.2" srcLang="ko" trgLang="en"><file id="page:1"></file></xliff>`)
	other := []byte(`<?xml version="1.0" encoding="UTF-8"?><catalog><item>xliff</item></catalog>`)

	detected := detectCanonicalMime(xliff, allowed)
	require.Equal(t, "application/xliff+xml", detected)
	require.Equal(t, "xlf", mediamime.GetExtensionFromMime(detected))
	mime, err := validateMultipartCompletionMime("application/x-xliff+xml", detected, permitted)
	require.NoError(t, err)
	require.Equal(t, "application/xliff+xml", mime)

	require.NotContains(t, allowed, detectCanonicalMime(other, allowed))
	require.NotEqual(t, "application/xliff+xml", detectCanonicalMime(xliff, buildAllowedMimeSet([]string{"text/plain"})))
}
