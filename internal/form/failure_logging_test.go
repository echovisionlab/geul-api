package form

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCorruptSubmissionStatsLogsCauseAndStillSkipsRow(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	now := time.Now()
	stats := buildFormSubmissionStats(nil, []formSubmissionStatsRow{{CreatedAt: now, Data: []byte(`{"private":"private-name",`)}}, now)
	require.Empty(t, stats.FieldStats)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Equal(t, "decode_submission_stats", entry["operation"])
	require.Contains(t, entry["failure_cause"], "unexpected end of JSON input")
	require.NotContains(t, output.String(), "private-name")
}

func TestSaveDocumentNilRequestLogsRejectionWithoutPanicking(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	service := &InternalFormService{}
	response, err := service.SaveDocument(t.Context(), nil)
	require.Nil(t, response)
	require.Error(t, err)
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	// An invalid argument is an expected client outcome; request.completed records it at Warn.
	require.Equal(t, "DEBUG", entry["level"])
	require.Equal(t, "SaveDocument", entry["operation"])
	require.Equal(t, "invalid_argument", entry["reason"])
	require.Contains(t, entry["failure_cause"], "request is required")
}
