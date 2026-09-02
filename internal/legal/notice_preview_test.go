package legal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLegalNoticeDispatchAtUsesSevenDayLeadOrNow(t *testing.T) {
	now := time.Date(2026, 8, 6, 9, 0, 0, 0, time.UTC)
	require.Equal(t, now.Add(24*time.Hour), legalNoticeDispatchAt(now, now.Add(8*24*time.Hour)))
	require.Equal(t, now, legalNoticeDispatchAt(now, now.Add(6*24*time.Hour)))
}

func TestAutomaticLegalNoticePreviewTokenRequiresExactSharePath(t *testing.T) {
	token, ok := automaticLegalNoticePreviewToken("https://example.test/s/preview-token")
	require.True(t, ok)
	require.Equal(t, "preview-token", token)

	for _, value := range []string{
		"https://example.test/privacy/preview/id",
		"https://example.test/s/token/extra",
		"https://example.test/s/token?password=forbidden",
		"https://example.test/s/",
	} {
		_, ok := automaticLegalNoticePreviewToken(value)
		require.False(t, ok, value)
	}
}
