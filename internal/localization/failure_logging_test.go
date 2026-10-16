package localization

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAcceptLanguageFallbackLogsCauseWithoutHeader(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	locale := InferPreferredLocaleFromAcceptLanguage("en;q=private-token")
	require.NotNil(t, locale)
	require.Equal(t, "en", *locale)
	require.Contains(t, output.String(), "ParseAcceptLanguage: invalid weight")
	require.NotContains(t, output.String(), "private-token")
}
