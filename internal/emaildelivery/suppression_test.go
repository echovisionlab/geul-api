package emaildelivery

import (
	"testing"
	"time"

	"github.com/echovisionlab/geul-api/internal/model"
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
	proto := toProtoEmailSuppression(&model.EmailSuppression{
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

func TestSESSuppressionUpdatesAreMonotonic(t *testing.T) {
	tests := []struct {
		name     string
		existing model.EmailSuppression
		incoming string
		want     bool
	}{
		{"admin suppression is authoritative", model.EmailSuppression{Reason: EmailSuppressionReasonInvalidRecipient, Source: EmailSuppressionSourceAdmin}, EmailSuppressionReasonSESComplaint, false},
		{"manual suppression is authoritative", model.EmailSuppression{Reason: EmailSuppressionReasonManual, Source: EmailSuppressionSourceEmailWorker}, EmailSuppressionReasonSESComplaint, false},
		{"complaint cannot regress to bounce", model.EmailSuppression{Reason: EmailSuppressionReasonSESComplaint, Source: EmailSuppressionSourceSESCallback}, EmailSuppressionReasonSESBounce, false},
		{"duplicate complaint is a no-op", model.EmailSuppression{Reason: EmailSuppressionReasonSESComplaint, Source: EmailSuppressionSourceSESCallback}, EmailSuppressionReasonSESComplaint, false},
		{"bounce upgrades to complaint", model.EmailSuppression{Reason: EmailSuppressionReasonSESBounce, Source: EmailSuppressionSourceSESCallback}, EmailSuppressionReasonSESComplaint, true},
		{"invalid recipient upgrades to bounce", model.EmailSuppression{Reason: EmailSuppressionReasonInvalidRecipient, Source: EmailSuppressionSourceEmailWorker}, EmailSuppressionReasonSESBounce, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, shouldApplySESSuppression(tt.existing, tt.incoming))
		})
	}
}
