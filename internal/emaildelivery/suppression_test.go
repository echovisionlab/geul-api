package emaildelivery

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEmailSuppressionDecisionPolicy(t *testing.T) {
	now := time.Date(2026, 1, 9, 11, 12, 13, 0, time.UTC)
	referenceID := "reference-1"
	releasedBy := "admin-1"

	require.Equal(t, []string{"fan@example.com", "other@example.com"}, normalizeEmailSuppressionList([]string{
		"",
		" FAN@example.com ",
		"fan@example.com",
		"other@example.com",
		"  ",
	}))

	lastError := "550 user unknown"
	releasedAt := now.Add(time.Minute)
	proto := emailSuppressionMessage(Suppression{
		ID:           "suppression-1",
		Email:        "fan@example.com",
		Reason:       EmailSuppressionReasonManual,
		Source:       EmailSuppressionSourceAdmin,
		ReferenceID:  &referenceID,
		LastError:    &lastError,
		SuppressedAt: now,
		ReleasedAt:   &releasedAt,
		ReleasedBy:   &releasedBy,
		CreatedAt:    now,
		UpdatedAt:    now,
	})

	require.Equal(t, "suppression-1", proto.Id)
	require.Equal(t, "fan@example.com", proto.Email)
	require.Equal(t, EmailSuppressionReasonManual, proto.Reason)
	require.Equal(t, EmailSuppressionSourceAdmin, proto.Source)
	require.Equal(t, referenceID, proto.GetReferenceId())
	require.Equal(t, lastError, proto.GetLastError())
	require.Equal(t, releasedBy, proto.GetReleasedBy())
	require.NotNil(t, proto.ReleasedAt)
}
