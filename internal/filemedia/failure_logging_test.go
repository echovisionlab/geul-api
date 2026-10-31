package filemedia

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/failurecause"
	telemetry "github.com/echovisionlab/geul-telemetry"
	"github.com/stretchr/testify/require"
)

func TestFileFolderConflictKeepsOriginalCauseForTheRequestRecord(t *testing.T) {
	cause := fmt.Errorf("create folder: %w", errors.New(`duplicate key violates constraint "file_folder_name_key": <folder> {detail} Private Folder private@example.test token=hidden`))
	err := fileManagerMutationError(cause, "folder already exists", "Private Folder")
	require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
	require.Equal(t, "folder already exists", err.(*connect.Error).Message()) //nolint:errorlint // the mapper returns *connect.Error directly

	logged := failurecause.Message(err)
	require.Contains(t, logged, `create folder: duplicate key violates constraint "file_folder_name_key": <folder> {detail}`)
	for _, private := range []string{"Private Folder", "private@example.test", "hidden"} {
		require.NotContains(t, logged, private)
	}
}

func TestPrepareClientMediaUploadRejectsNilRequestWithoutLogging(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug}))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	service := &FileService{}
	_, err := service.PrepareClientMediaUpload(t.Context(), nil)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	// The request log records the rejected request once.
	require.Empty(t, output.String())
}

func TestClientWaveformDecodeLogsCauseWithoutBody(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	err := validateClientWaveform([]byte(`{"private@example.test":`))
	require.EqualError(t, err, "invalid waveform JSON")
	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Contains(t, entry["failure_cause"], "unexpected end of JSON input")
	require.NotContains(t, output.String(), "private@example.test")
}

// The HTTP client wraps the transport error with the requested URL. Logging
// must preserve the transport cause before the client-facing wrapper hides it.
type failedRemoteImportTransport struct{ err error }

func (transport failedRemoteImportTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, transport.err
}

func TestRemoteImportLogsCauseBeforeSafeClientError(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(telemetry.NewNormalizingHandler(slog.NewJSONHandler(&output, nil))))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cause := errors.New("upstream connection refused private@example.test")
	service := &FileService{remoteImportResolver: &remoteImportTestResolver{ips: []net.IP{net.ParseIP("8.8.8.8")}}}
	client := &remoteImportHTTPClient{Client: &http.Client{Transport: failedRemoteImportTransport{err: cause}}}
	request := preparedRemoteImport{opts: remoteFileImportOptions{sourceURL: "https://example.com/source?signature=hidden"}}
	source, err := service.openRemoteImportSourceWithClient(t.Context(), request, nil, remoteImportFailureReporter(nil), client)
	require.Nil(t, source)
	require.ErrorIs(t, err, cause)
	require.Contains(t, err.Error(), "remote download failed")
	require.NotContains(t, err.Error(), "connection refused")

	var entry map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &entry))
	require.Contains(t, entry["failure_cause"], "upstream connection refused")
	require.Contains(t, entry["failure_cause"], "https://example.com/source")
	require.Equal(t, "ERROR", entry["level"])
	require.NotContains(t, output.String(), "private@example.test")
	require.NotContains(t, output.String(), "hidden")
}
