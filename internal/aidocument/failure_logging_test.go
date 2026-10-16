package aidocument

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvalidCursorOffsetLogsParseCauseWithoutCursor(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	_, err := decodeCursor("c1.private-token.binding", "binding")
	require.Error(t, err)
	require.EqualError(t, err, "invalid cursor offset")
	require.Contains(t, output.String(), "invalid syntax")
	require.Contains(t, output.String(), `"level":"WARN"`)
	require.NotContains(t, output.String(), "private-token")
}
