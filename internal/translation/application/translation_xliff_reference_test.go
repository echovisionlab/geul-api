package application

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/mediaauth"
	"github.com/stretchr/testify/require"
)

func TestXLIFFArtifactExpiryKeepsTheWireTimestampRangeAndFutureRequirement(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name   string
		expiry time.Time
		valid  bool
	}{
		{"future", now.Add(time.Nanosecond), true},
		{"last wire timestamp", time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC), true},
		{"missing", time.Time{}, false},
		{"expired", now.Add(-time.Nanosecond), false},
		{"at expiry", now, false},
		{"out of wire range", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ref := &mediaauth.Reference{FileID: "33333333-3333-4333-8333-333333333333", URL: "https://media.example/export.xlf", Purpose: mediaauth.PurposeDownload, ExpiresAt: test.expiry}
			err := validateTranslationXLIFFArtifactRef(ref, now)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, "translation XLIFF artifact must be short-lived")
			}
		})
	}
}
